package p2p

import (
	crand "crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
)

// WebRTCState tracks the lifecycle of a browser peer connection.
type WebRTCState int

const (
	WebRTCIdle WebRTCState = iota
	WebRTCConnecting
	WebRTCConnected
	WebRTCDisconnecting
)

// WebRTCPeer represents a single browser peer connected via WebRTC.
type WebRTCPeer struct {
	ID              string
	PeerConnection  *webrtc.PeerConnection
	DataChannel     *webrtc.DataChannel
	State           WebRTCState
	ConnectedAt     time.Time
	LastActivity    time.Time
	mu              sync.Mutex
}

// WebRTCBridge manages all browser peer connections via pion/webrtc.
type WebRTCBridge struct {
	mu           sync.RWMutex
	peers        map[string]*WebRTCPeer
	maxPeers     int
	config       *webrtc.Configuration
	coordinator  *Swarm
	rateLimiter  chan struct{} // semaphore: max concurrent sends
}

// NewWebRTCBridge creates a bridge backed by the given ICE config.
func NewWebRTCBridge(iceServers []webrtc.ICEServer, coordinator *Swarm, maxPeers int) *WebRTCBridge {
	if maxPeers <= 0 {
		maxPeers = 3
	}
	return &WebRTCBridge{
		peers:       make(map[string]*WebRTCPeer),
		maxPeers:    maxPeers,
		config:      &webrtc.Configuration{ICEServers: iceServers},
		coordinator: coordinator,
		rateLimiter: make(chan struct{}, 8), // 8 concurrent sends max
	}
}

// CreateOffer generates an SDP offer for a new browser peer.
func (wb *WebRTCBridge) CreateOffer() (*webrtc.SessionDescription, string, error) {
	wb.mu.RLock()
	count := len(wb.peers)
	wb.mu.RUnlock()

	if count >= wb.maxPeers {
		return nil, "", fmt.Errorf("connection limit reached (%d/%d)", count, wb.maxPeers)
	}

	peerConn, err := webrtc.NewPeerConnection(*wb.config)
	if err != nil {
		return nil, "", fmt.Errorf("create peer connection: %w", err)
	}

	peerID, err := generatePeerID()
	if err != nil {
		peerConn.Close()
		return nil, "", fmt.Errorf("generate peer ID: %w", err)
	}

	// Create ordered data channel for chunk metadata
	dc, err := peerConn.CreateDataChannel("chunks", &webrtc.DataChannelInit{
		Ordered: &[]bool{true}[0],
	})
	if err != nil {
		peerConn.Close()
		return nil, "", fmt.Errorf("create data channel: %w", err)
	}

	peer := &WebRTCPeer{
		ID:             peerID,
		PeerConnection: peerConn,
		DataChannel:    dc,
		State:          WebRTCConnecting,
		ConnectedAt:    time.Now(),
		LastActivity:   time.Now(),
	}

	wb.mu.Lock()
	wb.peers[peerID] = peer
	wb.mu.Unlock()

	// Handle connection state changes
	peerConn.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		switch state {
		case webrtc.PeerConnectionStateConnected:
			peer.mu.Lock()
			peer.State = WebRTCConnected
			peer.LastActivity = time.Now()
			peer.mu.Unlock()
			log.Printf("p2p: webrtc peer %s connected", peerID)
		case webrtc.PeerConnectionStateDisconnected, webrtc.PeerConnectionStateFailed:
			peer.mu.Lock()
			peer.State = WebRTCDisconnecting
			peer.mu.Unlock()
			wb.removePeer(peerID)
			log.Printf("p2p: webrtc peer %s disconnected", peerID)
		}
	})

	// Handle incoming messages from browser
	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		peer.mu.Lock()
		peer.LastActivity = time.Now()
		peer.mu.Unlock()
		wb.handleBrowserMessage(peerID, msg.Data)
	})

	// Create SDP offer
	offer, err := peerConn.CreateOffer(&webrtc.OfferOptions{})
	if err != nil {
		wb.removePeer(peerID)
		return nil, "", fmt.Errorf("create offer: %w", err)
	}

	// Set local description and wait for ICE gathering
	gatherComplete := webrtc.GatheringCompletePromise(peerConn)
	if err := peerConn.SetLocalDescription(offer); err != nil {
		wb.removePeer(peerID)
		return nil, "", fmt.Errorf("set local description: %w", err)
	}

	select {
	case <-gatherComplete:
	case <-time.After(10 * time.Second):
		wb.removePeer(peerID)
		return nil, "", fmt.Errorf("ICE gathering timed out")
	}

	return peerConn.LocalDescription(), peerID, nil
}

// HandleAnswer processes the browser's SDP answer to complete the handshake.
func (wb *WebRTCBridge) HandleAnswer(peerID string, answer webrtc.SessionDescription) error {
	wb.mu.RLock()
	peer, exists := wb.peers[peerID]
	wb.mu.RUnlock()

	if !exists {
		return fmt.Errorf("peer %s not found", peerID)
	}

	if err := peer.PeerConnection.SetRemoteDescription(answer); err != nil {
		return fmt.Errorf("set remote description: %w", err)
	}

	return nil
}

// SendChunk sends a video chunk to a specific browser peer.
func (wb *WebRTCBridge) SendChunk(peerID string, data []byte) error {
	wb.mu.RLock()
	peer, exists := wb.peers[peerID]
	wb.mu.RUnlock()

	if !exists {
		return fmt.Errorf("peer %s not found", peerID)
	}

	peer.mu.Lock()
	if peer.State != WebRTCConnected || peer.DataChannel == nil {
		peer.mu.Unlock()
		return fmt.Errorf("peer %s not connected", peerID)
	}
	peer.mu.Unlock()

	// Rate limit: acquire semaphore
	wb.rateLimiter <- struct{}{}
	defer func() { <-wb.rateLimiter }()

	return peer.DataChannel.Send(data)
}

// BroadcastChunk sends a chunk to all connected browser peers.
func (wb *WebRTCBridge) BroadcastChunk(data []byte) {
	wb.mu.RLock()
	peers := make([]*WebRTCPeer, 0, len(wb.peers))
	for _, p := range wb.peers {
		peers = append(peers, p)
	}
	wb.mu.RUnlock()

	for _, peer := range peers {
		peer.mu.Lock()
		if peer.State == WebRTCConnected && peer.DataChannel != nil {
			peer.mu.Unlock()
			if err := wb.SendChunk(peer.ID, data); err != nil {
				log.Printf("p2p: broadcast to %s failed: %v", peer.ID, err)
			}
		} else {
			peer.mu.Unlock()
		}
	}
}

// ClosePeer disconnects a specific browser peer.
func (wb *WebRTCBridge) ClosePeer(peerID string) error {
	wb.mu.RLock()
	peer, exists := wb.peers[peerID]
	wb.mu.RUnlock()

	if !exists {
		return fmt.Errorf("peer %s not found", peerID)
	}

	peer.mu.Lock()
	peer.State = WebRTCDisconnecting
	peer.mu.Unlock()

	if peer.DataChannel != nil {
		peer.DataChannel.Close()
	}
	if peer.PeerConnection != nil {
		peer.PeerConnection.Close()
	}

	wb.removePeer(peerID)
	return nil
}

// PeerCount returns the number of active browser peers.
func (wb *WebRTCBridge) PeerCount() int {
	wb.mu.RLock()
	defer wb.mu.RUnlock()
	return len(wb.peers)
}

func (wb *WebRTCBridge) removePeer(peerID string) {
	wb.mu.Lock()
	delete(wb.peers, peerID)
	wb.mu.Unlock()
}

func (wb *WebRTCBridge) handleBrowserMessage(peerID string, data []byte) {
	var msg struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		log.Printf("p2p: invalid message from browser peer %s: %v", peerID, err)
		return
	}

	switch msg.Type {
	case "chunk_request":
		// Browser requests a chunk — forward to swarm
		log.Printf("p2p: chunk request from browser peer %s", peerID)
	case "heartbeat":
		// Browser heartbeat — just acknowledge
		log.Printf("p2p: heartbeat from browser peer %s", peerID)
	default:
		log.Printf("p2p: unknown message type '%s' from browser peer %s", msg.Type, peerID)
	}
}

func generatePeerID() (string, error) {
	b := make([]byte, 8)
	if _, err := crand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", b), nil
}

// PeerInfo returns public info about a connected browser peer.
type PeerInfo struct {
	ID        string    `json:"id"`
	State     string    `json:"state"`
	Connected time.Time `json:"connected"`
	LastSeen  time.Time `json:"last_seen"`
}

// ListPeers returns info about all connected browser peers.
func (wb *WebRTCBridge) ListPeers() []PeerInfo {
	wb.mu.RLock()
	defer wb.mu.RUnlock()

	infos := make([]PeerInfo, 0, len(wb.peers))
	for _, p := range wb.peers {
		p.mu.Lock()
		state := "idle"
		switch p.State {
		case WebRTCConnecting:
			state = "connecting"
		case WebRTCConnected:
			state = "connected"
		case WebRTCDisconnecting:
			state = "disconnecting"
		}
		infos = append(infos, PeerInfo{
			ID:        p.ID,
			State:     state,
			Connected: p.ConnectedAt,
			LastSeen:  p.LastActivity,
		})
		p.mu.Unlock()
	}
	return infos
}
