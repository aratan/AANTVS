package p2p

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"sync"
	"time"
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
	TotalStations int         `json:"total_stations"`
	StationHashes []string    `json:"station_hashes"` // SHA-256(URL + Title) per station, for rareness calc
	Timestamp     int64       `json:"ts"`
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

// Swarm manages discovered peers and drives the gossip heartbeat loop.
type Swarm struct {
	mu                sync.RWMutex      // guards peers map
	peers             map[string]*Peer  // keyed by peer ID
	config            Config
	peerMgr           *PeerManager
	stopCh            chan struct{}
	rarityTracker     map[string]map[string]int // stationURL -> {chunkIdx -> countAcrossPeers}

	// Rarest-first tracking across swarm peers.
	chunkPresenceMu   sync.RWMutex
	stationChunks     map[string]map[int]bool // for rareness calc: station -> {hasChunk: set{idx}}

	// PR #23: Peer reputation tracking
	reputation        *ReputationManager
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
		config:        cfg,
		peers:         make(map[string]*Peer),
		peerMgr:       NewPeerManager("aantvs-"+uid, "0.1.0"),
		stopCh:        make(chan struct{}),
		chunkPresenceMu: sync.RWMutex{},
		stationChunks:  make(map[string]map[int]bool),
		rarityTracker:  make(map[string]map[string]int),
		reputation:    NewReputationManager(),
	}
	return s, nil
}

// Start opens UDP multicast on the configured McastAddr and begins gossip.
func (sw *Swarm) Start() error {
	addr, err := net.ResolveUDPAddr("udp4", sw.config.McastAddr)
	if err != nil {
		return fmt.Errorf("resolve mcast addr %s: %w", sw.config.McastAddr, err)
	}

	conn, err := net.ListenMulticastUDP("udp4", nil, addr)
	if err != nil {
		return fmt.Errorf("listen mcast %s: %w", sw.config.McastAddr, err)
	}
	log.Printf("p2p: multicast listening on %s (TTL=%d)", conn.LocalAddr(), mcastTTL)

	go sw.readLoop(conn)
	go sw.broadcastLoop()
	return nil
}

// readLoop receives P2PPacket messages from multicast and dispatches them.
func (sw *Swarm) readLoop(conn *net.UDPConn) {
	buf := make([]byte, 65536)
	for {
		select {
		case <-sw.stopCh:
			conn.Close()
			return
		default:
		}

		n, raddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-sw.stopCh:
				return
			default:
				log.Printf("p2p: mcast read error: %v", err)
				time.Sleep(time.Second)
				continue
			}
		}

		var pkt P2PPacket
		if err := json.Unmarshal(buf[:n], &pkt); err != nil {
			log.Printf("p2p: unmarshal packet from %s: %v", raddr, err)
			continue
		}

		switch pkt.Type {
		case PktHeartbeat:
			sw.handleHeartbeat(pkt.Payload, raddr.String())
		case PktIndexUpdate:
			sw.handleIndexUpdate(pkt.Payload)
		default:
			log.Printf("p2p: unknown packet type %d from %s", pkt.Type, raddr)
		}
	}
}

// handleHeartbeat validates a heartbeat and registers the sender as new peer when appropriate.
func (sw *Swarm) handleHeartbeat(raw json.RawMessage, raddr string) {
	var hb HeartbeatPayload
	if err := json.Unmarshal(raw, &hb); err != nil {
		return
	}

	if hb.PeerID == "" || hb.P2PPort == 0 {
		return // malformed
	}

	sw.mu.Lock()
	defer sw.mu.Unlock()

	existing, ok := sw.peers[hb.PeerID]
	if !ok {
		sw.peers[hb.PeerID] = &Peer{
			ID:     hb.PeerID,
			Addr:   fmt.Sprintf("%s:%d", raddr, hb.P2PPort),
			LastSeen: time.Now(),
			Alive:  true,
		}
		log.Printf("p2p: discovered peer %s at %s (http:%d p2p:%d)", hb.PeerID, raddr, hb.HTTPPort, hb.P2PPort)

		// Kick off TCP dial to newly found peer.
		pw := hb.PeerID // capture for goroutine
		pa := fmt.Sprintf("%s:%d", raddr, hb.P2PPort)
		go sw.peerMgr.DialPeer(pw, pa)
	} else {
		existing.LastSeen = time.Now()
		existing.Alive = true
	}
}

// handleIndexUpdate processes index gossip carrying a peer's full movie index.
func (sw *Swarm) handleIndexUpdate(raw json.RawMessage) {
	var idx IndexPayload
	if err := json.Unmarshal(raw, &idx); err != nil {
		log.Printf("p2p: unmarshal index update: %v", err)
		return
	}

	sw.chunkPresenceMu.Lock()
	for _, stationHash := range idx.StationHashes {
		if sw.stationChunks[stationHash] == nil {
			sw.stationChunks[stationHash] = make(map[int]bool)
		}
	}
	sw.chunkPresenceMu.Unlock()

	log.Printf("p2p: index update received (stations=%d)", idx.TotalStations)
}

// broadcastLoop periodically sends heartbeat packets on multicast.
func (sw *Swarm) broadcastLoop() {
	ticker := time.NewTicker(defaultHeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			sw.sendHeartbeat()
		case <-sw.stopCh:
			return
		}
	}
}

// sendHeartbeat marshals a heartbeat and floods it onto the multicast group.
func (sw *Swarm) sendHeartbeat() {
	addr, err := net.ResolveUDPAddr("udp4", sw.config.McastAddr)
	if err != nil {
		return
	}

	conn, err := net.DialUDP("udp4", nil, addr)
	if err != nil {
		log.Printf("p2p: connect to mcast group: %v", err)
		return
	}
	defer conn.Close()

	payload := HeartbeatPayload{
		PeerID:    sw.peerMgr.peerID,
		McastAddr: sw.config.McastAddr,
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

	data, err := json.Marshal(pkt)
	if err != nil {
		return
	}

	if _, err := conn.Write(data); err != nil {
		log.Printf("p2p: mcast heartbeat write: %v", err)
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
func (sw *Swarm) PublishIndexSnapshot(cards []StationInfo) P2PPacket {
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
			Timestamp:     time.Now().UnixMilli(),
		}),
	}
}

// Stop cleanly shuts down the swarm.
func (sw *Swarm) Stop() {
	close(sw.stopCh)
	sw.peerMgr.Stop()
	log.Println("p2p: swarm stopped")
}

// marshaled helper serializes a value to JSON or returns nil.
func marshaled(v interface{}) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return data
}
