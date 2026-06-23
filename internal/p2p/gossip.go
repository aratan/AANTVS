package p2p

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/protocol"
)

// PacketType identifies the kind of gossipPayload carried by a P2PPacket.
type PacketType uint8

const (
	PktIndexUpdate PacketType = iota + 1 // Movie index metadata broadcast between peers
	PktChunkRequest                      // Request to fetch chunks from another peer
	PktChunkResponse                     // Response carrying chunk data
	PktHeartbeat       				  // Node alive heartbeat on multicast
)

// P2PPacket is the wire-format envelope for all P2P messages.
type P2PPacket struct {
	Payload json.RawMessage `json:"_payload"`
	Type    PacketType      `json:"type"`
	PeerID  string          `json:"peer_id"`
	Ts      int64           `json:"ts"` // unix ms timestamp
}

// IndexPayload carries gossip metadata about this node's movie index.
type IndexPayload struct {
	TotalStations int            `json:"total_stations"`
	StationHashes []string       `json:"station_hashes"` // SHA-256(URL + Title) per station, for rareness calc
	Items         []InventoryItem `json:"items"`
	Timestamp     int64          `json:"ts"`
}

// ChunkRequestPayload describes a peer's request to fetch chunks (segments) of a station.
type ChunkRequestPayload struct {
	PeerID    string `json:"peer_id"`
	StationURL  string `json:"station_url"` // URL identifying the station being requested
	Requester string `json:"requester"`   // who is asking
}

// ChunkResponsePayload carries back chunk data in response to a request.
type ChunkResponsePayload struct {
	PeerID     string    `json:"peer_id"`
	StationURL string    `json:"station_url"`
	TotalChks  int       `json:"total_chunks"`
	Data       []byte   `json:"data_hex"` // hex-encoded chunk data, up to 256KB
}

// HeartbeatPayload is sent over UDP multicast to discover peers.
type HeartbeatPayload struct {
	PeerID        string `json:"peer_id"`
	McastAddr     string `json:"mcast_addr"`
	HTTPPort      int    `json:"http_port"`
	P2PPort       int    `json:"p2p_port"`
	Timestamp     int64  `json:"ts"`
}

// InventoryItem represents a catalog entry with metadata for P2P inventory broadcast.
type InventoryItem struct {
	PeerID   string `json:"peer_id"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Type     string `json:"type"`
}

// Swarm manages discovered peers and drives the gossip heartbeat loop.
type Swarm struct {
	mu                sync.RWMutex      // guards peers map
	peers             map[string]*Peer  // keyed by peer ID
	config            Config
	peerMgr           *PeerManager
	stopCh            chan struct{}
	stopOnce          sync.Once         // prevents double-close of stopCh
	rarityTracker     map[string]map[string]int // stationURL -> {chunkIdx -> countAcrossPeers}

	// Rarest-first tracking across swarm peers.
	chunkPresenceMu   sync.RWMutex
	stationChunks     map[string]map[int]bool // for rareness calc: station -> {hasChunk: set{idx}}

	// PR #23: Peer reputation tracking
	reputation        *ReputationManager

	// Remote inventories from other peers
	remoteInventories map[string][]InventoryItem // peerID -> items
	inventoryMu       sync.RWMutex

	// libp2p host for discovery and communication
	libp2pHost        *Libp2pHost
}

const defaultHeartbeatInterval = 5 * time.Second
const swarmValidateInterval    = 15 * time.Second
const mcastTTL = 4 // multicast hops

// NewSwarm binds a Swarm to the given config.
func NewSwarm(cfg Config) (*Swarm, error) {
	uid, err := randHex(8)
	if err != nil {
		return nil, fmt.Errorf("p2p: generate peer ID: %w", err)
	}

	s := &Swarm{
		config:            cfg,
		peers:             make(map[string]*Peer),
		peerMgr:           NewPeerManager("aantvs-"+uid, "0.1.0"),
		stopCh:            make(chan struct{}),
		chunkPresenceMu:   sync.RWMutex{},
		stationChunks:     make(map[string]map[int]bool),
		rarityTracker:     make(map[string]map[string]int),
		reputation:        NewReputationManager(),
		remoteInventories: make(map[string][]InventoryItem),
	}
	return s, nil
}

// Start creates a libp2p host and begins peer discovery.
func (sw *Swarm) Start() error {
	// Create libp2p host
	host, err := NewLibp2pHost(sw.config)
	if err != nil {
		return fmt.Errorf("create libp2p host: %w", err)
	}
	sw.libp2pHost = host

	// Connect to seed peers
	for _, seed := range sw.config.SeedPeers {
		if err := sw.libp2pHost.ConnectToPeer(seed); err != nil {
			log.Printf("libp2p: connect to seed %s failed: %v", seed, err)
		}
	}

	// Start periodic inventory broadcast via libp2p
	go sw.broadcastInventoryLoop()

	// Start heartbeat broadcast via libp2p
	go sw.broadcastHeartbeatLoop()

	log.Printf("libp2p: swarm started with %d seed peers", len(sw.config.SeedPeers))
	return nil
}

// broadcastInventoryLoop periodically broadcasts local inventory to all peers.
func (sw *Swarm) broadcastInventoryLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			items := buildLocalInventory()
			sw.broadcastInventoryViaLibp2p(items)
		case <-sw.stopCh:
			return
		}
	}
}

// broadcastInventoryViaLibp2p sends inventory to all connected peers via libp2p streams.
func (sw *Swarm) broadcastInventoryViaLibp2p(items []InventoryItem) {
	if sw.libp2pHost == nil {
		return
	}

	pkt := sw.PublishIndexSnapshot(nil, items)

	// Send inventory to all connected peers
	for _, peerID := range sw.libp2pHost.GetPeers() {
		s, err := sw.libp2pHost.NewStream(context.Background(), peerID, protocol.ID(ProtocolInventory))
		if err != nil {
			continue
		}

		// Encode and send inventory
		if err := json.NewEncoder(s).Encode(pkt); err != nil {
			s.Close()
			continue
		}
		s.Close()
	}
	log.Printf("libp2p: broadcast inventory (%d items) to %d peers", len(items), len(sw.libp2pHost.GetPeers()))
}

// broadcastHeartbeatLoop periodically sends heartbeats via libp2p.
func (sw *Swarm) broadcastHeartbeatLoop() {
	ticker := time.NewTicker(defaultHeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			sw.sendHeartbeatViaLibp2p()
		case <-sw.stopCh:
			return
		}
	}
}

// sendHeartbeatViaLibp2p sends a heartbeat to all connected peers via libp2p streams.
func (sw *Swarm) sendHeartbeatViaLibp2p() {
	if sw.libp2pHost == nil {
		return
	}

	payload := HeartbeatPayload{
		PeerID:    sw.peerMgr.peerID,
		HTTPPort:  sw.config.HTTP.Port,
		P2PPort:   sw.config.P2PPort,
		Timestamp: time.Now().UnixMilli(),
	}

	pkt := P2PPacket{
		Type:    PktHeartbeat,
		PeerID:  payload.PeerID,
		Ts:      payload.Timestamp,
		Payload: marshaled(payload),
	}

	// Send heartbeat to all connected peers
	for _, peerID := range sw.libp2pHost.GetPeers() {
		s, err := sw.libp2pHost.NewStream(context.Background(), peerID, protocol.ID(ProtocolHeartbeat))
		if err != nil {
			continue
		}

		if err := json.NewEncoder(s).Encode(pkt); err != nil {
			s.Close()
			continue
		}
		s.Close()
	}
}

// AddPeer registers a known peer.
func (sw *Swarm) AddPeer(p Peer) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	sw.peers[p.ID] = &p
}

// RemovePeer removes a peer from the swarm.
func (sw *Swarm) RemovePeer(id string) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	delete(sw.peers, id)
}

// ValidatePeers purges peers that have not been heard within validateInterval.
func (sw *Swarm) ValidatePeers(validateInterval time.Duration) {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	now := time.Now()
	for _, p := range sw.peers {
		if now.Sub(p.LastSeen) > validateInterval {
			p.Alive = false
		}
	}
}

// GetAlivePeers takes a thread-safe snapshot of alive peers.
func (sw *Swarm) GetAlivePeers() []Peer {
	sw.mu.RLock()
	defer sw.mu.RUnlock()
	result := make([]Peer, 0, len(sw.peers))
	for _, p := range sw.peers {
		if p.Alive {
			result = append(result, *p)
		}
	}
	return result
}

// GetLibp2pPeerCount returns the number of connected libp2p peers.
func (sw *Swarm) GetLibp2pPeerCount() int {
	if sw.libp2pHost == nil {
		return 0
	}
	return len(sw.libp2pHost.GetPeers())
}

// Libp2pHost returns the underlying libp2p host for direct protocol access.
func (sw *Swarm) Libp2pHost() *Libp2pHost {
	return sw.libp2pHost
}

// GetReputationStats returns reputation statistics for all tracked peers.
func (sw *Swarm) GetReputationStats() []PeerReputationStats {
	return sw.reputation.GetStats()
}

// RarestFirstStrategy picks peers that hold the least common chunks across the swarm.
// For Phase A this is a simple "all alive peers" since we haven't built full rareness calc yet.
func (sw *Swarm) RarestFirstStrategy() []Peer {
	sw.ValidatePeers(swarmValidateInterval)
	return sw.GetAlivePeers()
}

// StationInfo carries minimal metadata for gossip hashing.
type StationInfo struct {
	URL  string
	Name string
}

// PublishIndexSnapshot creates an P2PPacket carrying the current movie index for gossip distribution.
// This implements the "publish snapshot" pattern: data is serialized under no long-lived lock so
// HTTP handlers aren't blocked.
func (sw *Swarm) PublishIndexSnapshot(cards []StationInfo, items []InventoryItem) P2PPacket {
	hashes := make([]string, len(cards))
	for i, c := range cards {
		hashes[i] = c.URL + c.Name // placeholder — real SHA-256 in Phase B
	}
	return P2PPacket{
		Type:   PktIndexUpdate,
		PeerID: sw.peerMgr.peerID,
		Ts:     time.Now().UnixMilli(),
		Payload: marshaled(IndexPayload{
			TotalStations: len(cards),
			StationHashes: hashes,
			Items:         items,
			Timestamp:     time.Now().UnixMilli(),
		}),
	}
}

// GetCombinedInventory returns local items merged with all remote inventories.
func (sw *Swarm) GetCombinedInventory(localItems []InventoryItem) []InventoryItem {
	combined := make([]InventoryItem, 0, len(localItems))

	// Add local items
	combined = append(combined, localItems...)

	// Add remote items
	sw.inventoryMu.RLock()
	for peerID, items := range sw.remoteInventories {
		for _, item := range items {
			item.PeerID = peerID
			combined = append(combined, item)
		}
	}
	sw.inventoryMu.RUnlock()

	return combined
}

// BroadcastInventory sends the local inventory to all peers via libp2p.
func (sw *Swarm) BroadcastInventory(items []InventoryItem) {
	sw.broadcastInventoryViaLibp2p(items)
}

// Stop cleanly shuts down the swarm.
// Safe to call multiple times.
func (sw *Swarm) Stop() {
	sw.stopOnce.Do(func() {
		close(sw.stopCh)
		sw.peerMgr.Stop()
		if sw.libp2pHost != nil {
			sw.libp2pHost.Close()
		}
		log.Println("p2p: swarm stopped")
	})
}

// marshaled helper serializes a value to JSON or returns nil.
func marshaled(v interface{}) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return data
}
