# Design: P2P Node Integration — Swarm-First Replication Engine

## Technical Approach

Add an ephemeral P2P replication layer to the existing single-binary HTTP server. The design introduces a background goroutine swarm that gossips movie index metadata (not video itself) using stdlib-only UDP multicast + TCP peering, plus a WebRTC bridge for browser peers via `pion/webrtc`. **Critical finding:** The current project has ZERO external dependencies (`go.mod` has empty `go.sum`). This design phases the P2P engine into two stages:

- **Phase A** — stdlib-only gossip (UDP multicast + TCP handshake) — 0 deps
- **Phase B** — WebRTC bridge via `pion/webrtc` and `pion/turn` — adds 2 deps

The swarm operates as a replication fanout for the index (`[]MovieCard`) from upstream Pastebin, with peers exchanging "rareness" and chunk maps of video segments.

## Architecture Decisions

### Decision: Global State Protection Strategy

**Choice:** Three separate RWMutex instances keyed by data type, not one global mutex.

| Option | Tradeoff | Decision |
|--------|----------|----------|
| One global `sync.RWMutex` on all state | Simple but creates contention between HTTP reads and swarm writes | Rejected |
| Type-keyed RWMutex (peliculasMovies, peliculasGroups, peliculasStations) | More granularity, no cross-type lock ordering needed | **Adopted** |
| Channel-based fanout + snapshot swap via atomic.Pointer | Go 1.19+ compatible but fragile for partial updates | Rejected |

Three mutexes with a strict locking order: `mutexMovies` → `mutexGroups` → `mutexStations`. Never lock in reverse.

### Decision: Concurrency Model — Goroutine-per-Subnet + SwarmCoordinator Singleton

**Choice:** A single `SwarmCoordinator` singleton owns lifecycle (start/stop), peer discovery, and protocol dispatch. Each local subnet gets a UDP multicast listener goroutine. External peers connect via TCP dial initiated by the coordinator's PeerStore iterator (backoff retry). Video chunks are dispatched through channel-buffered work queues.

| Factor | Rationale |
|--------|-----------|
| Startup simplicity | Coordinator wraps all P2P concerns in one struct |
| Testability | `Coordinator.Stop()` guarantees clean goroutine shutdown via context cancellation |
| Error handling | Each peer connection gets its own error channel → coordinator aggregates |

### Decision: Protocol Choice — Stdlib UDP Multicast + TCP (Phase A) vs libp2p

**Choice:** Phase A uses Go stdlib only. `net.IPAddr` with `172.16.0.0/12` multicast group, heartbeat every 250ms TTL=4. TCP handshake for reliable metadata sync (`{peerID, version, peerIndex}`). Phase B adds WebRTC.

| | Stdlib UDP+TCP | libp2p | pion/webrtc-only |
|--|--|--|--|
| Dependencies | 0 extra | +8-10 transitive | 2 core deps |
| NAT traversal | Manual STUN/hole-punch | Built-in relay/dial | ICE via pion/stun |
| Bandwidth overhead | Minimal (UDP small) | Higher (protobuf, crypto) | WebRTC header ~1-2KB/msg |
| Learning curve | Familiar to Go devs | Steep config surface | Moderate |

**Verdict:** Stdlib-first phase minimizes risk. Migration path to libp2p exists later via interface swap (`PeerTransport` abstraction).

### Decision: WebRTC Video Delivery — MSE over DataChannels (not direct media)

**Choice:** Browser peers receive video as MP4 fragments through `RTCDataChannel` frames, reassembled client-side via MediaSource Extensions (MSE). Full video never downloads before playback. Maximum chunk size = 256KB/frame with sequential download priority on "Play" press → rarest-first only during idle swarm sync.

| Concern | Approach |
|---------|----------|
| Bandwidth protection | WebRTC peers rate-limited to 1 peer/stream, 500KB/s cap per swarm instance |
| Gap handling | Rarest-first fills gaps when no user is actively watching; sequential-fill on "Play" |
| Reassembly order guarantee | Each fragment carries `(frameN, sessionID)` — browser MSE appends in order, drops out-of-order |

### Decision: NAT Traversal — STUN + Manual Hole-Punch + Fallback Seed Relay

**Choice:** Three-tier approach with explicit fallback:

1. **STUN** (Phase B only): `pion/stun` queries get external IP/port
2. **Hole-punch**: Both peers open raw UDP sockets to each other's ICE candidates simultaneously (`net.DialUDP` with 500ms timeout per probe)
3. **Fallback seed relay**: If hole-punch fails after 3 rounds, fall back to `SeedRelayConn{tcpConn, localIndex}` which relays chunk payloads transparently

### Decision: Swarm Equilibrium Manager State Machine

**Choice:** A single `atomic.Int32` state machine with transition guards.

```
state = atomic.Int32 // RAREST_FIRST (0) or SEQ_FIRST (1)
modeTransitionDuration = 30s // minimum hold in each mode before switching
nextSwitchAt = time.Now().Add(30s)
```

- User presses Play → `atomic.CompareAndSwap(RAREST_FIRST, SEQ_FIRST)` → downloads chunk N, then N+1…N+k (sequential-fill priority 2x upload rate of idle sync)
- No active playback for 30s → swaps back to RAREST_FIRST (fill gaps, maintain peer redundancy index)

## File Changes

### New Files

| File | Action | Description |
|------|--------|-------------|
| `cmd/aantvs/main.go` | Create (move from root) | Entry point — spawns HTTP server goroutine + P2P SwarmCoordinator goroutine. Config load from `~/.aantvs/config.json`. |
| `internal/p2p/swarm.go` | Create | `SwarmCoordinator` struct — lifecycle, peer store, mode transitions, gap-fill scheduling |
| `internal/p2p/discovery.go` | Create | UDP multicast heartbeat listener (250ms interval), TCP handshake protocol, peer announce/retire flow |
| `internal/p2p/protocol.go` | Create | `P2PPacket` types, gossip payload format, chunk request/response encoding/decoding |
| `internal/p2p/relay.go` | Create | Seed node relay implementation — `SeedRelayer`, channel multiplexing, TCP conn bridging |
| `internal/p2p/nat.go` | Create | STUN client (Phase B), hole-punch logic, UDP probe manager with retry backoff |
| `internal/p2p/webrtc_bridge.go` | Create | WebRTC peer management — ICE config, dataChannel setup, MSE fragment streaming, download rate limiter |
| `internal/p2p/mutex_watch.go` | Create | Deadlock detection via panic-on-recursive-lock (development only) |
| `cmd/aantvs/config.json.example` | Create | Seed peers, STUN servers, WebRTC config template |
| `go.mod` | Modify | Update to `module aantvs`, add Go 1.22+, depend on `pion/webrtc/v3` + `pion/stun/v2` (Phase B only) |

### Modified Files

| File | Action | Description |
|------|--------|-------------|
| `main.go` | Move to `cmd/aantvs/main.go` | Entry point moves here from root. All P2P structs leave this file. The original `Peliculas`, `MovieCard`, `pageData`, and server boilerplate stay as a re-exported `pkg/server/` package or remain in `main.go` if the repo stays ultra-minimal. |
| `openspec/config.yaml` | Modify | Add `p2p-node` to list of active changes, document deps for Phase B |

## Interfaces / Contracts

### P2PPacket Protocol (All Peers Must Implement)

```go
// internal/p2p/protocol.go

type PacketType uint8

const (
    IndexUpdate PacketType = iota + 1
    PeerAnnounce
    ChunkRequest
    ChunkResponse
    SwarmHeartbeat
    VideoChunkRequest
    VideoChunkResponse
)

type P2PPacket struct {
    Version   int       `json:"v"`       // Always 1 until spec change
    PeerID    string    `json:"pid"`     // SHA-256 short hex of node's crypto key, first 16 chars
    MsgType   PacketType `json:"t"`      // Type discriminator
    Timestamp int64     `json:"ts"`      // Unix ms, for stale-heartbeat detection
    Payload   json.RawMessage `json:"p"`  // Type-specific JSON
}

// Per-message payload types:

type IndexPayload struct {
    PeerID         string   `json:"pid"`
    TotalStations  int      `json:"ts"`
    StationHashes  []string `json:"sh"`  // SHA-256 of each station's URL+title, for rareness calc
    LastIndexAt    int64    `json:"lia"` // When this peer last fetched from pastebin
}

type ChunkPayload struct {
    PeerID    string `json:"pid"`
    Index     int    `json:"i"`     // Station index
    ChunkIdx  int    `json:"ci"`    // Frame/chunk number within station
    TotalChks int    `json:"tc"`    // Expected total chunks for this file
    StartTime float64 `json:"st"`   // Segment start time in seconds (MSE offset)
    EndTime   float64 `json:"et"`   // Segment end time in seconds
    Data      []byte  `json:"d"`   // Base64-encoded frame bytes, max 256KB
}

type SwarmHeartbeatPayload struct {
    PeerID         string  `json:"pid"`
    ActivePlaybacks int32  `json:"ap"`  // How many videos this node is currently streaming
    Mode           SwarmMode `json:"m"`   // RAREST_FIRST or SEQ_FIRST
    AvailablePeers []string `json:"ap_list"` // PeerIDs known to this peer (gossip fanout)
}
```

### Mutex Watchdog (Deadlock Prevention)

```go
// internal/p2p/mutex_watch.go — dev-only panic-on-recursive-lock

var mutexChain [32]uintptr // Call stack trace of last acquire per mutex type
var maxMutexDepth = 8      // If depth > maxMutexDepth, panic during development

func (mw *MutexWatchdog) AssertSingle(mutex *sync.RWMutex) {
    // Records callstack; if this mutex was already held by same goroutine, panics.
}
```

### Config File Format (`~/.aantvs/config.json`)

```json
{
  "http": {
    "port": 80
  },
  "p2p": {
    "enabled": true,
    "multicast_group": "172.16.0.0",
    "multicast_port": 9302,
    "heartbeat_interval_ms": 250,
    "ttl": 4,
    "seed_peers": [
      {"addr": "seed1.example.com:9302", "role": "seed"},
      {"addr": "seed2.example.com:9302", "role": "seed"}
    ],
    "stun_servers": ["stun.l.google.com:19302"],
    "webrtc": {
      "iceServers": [
        {"urls": ["stun:stun.l.google.com:19302"]}
      ]
    },
    "chunk_size_bytes": 262144,
    "rate_limit_webRTC": 512000,
    "mode_switch_interval_s": 30
  }
}
```

## Data Flow — Gossip Heartbeat (Phase A)

```
┌─────────────────────┐         UDP Multicast         ┌─────────────────────┐
│   Peer A (HTTP +   │  ▸ heartbeat every 250ms      │   Peer B (HTTP +   │
│    P2P Swarm)       │                               │    P2P Swarm)       │
│                     │  ◂ update/announce resp       │                     │
│  Peliculas struct   │    index hashes               │  Peliculas struct   │
│  ├─ RWMutex (A) ───┤                               │  ├─ RWMutex (B) ───┤
│  └─ Chunk Cache ◄──┼────────── peer list fanout ──►└─ Chunk Cache ──────┘
│       ▲             │      + chunk map               │         ▲           │
│       │ update      │                              sync   │ update        │
│   P2P goroutine     │                                P2P goroutine        │
└─────────────────────┘                               └─────────────────────┘
```

### Video Chunk Handoff (WebRTC / MSE)

```
Peer A (hosting video local)                Peer B (browser viewer)
       │                                          │
  Read MP4 segments from disk                    │
  Fragment into ≤256KB chunks                     │
       │                                          │
  ── RTCDataChannel (ordered, buffered) ─────────►│
       │                            [ChunkIdx=0..N] │
                                 MediaSource Extensions
                                 appends frames in order
                                 video element plays live
```

### NAT Traversal — Hole Punch Flow (Phase B)

```
Client A                          STUN Server                        Client B
   │                                │                                  │
   │  GET external IP/port          │                                  │
   ├───────────────────────────────►│                                  │
   │                                │                                  │
   │  EXTERNAL:1234                 │                                  │
   │◄───────────────────────────────┤                                  │
   │                                │    announce to swarm via        │
   │                                │    gossip heartbeat             │
   │                                │                                  │
   │         ── simultaneous UDP probes (500ms timeout) ─────────────►│
   │◄────── simultaneous UDP probes ─────────────────────────────────┤
   │                                │                                  │
   │  Hole punched! Direct TCP      │    Direct TCP connection       ┌──┘
   └════════════════════════════════╧═════════════════════════════════►│  
                                                                    Fallback: Seed Relay
```

## Concurrency & Deadlock Prevention

### Mutex Locking Order (Enforced by convention + MutexWatchdog)

1. `peliculasMutex` — guards the full `Peliculas` fetch from pastebin (rare, set-and-forget, ~once/hour refetch)
2. `swarmPeerStoreMutex` — guards peer add/remove/retire in SwarmCoordinator's PeerStore slice
3. `chunkCacheMutex` — guards the per-peer chunk cache map

**Rule:** Never hold #1 while acquiring #2 or #3. If a refactor needs both, drop #1 → acquire others → reacquire #1 (pattern: "publish snapshot" to a temp struct, release lock, process off-lock).

### Goroutine Lifecycle via `context.Context`

Every P2P goroutine receives `ctx, cancel := context.WithCancel(context.Background())` from the SwarmCoordinator. On `Coordinator.Stop()`, all goroutines check `ctx.Done()` on their event loop select statements.

```
SwarmCoordinator.Stop()
  → peerRetireTimerCtx cancel           ← goroutine exits UDP multicast loop
  → chunkRelayCtx cancel                ← goroutine exits TCP relay loop  
  → swarmStateCtx cancel                ← goroutine exits rareness-eval loop
  → peerSyncWG.Done()                   ← Wait() unblocks in <50ms
```

### CPU / Memory Footprint Estimates (per node, typical deployment)

| Component | CPU Core % (idle) | CPU MHz (peak sync) | RAM (baseline) | RAM (+10 peers) |
|-----------|-------------------|---------------------|----------------|-----------------|
| HTTP server (stdlib) | ~2% | ~50 MHz | ~8 MB | — |
| UDP multicast heartbeat | <0.5% | ~3 MHz | 400 KB | +1 KB/peer gossip table |
| TCP peer connections | 0% idle / 5-15% active | ~20 MHz/conn (max) | 2 MB baseline | +5 MB conn buffer each |
| Chunk cache | <1% | — | 2 MB | +8 MB (+10 video refs, 4MB each) |
| **Total P2P engine** | **<3%** | **~30 MHz** | **~6.5 MB** | **+15 MB (10 peers)** |
| WebRTC bridge (Phase B) | — | ~15 MHz/conn (+MSE reassembly) | +4 MB/conn (+ICE state) | — |

## Risk Assessment + Mitigation Strategies

### Risk 1: STUN/Hole-Punch Fails Behind Symmetric NAT (Highest Severity)
**Mitigation:** Seed relay is the hard fallback. Any peer behind symmetric NAT connects outbound to a seed, and the seed acts as a TCP/UDP bridge between peers. Seed nodes have higher bandwidth requirements but are minimal in number (2-3 globally).

### Risk 2: WebRTC ICE Connectivity Timeout (Medium Severity)
**Mitigation:** Three-tier candidate type priority: host → srflx (STUN) → relay (TURN). WebRTC library defaults to this ordering. If all candidates fail after 15s, the peer falls back entirely to TCP seed relay (chunk bytes over stdlib TCP, bypassing ICE).

### Risk 3: DataChannel Fragmentation in Browser | Medium Severity
**Mitigation:** Set `RTCDataChannel.bufferedAmountLowThreshold` and monitor via `OnBufferedAmountLow` callback. Backpressure is applied by pausing the upload goroutine on channel send until buffered amount drops below threshold. Maximum frame size = 256KB guarantees no fragmentation at MTU level.

### Risk 4: Swarm Storm / Gossip Amplification | Medium Severity
**Mitigation:** Each peer only forwards a heartbeat at most once per interval to any given neighbor (tracked via `visitedSet[string]struct{}`). TTL=4 multicast hop limit. If a peer sees >15 unique peers, prune the oldest 50%.

### Risk 5: Single-file P2P Logic Bloating main.go | Low Severity
**Mitigation:** This design explicitly moves all P2P code out of `main.go` into `internal/p2p/`. The root `go.mod` module is renamed to `aantvs`. HTTP handler logic in the original `main.go` stays as the "frontend" of the binary; P2P becomes its replication engine.

### Risk 6: Browser MSE Fragment Reassembly Failures | Low Severity
**Mitigation:** Client-side MSE append loop wraps each segment in a try-catch. Drops corrupted frames and requests them again from another peer within 500ms. Frame order is always deterministic (sequence number), so the browser never hangs waiting on an out-of-order gap.

## Implementation Order Recommendation

### Phase A — Core Swarm (No New Dependencies)

| # | Task | Files | Estimated Lines |
|---|------|-------|-----------------|
| 1 | Move `main.go` entry point to `cmd/aantvs/`, leave domain types in root | `main.go` → `cmd/aantvs/main.go` | ~20 (move + refactor imports) |
| 2 | Add `config.json` loader (`~/.aantvs/config.json`) | `internal/p2p/config.go` | ~80 |
| 3 | P2P protocol types + marshaling | `internal/p2p/protocol.go` | ~120 |
| 4 | SwarmCoordinator struct + lifecycle (context, channels, mutex) | `internal/p2p/swarm.go` | ~200 |
| 5 | UDP multicast heartbeat listener | `internal/p2p/discovery.go` | ~100 |
| 6 | TCP peer handshake + announce protocol | `internal/p2p/discovery.go` (cont.) | ~80 |
| 7 | Gap-fill scheduler (Rarest First) | `internal/p2p/swarm.go` (extension) | ~60 |

**Phase A subtotal:** 7 tasks, ~660 lines across 3 new files. **Zero external deps.** Compiles with current go.sum.

### Phase B — WebRTC + NAT Traversal (Adds pion dependencies)

| # | Task | Files | Estimated Lines |
|---|------|-------|-----------------|
| 1 | Add `pion/webrtc/v3` + `pion/stun/v2` to go.mod (Phase B only commit) | `go.mod`, `go.sum` | — |
| 2 | STUN client + ICE candidate collection | `internal/p2p/nat.go` | ~90 |
| 3 | Hole-punch manager with UDP probe timeout | `internal/p2p/nat.go` (extension) | ~80 |
| 4 | WebRTC peer management + dataChannel setup | `internal/p2p/webrtc_bridge.go` | ~150 |
| 5 | MSE fragment streamer (chunk framing, base64 encode/decode overhead in Go → JSON over WS/BinaryFrame) | `internal/p2p/webrtc_bridge.go` (extension) | ~70 |
| 6 | Rate limiter + backpressure for WebRTC peers | `internal/p2p/webrtc_bridge.go` (extension) | ~40 |

**Phase B subtotal:** 6 tasks, ~430 lines across 2 new files. **+2 deps.**

### Phase C — Seed Node & Relay (Minimal, Optional for Dev)

| # | Task | Files | Lines |
|---|------|-------|-------|
| 1 | Seed relayer TCP conn multiplexer | `internal/p2p/relay.go` | ~80 |
| 2 | Config example + documentation | `cmd/aantvs/config.json.example` | ~30 |

## Open Questions

- [ ] Pastebin fetch refetch frequency when P2P peers exist — should we still poll every `hora()` interval or trust peer gossip for new stations? If peer-based, how do we bootstrap from cold start with zero peers?
- [ ] Should chunk size (256KB default) be configurable per-file based on video bitrate vs fixed 8-second segments?
- [ ] TLS termination: the current server uses `ListenAndServe` (plain HTTP). Does P2P also need mTLS between peers, or is this an internal LAN deployment only?
