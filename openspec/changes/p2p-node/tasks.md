# Tasks: P2P Node Integration — Swarm-First Replication Engine

## Review Workload Forecast

| Field | Value |
|-------|-------|
| Estimated changed lines | ~1,000 (additions + deletions across 8 files) |
| 400-line budget risk | High (~2.5× budget) |
| Chained PRs recommended | Yes |
| Suggested split | PR 1 → PR 2 → PR 3 |
| Delivery strategy | force-chained |
| Chain strategy | stacked-to-main |

**Decision needed before apply: No** (auto-chain accepted at session start)
**Chained PRs recommended: Yes**
**Chain strategy: stacked-to-main**
**400-line budget risk: High**

### Suggested Work Units

| Unit | Goal | Likely PR | Notes |
|------|------|-----------|-------|
| 1 | Phase A: stdlib-only gossip (config, peer, gossip, main wiring) — zero deps, independently deployable | PR `p2p/01-stdlib-gossip` | Base = main after xss-fix merges; ~450 lines |
| 2 | Phase B: pion/webrtc bridge + hole punching + rate limiting | PR `p2p/02-webrtc-bridge` | Depends on PR 1; new deps (pion/webrtc, pion/stun); ~370 lines |
| 3 | Phase C: seed command + equilibrium manager | PR `p2p/03-seed-equilibrium` | Depends on PR 1+2 for types; ~180 lines |

---

## Phase A — Stdlib-Only Gossip (Tasks A1–A4)

> **Goal:** Achieve a fully functional P2P swarm with UDP multicast discovery + TCP peer gossip using zero external dependencies. Prerequisite: XSS fix PRs must merge first for structural alignment on `MovieCard` + `pageData`.

### Task A1 — Create internal/p2p/config.go (JSON config loader)

**Goal:** Parse ~/.aantvs/config.json into a typed Config struct; defaults if file absent.
**Files:** `internal/p2p/config.go` (new)

```go
type Config struct {
    HTTPPort      int           `json:"http_port"`       // default 80
    P2PEnabled    bool          `json:"p2p_enabled"`     // default false
    McastGroup    string        `json:"multicast_group"` // default "172.16.0.0"
    MCastPort     int           `json:"multicast_port"`  // default 9302
    HeartbeatMs   int           `json:"heartbeat_interval_ms"` // default 250
    TTL           int           `json:"ttl"`             // default 4
    SeedPeers     []PeerAddr    `json:"seed_peers"`     // addr + role fields
    StunServers   []string      `json:"stun_servers"`   // e.g. "stun.l.google.com:19302"
    ChunkSizeBytes int          `json:"chunk_size_bytes"` // default 262144
}

type PeerAddr struct {
    Addr string `json:"addr"`
    Role string `json:"role"` // "seed" or "peer"
}

func LoadConfig(path string) (*Config, error)
func DefaultConfig() *Config
```

**Acceptance criteria:**
- [ ] `LoadConfig("/nonexistent")` returns Config with all defaults (no panic, no error on missing file when returning defaults)
- [ ] Valid JSON is parsed without loss of fields
- [ ] Invalid JSON returns descriptive error containing "invalid character" or "unmarshal"

**Estimated lines:** +80 / -0

### Task A2 — Create internal/p2p/peer.go (TCP peer connections with heartbeat + reconnect)

**Goal:** Mutex-protected PeerStore with exponential-backoff reconnect logic.
**Files:** `internal/p2p/peer.go` (new)

```go
type Peer struct {
    ID        string
    Addr      string // "host:port"
    Role      Role  // SeedPeer | RegularPeer
    Conn      net.Conn // nil when disconnected, guarded by peerMu
    LastSeen  time.Time
    IsActive  bool
}

type PeerStore struct {
    mu   sync.RWMutex
    peers []*Peer
}

const (
    reconnectTimeout     = 3 * time.Second
    minReconnectJitter   = 1 * time.Second
    maxReconnectJitter   = 30 * time.Second
)
```

**Key methods:**
- `Add(Conn, Role) *Peer` — inserts with mutex lock
- `Remove(id string)` — removes and closes Conn; guarded by mu
- `HeartbeatAll(ctx)` — goroutine: ticks every heartbeatInterval, marks peers stale after 3×heartbeat (750ms), retries dead connections with jittered exponential backoff
- `ReconnectLoop(peerID, initialDelay time.Duration) <-chan error` — reconnection channel; jitter = random(1s..30s). Uses tcpDialTimeout=3s per attempt.

**Acceptance criteria:**
- [ ] Two concurrent calls to Add() cannot corrupt the slice (RWMutex proven safe via `-race`)
- [ ] Heartbeat goroutine exits cleanly when context is cancelled
- [ ] Stale peer detection fires after exactly 3 missed heartbeats
- [ ] Jittered reconnect delays are non-deterministic (verified across 20 runs: ≥15 unique values)

**Estimated lines:** +150 / -0

### Task A3 — Create internal/p2p/gossip.go (UDP multicast discovery + P2PPacket dispatch)

**Goal:** Full gossip protocol implementation: packet types, marshaling, UDP multicast listener, TCP acceptor.
**Files:** `internal/p2p/gossip.go` (new)

**Packet types:**
```go
type PacketType uint8
const (
    IndexUpdate PacketType = iota + 1
    ChunkRequest
    ChunkResponse
    SwarmHeartbeat // renamed from Heartbeat to avoid confusion with Peer.heartbeat
)
```

**P2PPacket struct and payload types:**
```go
type P2PPacket struct {
    Version   int             `json:"v"`
    PeerID    string          `json:"pid"`
    MsgType   PacketType      `json:"t"`
    Timestamp int64           `json:"ts"`
    Payload   json.RawMessage `json:"p"`
}

type IndexPayload struct {
    PeerID        string   `json:"pid"`
    TotalStations int      `json:"ts"`
    StationHashes []string `json:"sh"`
    LastIndexAt   int64    `json:"lia"`
}

type ChunkRequestPayload struct {
    PeerID   string `json:"pid"`
    Index    int    `json:"i"`
    ChunkIdx int    `json:"ci"`
    TotalChks int   `json:"tc"`
}

type ChunkResponsePayload struct {
    PeerID  string `json:"pid"`
    Index   int    `json:"i"`
    ChunkIdx int   `json:"ci"`
    TotalChks int  `json:"tc"`
    StartTime float64 `json:"st"`
    EndTime   float64 `json:"et"`
    Data      []byte `json:"d"` // base64-encoded frame bytes, max 256KB
}

type SwarmHeartbeatPayload struct {
    PeerID            string  `json:"pid"`
    ActivePlaybacks   int32   `json:"ap"`
    Mode              SwarmMode `json:"m"`
    AvailablePeers     []string `json:"peers"`
}
```

**Key methods / types:**
- `type SwarmCoordinator struct` containing: peerStore, config, ctx/cancel, chunkCache (map[string]*chunkCacheEntry), modeSwitchInterval (30s)
- `NewSwarmCoordinator(cfg *config.Config) (*SwarmCoordinator, error)` — validates port, creates default config if nil
- `Start(ctx context.Context, peliculasRef *PeliculasHandler) error` — launches goroutines for: UDP multicast listener on MCastGroup:MCastPort with TTL=TTL; TCP acceptor loop; heartbeat fanout; rareness evaluator
- `handlePacket(peerID, rawJSON []byte) error` — type-switch on MsgType, dispatches to IndexUpdateHandler/ChunkRequestHandler/HeartbeatHandler

**Rarest First seeding mode:** Station hashes from all peers are collected. `StationRareness(h)` = count of peers holding hash h. `GossipTarget()` returns the station with max rareness (rarest = needs most replication). When activePeers < 3, falls back to sequential-first to guarantee quick coverage. Gap-fill scheduling runs every heartbeatInterval and picks the rarest station first.

**Acceptance criteria:**
- [ ] UDP multicast listener binds to MCastGroup:MCastPort without error
- [ ] Packet round-trip marshal → send → receive → unmarshal produces identical struct
- [ ] StationHashes use SHA-256 of `URL+Title` for rareness calculation (deterministic)
- [ ] Gossip fanout: a packet sent to one peer reaches all connected peers within 1 heartbeAt interval (250ms + network variance)
- [ ] Rarest-first: station with fewest peer copies is selected first when no active playback is happening

**Estimated lines:** +280 / -0

### Task A4 — main.go integration + cmd/aantvs entry point move

**Goal:** Launch P2P swarm alongside HTTP server; share `peliculas` mutex safely with documented lock ordering.
**Files:** `internal/p2p/swarm_wiring.go` (new), root `main.go` modified: imports, startup sequence

Wiring approach — do NOT merge all P2P code into main.go. Instead, the coordinator Start() is called from a new cmd entry point with goroutine management:

```go
// cmd/aantvs/main.go — thin bootstrap wrapper
func main() {
    // 1. Load config (before anything else)
    cfg, err := p2p.LoadConfig(filepath.Join(os.Getenv("HOME"), ".aantvs", "config.json"))
    // 2. Fetch initial peliculas from pastebin (unchanged logic from current main.go fetchJSON)
    // 3. Run HTTP server in goroutine with standard mux
    // 4. Run P2P coordinator alongside HTTP, sharing peliculas through a mutex-synchronized wrapper:
    //    lock order enforced by convention: httpHandlerReads → p2pWrites (never reverse)
}
```

**File changes for main.go:**
- Replace the current `fetchJSON()` call with a synchronized version using `sync.RWMutex` on peliculas writes
- Add P2P config check: only launch swarm when `config.p2p_enabled == true` and `len(config.SeedPeers) > 0` or peer is auto-discoverable
- Startup sequence: config → pastebin fetch → initialPageData build → HTTP goroutine → P2P startup → wait on shutdown signal

**Lock ordering rule:** HTTP handler (Read mutex before write mutex). Specifically: read `peliculas` under RLock during template render, write during pastebin refetch. The P2P coordinator writes through the same sync.RWMutex but always after dropping any http-side read lock. Document in code comment:

```go
// LOCK ORDERING (enforced by convention): 
//   1. peliculasMu (ReadLocker for template renders, WriteLocker for pastebin updates)
//   NEVER hold p2pPeerStoreMu while holding peliculasMu or vice versa.
//   If both needed: drop one → acquire other → release → reacquire pattern ("publish snapshot").
```

**Acceptance criteria:**
- [ ] `go build ./...` produces a binary with HTTP + P2P (config disabled by default so P2P is no-op without config)
- [ ] Binary runs without panic; HTTP serves correctly even when P2P has zero peers
- [ ] Config file missing → defaults apply, HTTP still works

**Estimated lines:** +60 / -30 (move + add goroutine startup logic to existing code)

---

## Phase B — WebRTC Bridge (Tasks B1–B3)

> **Added dependency:** `github.com/pion/webrtc/v4`, `github.com/pion/stun/v2`; run `go get` in this phase's commit.

### Task B1 — Create internal/webrtc/bridge.go (pion/webrtc setup + SDP exchange)

**Goal:** WebRTC peer lifecycle: offer/answer generation, DataChannel creation for JSON message passing, ICE candidate collection via STUN.
**Files:** `internal/webrtc/bridge.go` (new)

```go
type WebRTCPeer struct {
    ID string // unique per-browser-peer identity
    PeerConnection *webrtc.PeerConnection
    DataChannel    *webrtc.DataChannel
    RateLimiter    *rate.Limiter // 50KB/s per peer upload cap
    State          WebRTCState   // idle | connecting | connected | disconnecting (state machine via sync.State)
}

type WebRTCBridge struct {
    mu        sync.RWMutex
    peers     map[string]*WebRTCPeer
    maxPeers  int           // default 3 browser peers (conservative for initial deploy)
    stunServers []string   // from config.StunServers
    coordinator *p2p.SwarmCoordinator
}

func NewWebRTCBridge(cfg *config.Config, coordinator *p2p.SwarmCoordinator) *WebRTCBridge
func (wb *WebRTCBridge) CreateOffer() (webrtc.SessionDescription, error)
func (wb *WebRTCBridge) CreateAnswer(sd webrtc.SessionDescription) error
func (wb *WebRTCBridge) OpenDataChannel(label string) (*webrtc.DataChannel, error)
func (wb *WebRTCBridge) ClosePeer(id string) error
```

**Acceptance criteria:**
- [ ] `CreateOffer()` returns a valid SDP offer that can be round-tripped through the browser's RTCPeerConnection.createOffer() → RTCSessionDescription init → createAnswer()
- [ ] DataChannel is created with ordered=true, maxRetransmits=0 (reliable delivery for chunk metadata)
- [ ] `maxPeers` limits connection count; 4th peer gets HTTP error "connection limit reached"

**Estimated lines:** +190 / -0

### Task B2 — Create internal/p2p/holePunch.go (STUN client + NAT traversal)

**Goal:** Extract external IP:port via STUN, simultaneous hole-punch attempts to both peers, fallback to seed relay connection when punching fails.
**Files:** `internal/p2p/holePunch.go` (new)

```go
type HolePunchResult struct {
    DirectConn net.Conn // successful direct TCP connection
    ICECandidates []ice.Candidate
    Success bool
    FallbackRelayAddr string // empty if direct succeeded; peer's seed relay address otherwise
}

func QuerySTUN(server string) (net.IP, int, error)          // gets external IP:port
func HolePunch(localIP net.IP, localPort int, targetIP net.IP, targetPort int, timeout time.Duration) *HolePunchResult
```

**Hole punch logic:**
1. `QuerySTUN()` → stdlib UDP to STUN server every config.StunServers[i], parse binding response for external IP + mapped port
2. Both peers send their local ICE candidates through the gossip protocol so each knows the other's potential addresses
3. **Simultaneous UDP probe:** both peers open raw `net.DialUDP` simultaneously to each other's candidate pairs with 500ms timeout per probe. Most successful pair becomes DirectConn.
4. If all probes fail after 3 rounds → fallback seed relay: peer connects outbound to SeedPeer (TCP), which acts as transparent TCP/UDP bridge. Implemented by reusing the existing Peer.Store TCP infrastructure (Task A2) with a new `relayMode` flag in the peer's Conn lifecycle.

**Acceptance criteria:**
- [ ] QuerySTUN returns correct public IP when behind NAT router (verified with local STUN + public STUN pair)
- [ ] HolePunch succeeds for peers on same LAN or symmetric-NAT-compliant routers
- [ ] Fallback relay path activates within 3s after all probes exhausted
- [ ] No blocking in the main goroutine: all STUN queries are context-aware and cancellable

**Estimated lines:** +170 / -0

### Task B3 — WebRTC goroutine startup + rate limiting in main.go integration

**Goal:** Wire WebRTC bridge into main bootstrap; enforce 50KB/s upload per browser peer to protect infrastructure.
**Files:** Modify `cmd/aantvs/main.go` (add WebRTC bridge launch), `internal/webrtc/bridge.go` adds rate limiting extension

Rate limiter logic: each WebRTCPeer gets its own `rate.Limiter(50 * 1024)` and `rate.NewBucket(rate.Limit(50*1024), 50*1024)`. Every DataChannel send calls `rl.Wait(ctx)` before transmitting. Backpressure: when bufferedAmount exceeds `bufferedAmountLowThreshold`, pause upload goroutine until drained.

**Acceptance criteria:**
- [ ] WebRTC bridge starts (and gracefully no-ops during dev mode or config disabled) without affecting HTTP startup time
- [ ] Rate limiter enforces ≤50KB/s per peer within 5% tolerance when measured over 10s window
- [ ] Buffer overflow backpressure: after sending at max rate, subsequent sends block until buffer drains

**Estimated lines:** +40 / -0

---

## Phase C — Seed Node & Equilibrium (Tasks C1–C2)

### Task C1 — Create cmd/aantvs-seed/seed.go (seed/relay subcommand binary)

**Goal:** Build a separate `aantvs seed` subcommand from the same codebase. Implements stateless relay forwarding for failed hole punches + initial swarm bootstrap data.
**Files:** `cmd/aantvs-seed/seed.go` (new), `go.mod` unchanged (same module, different binary target)

```go
// cmd/aantvs-seed/seed.go — standalone binary built via: go build -o aantvs-seed ./cmd/aantvs-seed
func main() {
    // Flags: --port, --peers, --seed-relay-port
    // Listens on seed-relay-port for incoming fallback connections
    // Receives bootstrap data (station list hashes) via gossip from connected peers
    // Relays chunk payloads between two peers over TCP (transparent bridging)
    // Stateless: does NOT store chunks permanently — just pipes bytes between two peer connections
}
```

Stateless relay forwarding implementation pattern in seed.go:
```go
func relayConn(a, b net.Conn) {
    done := make(chan struct{})
    _, _ = io.Copy(a, b); close(done) // goroutine pair for bidirectional pipe
    _, _ = io.Copy(b, a); <-done      // second goroutine handles reverse direction
}
```

Seed provides `GetBootstrap() pelis.IndexPayload` which returns the full station hash list immediately on connection (cold-boot for new peers). Used during `SwarmCoordinator.Start()` to populate PeerStore if no multicast peers are found.

**Acceptance criteria:**
- [ ] `aantvs seed` binary starts, listens on specified port, and accepts TCP connections without panic
- [ ] relay forwarding is stateless: two connected peers (A→seed, B→seed) get full-duplex piped connectivity (bidirectional bytes flow transparently)
- [ ] Bootstrap data returns all StationHashes from config within 100ms regardless of connection count

**Estimated lines:** +100 / -0

### Task C2 — Create internal/p2p/equilibrium.go (state mode machine for playback-aware seeding)

**Goal:** EquilibriumManager that monitors peer health, load, and active playbacks; switches between RarestFirstMode (default/archival) and SequentialFirstMode (when Play is pressed).
**Files:** `internal/p2p/equilibrium.go` (new)

```go
type SwarmMode int8
const (
    RarestFirstMode SwarmMode = iota // default: fill rarest gaps during idle sync
    SequentialFirstMode               // user pressing Play drives sequential priority upload
)

type EquilibriumManager struct {
    mu                     sync.Mutex
    state                  SwarmMode
    modeSwitchInterval     time.Duration // default 30s
    nextSwitchAt           time.Time
    activePlaybacks        int32         // atomic.Int32 — count of videos actively being watched across swarm
    peerHealthScore       map[string]int   // healthy=1, degraded=0, dead=-1 (computed from heartbeat response rate)
    loadedThreshold       float64          // when all peers' health score average < threshold → force RarestFirst back
    
}

func (em *EquilibriumManager) OnPlaybackStart() SwarmMode
func (em *EquilibriumManager) OnPlaybackStop() SwarmMode
func (em equilibrium.EquilibriumManager). UpdatePeerHealth(peerID string, heartbeatLatency time.Duration)
```

**State machine transitions:**
- Default → RAREST_FIRST
- User presses Play → **SequentialFirst**, modeSwitchAt = now() + 30s (hold for 30s minimum before switching back)
- No active playback for 30s → **RarestFirstMode** again
- peerHealthScore average < 0.5 threshold → force switch to RarestFirst regardless of playback state (preservation mode)

SequentialFirst priority: during SequentialFirst, chunk upload bandwidth is diverted: 2x upload rate of idle gaps = sequential chunks get prioritized bandwidth allocation while rareness evaluation still runs in parallel but at half capacity.

**Acceptance criteria:**
- [ ] `OnPlaybackStart()` returns SequentialFirstMode and sets nextSwitchAt to 30s in the future without panic
- [ ] Mode holds for exactly `modeSwitchInterval` before switching back to RAREST_FIRST (verified with time-tracked test)
- [ ] Low-health peers don't participate in chunk routing (health score = 0 filters them from peer selection)
- [ ] Sequential chunks get bandwidth priority when mode is SequentialFirstMode (measured at the DataChannel level: sequential chunks transmitted before gap-fill chunks within same heartbeat window)

**Estimated lines:** +150 / -0

---

## Total Work Summary

| Phase | Tasks | Changed Lines | Deps Required |
|-------|-------|---------------|---------------|
| **PR 1 — Stdlib Gossip (A1-A4)** | A1, A2, A3, A4 | ~570 lines across 4 new files + main.go mod | None (stdlib only) |
| **PR 2 — WebRTC Bridge (B1-B3)** | B1, B2, B3 | ~400 lines across 3 new files + go.mod add | `pion/webrtc/v4`, `pion/stun/v2` |
| **PR 3 — Seed & Equilibrium (C1-C2)** | C1, C2 | ~250 lines across 2 new files | None |
| **Total** | 9 tasks | **~1,220 lines** across 8+ new files + 4 mod changes | Phase B adds 2 deps |

## Implementation Order and Rationale

1. **PR 1 first** — Self-contained: config → peer connections → gossip protocol → main wiring. Zero external dependencies; compiles with current go.sum; fully deployable slice for LAN testing. XSS fix PRs must merge first (MovieCard/pageData types are the replicated data shape).
2. **PR 2 second** — Depends on PR 1's SwarmCoordinator and PeerStore being wired. Adds pion deps but builds the browser-facing capability that makes P2P useful beyond LAN. Each Phase A type is already exercised by PR 1's relay tests.
3. **PR 3 third** — Seed command + equilibrium are polish features: seed bootstrap enables cold-start, equilibrium mode improves swarm health during active playback. These depend on both PR 1 and PR 2 being wired (seed relay uses Peer.Store TCP; equilibrium references SwarmMode from the gossip layer).

### PR Branch Names (stacked-to-main)
- `feature/aantvs-p2p-gossip` → main (PR 1, squashed merge)
- `feature/aantvs-p2p-webrtc` → `feature/aantvs-p2p-gossip` (PR 2)

## Files Created / Modified Summary

| File | Action | Phase | Lines Est. |
|------|--------|-------|-----------|
| internal/p2p/config.go | Create | A1 | ~80 |
| internal/p2p/peer.go | Create | A2 | ~150 |
| internal/p2p/gossip.go | Create | A3 | ~280 |
| internal/p2p/swarm_wiring.go | Create | A4 | ~60 |
| cmd/aantvs/main.go | Create (from main.go) + modify | A4 | +40/-30 |
| go.mod | Modify (module name, Go 1.22+, pion deps) | B1 | small |
| internal/webrtc/bridge.go | Create | B1-B2 | ~190 |
| internal/p2p/holePunch.go | Create | B2 | ~170 |
| cmd/aantvs-seed/seed.go | Create | C1 | ~100 |
| internal/p2p/equilibrium.go | Create | C2 | ~150 |

## Key Design Constraints (from the design doc)

- **Mutex lock order:** NEVER hold p2pPeerStoreMu while holding peliculasMu. If both needed: drop one → acquire other → release → reacquire pattern ("publish snapshot").
- **Phase A MUST compile standalone** — zero pion deps; all of Phase A should work fully without any WebRTC functionality in production.
- **Rate limiting:** Each browser peer capped at 50KB/s upload to protect infrastructure.
- **Data integrity:** Station hashes use SHA-256 of `URL+Title` for deterministic rareness calculation across peers.
- **Gossip fanout cap:** Peer list pruned if exceeds maxPeers (3 default); oldest 50% removed when at capacity.
