package p2p

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	"github.com/multiformats/go-multiaddr"
)

const (
	// ProtocolInventory defines the protocol ID for inventory exchange.
	ProtocolInventory = "/aantvs/inventory/1.0.0"
	// ProtocolStream defines the protocol ID for video streaming.
	ProtocolStream = "/aantvs/stream/1.0.0"
	// ProtocolHeartbeat defines the protocol ID for peer heartbeats.
	ProtocolHeartbeat = "/aantvs/heartbeat/1.0.0"
)

// Libp2pHost wraps a libp2p host with AANTVS-specific discovery and protocols.
type Libp2pHost struct {
	host   host.Host
	cfg    Config
	stopCh chan struct{}
}

// mDNSNotifee implements mdns.Notifee for peer discovery.
type mDNSNotifee struct {
	host host.Host
}

// HandlePeerFound is called when a new peer is discovered via mDNS.
func (n *mDNSNotifee) HandlePeerFound(peerInfo peer.AddrInfo) {
	log.Printf("libp2p: mDNS discovered peer %s at %v", peerInfo.ID, peerInfo.Addrs)

	// Connect to the discovered peer
	if err := n.host.Connect(context.Background(), peerInfo); err != nil {
		log.Printf("libp2p: connect to mDNS peer %s failed: %v", peerInfo.ID, err)
	}
}

// NewLibp2pHost creates a new libp2p host with discovery and protocol handlers.
func NewLibp2pHost(cfg Config) (*Libp2pHost, error) {
	// Parse listen address
	listenAddr, err := multiaddr.NewMultiaddr(cfg.P2P.ListenAddr)
	if err != nil {
		return nil, fmt.Errorf("invalid listen addr %q: %w", cfg.P2P.ListenAddr, err)
	}

	// Create libp2p host with TCP + yamux + Noise encryption
	h, err := libp2p.New(
		libp2p.ListenAddrs(listenAddr),
		libp2p.NATPortMap(),
		libp2p.EnableRelay(),
	)
	if err != nil {
		return nil, fmt.Errorf("create libp2p host: %w", err)
	}

	l := &Libp2pHost{
		host:   h,
		cfg:    cfg,
		stopCh: make(chan struct{}),
	}

	// Setup mDNS discovery for local network
	if cfg.P2P.DiscoveryMode == "mdns" || cfg.P2P.DiscoveryMode == "both" {
		if err := l.setupMDNS(); err != nil {
			log.Printf("libp2p: mDNS setup failed: %v", err)
		}
	}

	// Setup protocol handlers
	l.setupProtocols()

	// Connect to bootstrap/seed peers
	l.connectToSeedPeers()

	log.Printf("libp2p: host started — ID: %s, Addrs: %v", h.ID(), h.Addrs())
	return l, nil
}

// setupMDNS initializes mDNS service for local network discovery.
func (l *Libp2pHost) setupMDNS() error {
	notifee := &mDNSNotifee{host: l.host}
	mdnsService := mdns.NewMdnsService(l.host, "aantvs", notifee)
	_ = mdnsService // Service keeps running in background

	log.Println("libp2p: mDNS discovery enabled (service=aantvs)")
	return nil
}

// setupProtocols registers stream handlers for AANTVS protocols.
func (l *Libp2pHost) setupProtocols() {
	// Handle inventory exchange requests
	l.host.SetStreamHandler(protocol.ID(ProtocolInventory), func(s network.Stream) {
		defer s.Close()
		log.Printf("libp2p: inventory stream from %s", s.Conn().RemotePeer())
		// Inventory handling will be delegated to Swarm
	})

	// Handle heartbeat requests
	l.host.SetStreamHandler(protocol.ID(ProtocolHeartbeat), func(s network.Stream) {
		defer s.Close()

		var pkt P2PPacket
		if err := json.NewDecoder(s).Decode(&pkt); err != nil {
			log.Printf("libp2p: heartbeat decode error: %v", err)
			return
		}

		if pkt.Type == PktHeartbeat {
			log.Printf("libp2p: heartbeat received from %s", s.Conn().RemotePeer())
		}
	})

	// Handle video stream requests
	l.host.SetStreamHandler(protocol.ID(ProtocolStream), func(s network.Stream) {
		defer s.Close()
		peerID := s.Conn().RemotePeer()
		log.Printf("libp2p: stream request from %s", peerID)

		// Read chunk request (JSON header, then binary data follows)
		var req ChunkRequest
		if err := json.NewDecoder(s).Decode(&req); err != nil {
			log.Printf("libp2p: stream decode error: %v", err)
			return
		}

		log.Printf("libp2p: chunk request — file=%s chunk=%d size=%d",
			req.FileID, req.ChunkIdx, req.ChunkSize)

		// Find and read the file
		filePath := filepath.Join("api", req.FileID)
		f, err := os.Open(filePath)
		if err != nil {
			log.Printf("libp2p: file not found: %s", req.FileID)
			resp := ChunkResponse{FileID: req.FileID, Error: "file not found"}
			json.NewEncoder(s).Encode(resp)
			return
		}
		defer f.Close()

		// Get file info
		info, err := f.Stat()
		if err != nil {
			resp := ChunkResponse{FileID: req.FileID, Error: "stat error"}
			json.NewEncoder(s).Encode(resp)
			return
		}

		// Calculate chunk offset
		chunkSize := req.ChunkSize
		if chunkSize == 0 {
			chunkSize = 262144 // 256KB default
		}
		offset := int64(req.ChunkIdx * chunkSize)

		if offset >= info.Size() {
			resp := ChunkResponse{FileID: req.FileID, Error: "offset out of range"}
			json.NewEncoder(s).Encode(resp)
			return
		}

		// Seek and read chunk
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			resp := ChunkResponse{FileID: req.FileID, Error: "seek error"}
			json.NewEncoder(s).Encode(resp)
			return
		}

		buf := make([]byte, chunkSize)
		n, err := io.ReadFull(f, buf)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			resp := ChunkResponse{FileID: req.FileID, Error: "read error"}
			json.NewEncoder(s).Encode(resp)
			return
		}

		// Send response header (JSON), then raw data
		resp := ChunkResponse{
			FileID:    req.FileID,
			ChunkIdx:  req.ChunkIdx,
			Size:      n,
			TotalSize: info.Size(),
		}
		if err := json.NewEncoder(s).Encode(resp); err != nil {
			return
		}

		// Send raw chunk data
		if _, err := s.Write(buf[:n]); err != nil {
			log.Printf("libp2p: write chunk error: %v", err)
			return
		}

		log.Printf("libp2p: sent chunk %d of %s (%d bytes) to %s",
			req.ChunkIdx, req.FileID, n, peerID)
	})

	log.Println("libp2p: protocols registered (inventory, heartbeat, stream)")
}

// connectToSeedPeers connects to configured seed peers.
func (l *Libp2pHost) connectToSeedPeers() {
	for _, seedAddr := range l.cfg.SeedPeers {
		if err := l.ConnectToPeer(seedAddr); err != nil {
			log.Printf("libp2p: connect to seed %s failed: %v", seedAddr, err)
		}
	}
}

// ConnectToPeer connects to a peer by multiaddr string.
// Format: /ip4/x.x.x.x/tcp/port/p2p/PeerID
func (l *Libp2pHost) ConnectToPeer(addr string) error {
	maddr, err := multiaddr.NewMultiaddr(addr)
	if err != nil {
		return fmt.Errorf("invalid multiaddr %q: %w", addr, err)
	}

	peerInfo, err := peer.AddrInfoFromP2pAddr(maddr)
	if err != nil {
		return fmt.Errorf("parse peer info: %w", err)
	}

	if err := l.host.Connect(context.Background(), *peerInfo); err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	log.Printf("libp2p: connected to peer %s at %s", peerInfo.ID, addr)
	return nil
}

// GetPeers returns all connected peer IDs.
func (l *Libp2pHost) GetPeers() []peer.ID {
	return l.host.Network().Peers()
}

// GetPeerID returns the host's peer ID as a string.
func (l *Libp2pHost) GetPeerID() string {
	return l.host.ID().String()
}

// NewStream opens a new stream to a peer for a given protocol.
func (l *Libp2pHost) NewStream(ctx context.Context, peerID peer.ID, proto protocol.ID) (network.Stream, error) {
	return l.host.NewStream(ctx, peerID, proto)
}

// Host returns the underlying libp2p host.
func (l *Libp2pHost) Host() host.Host {
	return l.host
}

// RequestChunk requests a video chunk from a peer.
func (l *Libp2pHost) RequestChunk(ctx context.Context, peerID peer.ID, fileID string, chunkIdx int, chunkSize int) ([]byte, error) {
	s, err := l.host.NewStream(ctx, peerID, protocol.ID(ProtocolStream))
	if err != nil {
		return nil, fmt.Errorf("open stream: %w", err)
	}
	defer s.Close()

	// Send request
	req := ChunkRequest{
		FileID:    fileID,
		ChunkIdx:  chunkIdx,
		ChunkSize: chunkSize,
	}
	if err := json.NewEncoder(s).Encode(req); err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	// Read response header
	var resp ChunkResponse
	if err := json.NewDecoder(s).Decode(&resp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	if resp.Error != "" {
		return nil, fmt.Errorf("peer error: %s", resp.Error)
	}

	// Read raw chunk data
	data := make([]byte, resp.Size)
	if _, err := io.ReadFull(s, data); err != nil {
		return nil, fmt.Errorf("read chunk data: %w", err)
	}

	return data, nil
}

// Close shuts down the libp2p host and all associated services.
func (l *Libp2pHost) Close() error {
	close(l.stopCh)
	return l.host.Close()
}
