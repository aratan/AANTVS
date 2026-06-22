# ArchiteCture Documentation — Project AANTVS (Peer 2 Peer Streaming)

## 📑 Table of Contents

- [System Overview](#system-overview)
- [Technology Stack](#technology-stack)
- [Project Structure](#project-structure)
- [Components Architecture](#components-architecture)
- [P2P Network Layer](#p2p-network-layer)
- [Media Streaming Pipeline](#media-streaming-pipeline)
- [Data Consensus Mechanisms](#data-consensus-mechanisms)
- [Security & Privacy](#security--privacy)
- [Operational Workflow](#operational-workflow)

---

## 📋 System Overview

**AANTVS** is a decentralized peer-to-peer streaming system for the Spanish Film Archive (Archivo Nacional de Arte y ATRacción en Televisión). The architecture enables users to stream video content directly from each other without centralized servers. Key features: discovery, gossip protocol, WebRTC hole-punching, distributed chunk delivery with QoS metrics and reputation-based peer selection.

**Core Capabilities:**
- ✅ P2P streaming (UDP multicast + TCP gossip)  
- ✅ Distributed consensus for reliable data exchange  
- ✅ Adaptive bitrate based on available bandwidths  

---

## 🛠️ Technology Stack

### Go Monolith (`main.go`) - Core Application Layer
```yaml
language:    go 1.23+          # Goroutines, channels (concurrency patterns)                stdlib-first
webserver:   net/http         # API endpoints for content upload/download            http.Handler
httpmux:     github.com/gorilla/mux  # Clean routing pattern

api/handlers.go — HTTP Handlers Layer
- Upload handler → saves & registers chunk in P2P swarm  
- Download request → resolves peer candidates, starts streaming session  

cmd/server/main.go — Entry point for server mode (starts swarm, serves API)  
```

### Go Modules (`internal/p2p/`) - Distributed Network Module
Standards-compliant modules with clear separation of concerns. Each module has own test suite + integration tests via `sdd-apply` workflow:  

| Package          | Purpose                                    | Key Features                                | Dependencies         |
|------------------|--------------------------------------------|----------------------------------------------|-----------------------|
| **discovery.go** | Bootstrap new peers                       | Multicast UDP, beacon service                | net/udp              |
| **gossip.go**           | Peer discovery & synchronization          | Gossiper peer updates                        | crypto/rand          |
| **webrtc_bridge.go**    | STUN hole-punch via WebRTC               | Signaling (SFU), relay fallback             pion/webrtc/v4       |
| **swarm.go**      | P2P coordination                           | Swarm manager, chunk distribution orchestration                    | net/http            |
| **reputation.go** | Peer trust                                | Scoring mechanism                           | math/rand         |

---

## 🗂️ Project Structure (Complete)

```text
AANTVS/
├── cmd/aantvs-server/main.go — Server entry point, starts swarm + API server  
│                                   - Constructs P2P network from config file      
│                                   - Mounts HTTP handlers for upload/download     
├── internal/p2p/go.mod         # Dependencies isolated here (net/http; pion/webrtc/v4)         
└── docs/architecture.md        # This page — architecture documentation          
```

| File | Purpose                                     | Lines Changed   | When               |
|------|----------------------------------------------|------------------|--------------------|
| main.go       | Server wiring for P2P module                | ~150             | Latest session     |  
│                 ⚠️ **Pending**    GC garbage collector          | —                   | Suggested task      |

---

## 🧱 Components Architecture

### 1. Discovery Layer (`discovery.go`)
Purpose: Connect new peers, broadcast beacon with peer list to swarm bootstrap discovery process  

Protocol Stack: UDP Multicast Channel (239.0.0.0/8) + Gossip Protocol (RFC5716 style sync updates on `/status` endpoint).

```go
func NewDiscovery(addr string) (*discovery.Discoverer, error) {    // Constructor  
  udpConn := net.ListenMulticastIP("udp", "239.0.0.0/8")           // Multicast channel      → broadcast beacon every second       


return discovery.New(udpConn), nil                                // Discoverable peer list
}
```

### 2. Gossip Layer (`gossip.go`)
Purpose: Peer updates synchronization (status, last-seen timestamps)  

Design Pattern Event Sourcing  
- Events persist in `history` → replay for state reconstruction.   
- State machine transitions emit domain events automatically via Go's reflection mechanism. 

---

## 🌐 P2P Network Layer — Detailed Specifications

### WebRTC Hole-Punch (`webrtc_bridge.go`)
Purpose: Peer-to-peer connection through NAT traversal using STUN signaling (SFU-based fallback).  

Architecture Diagram Conceptually:

```text
                    ┌─────────────────────┐
Peer A            → │ WebRTC Bridge       ├──→│ NAT Traversal          │←──→  Signaling Channel     │ SFU relay server  
      ↓             │ pion/webrtc/v4     │   (STUN/TURN)                │                          │    
    SDP Offer ──────┤ Hole-Punch Logic   ├──────────────▶ Peer B        │                          
                    └─────────────────────┘
```

### STUN Server Binding  
- **Primary signaling**: Via SFU WebRTC server (SFU = Scalable Forwarding Unit)    
  - SDP negotiation: `iceGatherer` + DTLS handshake → direct P2P channel  
  - Fallback relay if hole-punch fails  

---

## 📼 Media Streaming Pipeline — Chunk Delivery Spec

### QoS Overlay Metrics (`qos.js`)
Purpose: Real-time network quality monitoring. Displays throughput, bandwidth availability on MSE player for adaptive bitrate streaming (ABR).  

**Implemented Features:**  
- ✅ Network round-trip latency tracking via TCP RTT measurements  
- ⚠️ **Suggested**: Add Jitter Buffer implementation using Go's `time` package  

### Adaptive Bitrate Logic (`mse-player.js`)
Purpose: Automatically adjust video quality based on network conditions (QoS metrics).  

**Key Implementation Notes:**
1. Initial handshake establishes baseline bitrate via `/api/stream?path={file}` endpoint  
2. MSE source extends `<video>` tag with chunk data sourced from swarm peers  
3. QoS overlay updates client-side UI dynamically every second using WebSocket events  

---

## ⚖️ Data Consensus Mechanisms

### Seed Manager & Relay System (`seed.go`)
Purpose: Distribute initial video chunks to bootstrap swarm discovery process (e.g., first peer joins and needs full file).  

Design Pattern Pub-sub Messaging  
- Publish event: `/publish/{file}/chunk-{n}.ts` → subscribers receive chunk via `http.Client.Get()`  
1. **Equilibrium Manager** (`equil.go`)
   - Purpose: Maintain optimal distribution of chunks across swarm (e.g., all peers hold ~same number of unique files).  

### Reputation System Implementation (`reputation.go`)
Purpose: Rate-limit or drop malicious/slow uploading clients from P2P network automatically.  

Trust Model Byzantine Fault Tolerance  
- **Bad Actor Behavior Detected**: Slow upload → reputation score drops below threshold `score < 0.75`    
→ Peer marked for rate limiting (e.g., `/api/upload/limit?peerId=abc`)

---

## 🔒 Security & Privacy Considerations
1. Encryption in transit: TLS 1.3 + DTLS via WebRTC handshake  
2. Content provenance tracked via hash signatures (`crypto/sha256` on each chunk)  

### Threat Model Mitigation Strategies:
| Attack Vector                 | Defense Mechanism                          | Module            | Status    |
-------------------------------|---------------------------------------------|--------------------|-----------|
Resource starvation (DoS swarm peer bandwidth limits, per-peer rate limiting in `gossip.go`)              | Rate limiter (`ratelimit` middleware)      | ✅ Implemented     | 
Slow uploads (leeching prevention, drop peers with low reputation score from consensus pool via `reputation.go`).               | Reputation-based trust model                | ✅ Implemented     

---

## ♻️ Operational Workflow — Maintenance Tasks
### Recommended Actions:
1. **Implement Gossip Collector** → Clean up stale peer connections periodically (every 5 minutes recommended).  

2. Update health checks to query swarm status endpoint (`/status`) for live metrics display on admin dashboard.  

> ✅ Complete checklist available in Engram memory sessions tagged 'aantvs'  