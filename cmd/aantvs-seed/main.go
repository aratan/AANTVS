// Command aantvs-seed runs a lightweight seed/relay node for the AANTVS P2P swarm.
//
// A seed node accepts TCP connections from peers and transparently relays
// traffic between them, acting as a fallback when direct NAT traversal fails.
// It also provides bootstrap data (station hashes) to new peers on connect.
//
// Usage:
//
//	aantvs-seed [-port 9302] [-config ~/.aantvs/config.json]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"aantvs/internal/p2p"
)

type seedServer struct {
	port    int
	config  p2p.Config
	peers   map[net.Conn]string // conn -> peerID
	mu      sync.Mutex
}

func main() {
	port := flag.Int("port", 9302, "TCP port to listen on")
	configPath := flag.String("config", "", "Path to config.json (optional)")
	flag.Parse()

	cfg := p2p.DefaultConfig()
	if *configPath != "" {
		if loaded, err := p2p.LoadConfigFrom(*configPath); err == nil {
			cfg = loaded
		} else {
			log.Printf("seed: config load failed, using defaults: %v", err)
		}
	}

	srv := &seedServer{
		port:   *port,
		config: cfg,
		peers:  make(map[net.Conn]string),
	}

	// Start listening
	addr := fmt.Sprintf(":%d", srv.port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("seed: listen on %s: %v", addr, err)
	}
	defer ln.Close()

	log.Printf("seed: listening on %s (max peers: 50)", addr)

	// Handle graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Println("seed: shutting down...")
		ln.Close()
		srv.mu.Lock()
		for conn := range srv.peers {
			conn.Close()
		}
		srv.mu.Unlock()
		os.Exit(0)
	}()

	// Accept loop
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("seed: accept: %v", err)
			continue
		}

		srv.mu.Lock()
		if len(srv.peers) >= 50 {
			srv.mu.Unlock()
			log.Printf("seed: max peers reached, rejecting %s", conn.RemoteAddr())
			conn.Close()
			continue
		}
		srv.mu.Unlock()

		go srv.handlePeer(conn)
	}
}

func (srv *seedServer) handlePeer(conn net.Conn) {
	peerAddr := conn.RemoteAddr().String()
	log.Printf("seed: new peer connected: %s", peerAddr)

	// Register peer
	srv.mu.Lock()
	srv.peers[conn] = peerAddr
	srv.mu.Unlock()

	defer func() {
		conn.Close()
		srv.mu.Lock()
		delete(srv.peers, conn)
		srv.mu.Unlock()
		log.Printf("seed: peer disconnected: %s", peerAddr)
	}()

	// Send bootstrap data immediately
	if err := srv.sendBootstrap(conn); err != nil {
		log.Printf("seed: bootstrap to %s failed: %v", peerAddr, err)
		return
	}

	// Relay loop: forward bytes between this peer and all others
	srv.relayLoop(conn)
}

func (srv *seedServer) sendBootstrap(conn net.Conn) error {
	// Build index payload with station hashes from config
	bootstrap := p2p.P2PPacket{
		Type:   p2p.PktIndexUpdate,
		PeerID: "seed-" + fmt.Sprintf("%d", srv.port),
		Ts:     time.Now().UnixMilli(),
	}

	payload, err := json.Marshal(bootstrap)
	if err != nil {
		return fmt.Errorf("marshal bootstrap: %w", err)
	}

	// Length-prefix the message (4 bytes, big-endian)
	lenBuf := make([]byte, 4)
	lenBuf[0] = byte(len(payload) >> 24)
	lenBuf[1] = byte(len(payload) >> 16)
	lenBuf[2] = byte(len(payload) >> 8)
	lenBuf[3] = byte(len(payload))

	if _, err := conn.Write(lenBuf); err != nil {
		return fmt.Errorf("send bootstrap length: %w", err)
	}
	if _, err := conn.Write(payload); err != nil {
		return fmt.Errorf("send bootstrap data: %w", err)
	}

	return nil
}

func (srv *seedServer) relayLoop(self net.Conn) {
	// Read from self and forward to all other peers
	buf := make([]byte, 65536)
	for {
		n, err := self.Read(buf)
		if err != nil {
			if err != io.EOF {
				log.Printf("seed: read from %s: %v", srv.peers[self], err)
			}
			return
		}

		// Broadcast to all other peers
		srv.mu.Lock()
		for conn := range srv.peers {
			if conn != self {
				if _, werr := conn.Write(buf[:n]); werr != nil {
					log.Printf("seed: write to %s: %v", srv.peers[conn], werr)
					conn.Close()
					delete(srv.peers, conn)
				}
			}
		}
		srv.mu.Unlock()
	}
}
