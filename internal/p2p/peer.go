package p2p

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net"
	"sync"
	"time"
)

// Peer represents a remote P2P peer known to this node.
type Peer struct {
	ID        string    `json:"id"`
	Addr      string    `json:"addr"`       // TCP address (host:port)
	Connected bool      `json:"connected"`
	LastSeen  time.Time `json:"last_seen"`
	Alive     bool      `json:"alive"`
}

// PeerStore is a thread-safe collection of known peers.
type PeerStore struct {
	mu    sync.RWMutex
	peers map[string]*Peer
}

var p2pPeerStoreMu sync.RWMutex // package-level lock for external access (locks after httpMutex)

// PeerAnnounce is the heartbeat message exchanged between peers.
type PeerAnnounce struct {
	PeerID  string    `json:"pid"`
	Version string    `json:"v"`
	Alive   bool      `json:"alive"`
	Time    time.Time `json:"ts"`
}

// NewPeerStore creates a fresh PeerStore instance.
func NewPeerStore() *PeerStore {
	return &PeerStore{peers: make(map[string]*Peer)}
}

// Add registers or updates a peer in the store.
func (ps *PeerStore) Add(p *Peer) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if _, existing := ps.peers[p.ID]; !existing {
		p.LastSeen = time.Now()
		p.Alive = true
	}
	ps.peers[p.ID] = p
}

// Remove deletes a peer from the store.
func (ps *PeerStore) Remove(id string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	delete(ps.peers, id)
}

// GetAll returns a snapshot of all peers currently in the store.
func (ps *PeerStore) GetAll() []*Peer {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	snap := make([]*Peer, 0, len(ps.peers))
	for _, p := range ps.peers {
		snap = append(snap, &Peer{ID: p.ID, Addr: p.Addr, Connected: p.Connected, LastSeen: p.LastSeen, Alive: p.Alive})
	}
	return snap
}

// Get returns a peer by ID or nil if not found.
func (ps *PeerStore) Get(id string) *Peer {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	if p, ok := ps.peers[id]; ok {
		return &Peer{ID: p.ID, Addr: p.Addr, Connected: p.Connected, LastSeen: p.LastSeen, Alive: p.Alive}
	}
	return nil
}

// UpdateAlive updates the alive status and last-seen time for an existing peer.
func (ps *PeerStore) UpdateAlive(id string, alive bool) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if p, ok := ps.peers[id]; ok {
		p.Alive = alive
		p.LastSeen = time.Now()
	}
}

// AliveCount returns how many peers are currently considered alive.
func (ps *PeerStore) AliveCount() int {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	count := 0
	for _, p := range ps.peers {
		if p.Alive && time.Since(p.LastSeen) < heartbeatInterval {
			count++
		}
	}
	return count
}

// PeerManager orchestrates TCP dialing, heartbeat exchanges, and reconnect logic.
type PeerManager struct {
	store   *PeerStore
	peerID  string
	ctx     chan struct{} // closed to signal shutdown
	version string
}

const heartbeatInterval = 15 * time.Second
const connectTimeout = 3 * time.Second

// HeartbeatChannel returns a channel that can receive PeerAnnounce messages.
var HeartbeatChannel = make(chan []byte, 64)

// NewPeerManager creates a new PeerManager with the given peer ID and version string.
func NewPeerManager(peerID, version string) *PeerManager {
	pm := &PeerManager{
		store:   NewPeerStore(),
		peerID:  peerID,
		ctx:     make(chan struct{}),
		version: version,
	}

	go pm.heartbeatLoop()
	return pm
}

// heartbeatLoop sends periodic heartbeats to all connected peers.
func (pm *PeerManager) heartbeatLoop() {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			pm.broadcastHeartbeat()
		case <-pm.ctx:
			return
		}
	}
}

func (pm *PeerManager) broadcastHeartbeat() {
	peers := pm.store.GetAll()
	ts := time.Now()
	msg := PeerAnnounce{
		PeerID:  pm.peerID,
		Version: pm.version,
		Alive:   true,
		Time:    ts,
	}
	b, err := json.Marshal(msg)
	if err != nil {
		log.Printf("p2p: marshal heartbeat: %v", err)
		return
	}

	for _, p := range peers {
		if p.Connected && time.Since(p.LastSeen) < heartbeatInterval*2 {
			HeartbeatChannel <- append([]byte(nil), b...) // copy before sending
		}
	}
}

// DialPeer attempts a TCP connection to a peer with exponential backoff reconnect.
func (pm *PeerManager) DialPeer(id, addr string) {
	const (
		minBackoff     = 1 * time.Second
		maxBackoff     = 30 * time.Second
		jitterSpread   = 1.0
		retryIntervalS = 5 // seconds between reconnect attempts
	)

	backoff := minBackoff


	for backoff <= maxBackoff {
		conn, err := net.DialTimeout("tcp", addr, connectTimeout)
		if err == nil {
			pm.store.Add(&Peer{ID: id, Addr: addr, Connected: true, LastSeen: time.Now(), Alive: true})
			log.Printf("p2p: connected to peer %s at %s", id, addr)

			// Read from the established connection in a goroutine.
			go pm.readPeer(conn, id, addr)
			return // success, stop retrying
		}

		log.Printf("p2p: failed to connect to peer %s at %s after %v", id, addr, err)

		loggedInterval := retryIntervalS * time.Second
		select {
		case <-time.After(loggedInterval):
			backoff = backoff * 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		case <-pm.ctx:
			return
		}
	}

	log.Printf("p2p: giving up on peer %s at %s (max retries reached)", id, addr)
	pm.store.UpdateAlive(id, false)
}

// readPeer reads from a TCP connection and dispatches messages.
func (pm *PeerManager) readPeer(conn net.Conn, id, addr string) {
	dec := json.NewDecoder(conn)
	for {
		var msg PeerAnnounce
		if err := dec.Decode(&msg); err != nil {
			log.Printf("p2p: peer %s at %s disconnected: %v", id, addr, err)
			pm.store.UpdateAlive(id, false)
			return
		}
		go pm.HandlePeerMsg(msg, addr)
	}
}

// HandlePeerMsg processes an incoming heartbeat/announce from a remote peer.
func (pm *PeerManager) HandlePeerMsg(msg PeerAnnounce, addr string) {
	pm.store.UpdateAlive(msg.PeerID, msg.Alive)

	if pm.store.Get(msg.PeerID) == nil && !msg.Alive {
		return // no point connecting to dead peers with no prior knowledge
	}

	// Try dialing if we don't know this peer yet.
	if pm.store.Get(msg.PeerID) == nil {
		pm.store.Add(&Peer{ID: msg.PeerID, Addr: addr, Connected: false, LastSeen: time.Now(), Alive: msg.Alive})
		id := msg.PeerID // capture for goroutine
		a := addr        // capture for goroutine
		go pm.DialPeer(id, a)
	}
}

// randHex returns n random hex characters.
func randHex(n int) (string, error) {
	b := make([]byte, n)
	_, err := rand.Read(b)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", b), nil
}

// jitteredDuration applies jitter to make backoff less synchronized.
func jitteredDuration(base time.Duration) time.Duration {
	jitter := float64(base) * (0.5 + 0.5*rand.Float64())
	return time.Duration(jitter)
}

// Stop signals all background goroutines to terminate.
func (pm *PeerManager) Stop() {
	close(pm.ctx)
}
