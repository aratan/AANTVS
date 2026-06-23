package p2p

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"sync"
	"time"
)

// HolePunchResult holds the outcome of a NAT traversal attempt.
type HolePunchResult struct {
	DirectConn     net.Conn
	Success        bool
	FallbackAddr   string // empty if direct succeeded
	ExternalIP     net.IP
	ExternalPort   int
}

// STUNClient queries STUN servers to discover the external IP:port.
type STUNClient struct {
	servers []string
	timeout time.Duration
}

// NewSTUNClient creates a client with the given STUN server addresses.
func NewSTUNClient(servers []string) *STUNClient {
	return &STUNClient{
		servers: servers,
		timeout: 5 * time.Second,
	}
}

// QuerySTUN sends a binding request to the first reachable STUN server
// and returns the external IP and port.
func (sc *STUNClient) QuerySTUN() (net.IP, int, error) {
	for _, server := range sc.servers {
		ip, port, err := sc.queryServer(server)
		if err != nil {
			log.Printf("p2p: STUN query to %s failed: %v", server, err)
			continue
		}
		return ip, port, nil
	}
	return nil, 0, fmt.Errorf("all STUN servers failed")
}

func (sc *STUNClient) queryServer(server string) (net.IP, int, error) {
	conn, err := net.DialTimeout("udp4", server, sc.timeout)
	if err != nil {
		return nil, 0, fmt.Errorf("dial STUN %s: %w", server, err)
	}
	defer conn.Close()

	// Simple STUN binding request (RFC 5389)
	// Message type: 0x0001 (Binding Request)
	// Message length: 0
	// Magic cookie: 0x2112A442
	// Transaction ID: 12 random bytes
	msg := make([]byte, 28)
	msg[0] = 0x00 // Type high byte
	msg[1] = 0x01 // Type low byte (Binding Request)
	// Length = 0 (no attributes)
	msg[4] = 0x21 // Magic cookie
	msg[5] = 0x12
	msg[6] = 0xA4
	msg[7] = 0x42
	// Transaction ID (12 bytes of randomness)
	for i := 8; i < 20; i++ {
		msg[i] = byte(i * 7) // deterministic but unique enough
	}

	conn.SetWriteDeadline(time.Now().Add(sc.timeout))
	if _, err := conn.Write(msg); err != nil {
		return nil, 0, fmt.Errorf("send STUN request: %w", err)
	}

	buf := make([]byte, 1024)
	conn.SetReadDeadline(time.Now().Add(sc.timeout))
	n, err := conn.Read(buf)
	if err != nil {
		return nil, 0, fmt.Errorf("read STUN response: %w", err)
	}

	if n < 20 {
		return nil, 0, fmt.Errorf("STUN response too short (%d bytes)", n)
	}

	// Parse STUN Binding Response (type 0x0101)
	if buf[0] != 0x01 || buf[1] != 0x01 {
		return nil, 0, fmt.Errorf("unexpected STUN response type: 0x%02x%02x", buf[0], buf[1])
	}

	// Parse XOR-MAPPED-ADDRESS attribute (0x0020)
	// Attribute header: type(2) + length(2)
	pos := 20 // skip header
	for pos+4 <= n {
		attrType := uint16(buf[pos])<<8 | uint16(buf[pos+1])
		attrLen := uint16(buf[pos+2])<<8 | uint16(buf[pos+3])
		pos += 4

		if attrType == 0x0020 && attrLen >= 8 { // XOR-MAPPED-ADDRESS
			// Family: 0x01 = IPv4
			if buf[pos] == 0x01 {
				ip := net.IPv4(
					buf[pos+4]^0x21,
					buf[pos+5]^0x12,
					buf[pos+6]^0xA4,
					buf[pos+7]^0x42,
				)
				port := int(buf[pos+2])<<8 | int(buf[pos+3])
				port ^= 0x2112 // XOR with magic cookie high bytes
				return ip, port, nil
			}
		}
		pos += int(attrLen)
		// Pad to 4-byte boundary
		if attrLen%4 != 0 {
			pos += 4 - int(attrLen%4)
		}
	}

	return nil, 0, fmt.Errorf("XOR-MAPPED-ADDRESS not found in STUN response")
}

// HolePuncher manages simultaneous UDP probes to traverse NATs.
type HolePuncher struct {
	mu       sync.Mutex
	timeouts map[string]context.CancelFunc // peerID -> cancel
}

// NewHolePuncher creates a new hole-punch manager.
func NewHolePuncher() *HolePuncher {
	return &HolePuncher{
		timeouts: make(map[string]context.CancelFunc),
	}
}

// HolePunch attempts to establish a direct UDP connection to a remote peer
// by sending simultaneous probes. Falls back to relay if punching fails.
func (hp *HolePuncher) HolePunch(
	ctx context.Context,
	localIP net.IP,
	localPort int,
	remoteIP net.IP,
	remotePort int,
	timeout time.Duration,
) *HolePunchResult {
	result := &HolePunchResult{}

	// Try simultaneous UDP probes
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	conn, err := hp.sendProbes(probeCtx, localIP, localPort, remoteIP, remotePort)
	if err == nil {
		result.DirectConn = conn
		result.Success = true
		result.ExternalIP = localIP
		result.ExternalPort = localPort
		return result
	}

	log.Printf("p2p: hole punch failed to %s:%d: %v", remoteIP, remotePort, err)

	// Fallback: return failure, caller should use seed relay
	result.Success = false
	result.FallbackAddr = fmt.Sprintf("%s:%d", remoteIP, remotePort)
	return result
}

func (hp *HolePuncher) sendProbes(
	ctx context.Context,
	localIP net.IP,
	localPort int,
	remoteIP net.IP,
	remotePort int,
) (net.Conn, error) {
	// Create UDP socket for probing
	addr := &net.UDPAddr{IP: localIP, Port: localPort}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		return nil, fmt.Errorf("listen UDP: %w", err)
	}
	// On any error path after this point, close the connection.
	// On success, the caller owns the connection lifetime.

	remoteAddr := &net.UDPAddr{IP: remoteIP, Port: remotePort}

	// Send probe (magic bytes to identify our traffic)
	probe := []byte("AANTVS-PUNCH")
	conn.WriteToUDP(probe, remoteAddr)

	// Wait for response (the remote peer should send the same probe back)
	buf := make([]byte, 1024)
	conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))

	for {
		select {
		case <-ctx.Done():
			conn.Close()
			return nil, fmt.Errorf("hole punch timeout")
		default:
		}

		n, raddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("no response from %s:%d: %w", remoteIP, remotePort, err)
		}

		if string(buf[:n]) == "AANTVS-PUNCH" && raddr.IP.Equal(remoteIP) {
			log.Printf("p2p: hole punch success with %s:%d", remoteIP, remotePort)
			return conn, nil
		}
	}
}

// ExternalAddr holds the result of a STUN query.
type ExternalAddr struct {
	IP   net.IP
	Port int
}

// GetExternalAddr queries STUN to discover the node's external address.
func GetExternalAddr(stunServers []string) (*ExternalAddr, error) {
	client := NewSTUNClient(stunServers)
	ip, port, err := client.QuerySTUN()
	if err != nil {
		return nil, err
	}
	return &ExternalAddr{IP: ip, Port: port}, nil
}

// PeerAddress is used to exchange addresses during hole-punch coordination.
type PeerAddress struct {
	PeerID string `json:"peer_id"`
	IP     string `json:"ip"`
	Port   int    `json:"port"`
}

// EncodePeerAddress serializes a PeerAddress to JSON.
func (pa *PeerAddress) EncodePeerAddress() ([]byte, error) {
	return json.Marshal(pa)
}

// DecodePeerAddress deserializes a PeerAddress from JSON.
func DecodePeerAddress(data []byte) (*PeerAddress, error) {
	var pa PeerAddress
	if err := json.Unmarshal(data, &pa); err != nil {
		return nil, err
	}
	return &pa, nil
}
