package p2p

import (
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// SwarmMode represents the current swarm operating mode.
type SwarmMode int8

const (
	// RarestFirstMode — default: fill rarest gaps during idle sync
	RarestFirstMode SwarmMode = iota
	// SequentialFirstMode — user pressing Play drives sequential priority upload
	SequentialFirstMode
)

// String returns a human-readable name for the mode.
func (m SwarmMode) String() string {
	switch m {
	case RarestFirstMode:
		return "RAREST_FIRST"
	case SequentialFirstMode:
		return "SEQUENTIAL_FIRST"
	default:
		return "UNKNOWN"
	}
}

// EquilibriumManager monitors peer health, load, and active playbacks;
// switches between RarestFirstMode (archival) and SequentialFirstMode (playback).
type EquilibriumManager struct {
	mu                 sync.Mutex
	state              SwarmMode
	modeSwitchInterval time.Duration
	nextSwitchAt       time.Time
	activePlaybacks    int32 // atomic — count of videos being watched
	peerHealthScore    map[string]int // peerID -> health score (1=healthy, 0=degraded, -1=dead)
	loadedThreshold    float64       // avg health below this → force RarestFirst
}

// NewEquilibriumManager creates a manager with default settings.
func NewEquilibriumManager() *EquilibriumManager {
	return &EquilibriumManager{
		state:              RarestFirstMode,
		modeSwitchInterval: 30 * time.Second,
		nextSwitchAt:       time.Now().Add(30 * time.Second),
		peerHealthScore:    make(map[string]int),
		loadedThreshold:    0.5,
	}
}

// OnPlaybackStart is called when a video starts playing.
// Returns the new mode and whether a transition occurred.
func (em *EquilibriumManager) OnPlaybackStart() SwarmMode {
	em.mu.Lock()
	defer em.mu.Unlock()

	atomic.AddInt32(&em.activePlaybacks, 1)

	if em.state == RarestFirstMode {
		em.state = SequentialFirstMode
		em.nextSwitchAt = time.Now().Add(em.modeSwitchInterval)
		log.Printf("p2p: equilibrium → %s (playback started)", em.state)
	}

	return em.state
}

// OnPlaybackStop is called when a video stops playing.
// Returns the new mode.
func (em *EquilibriumManager) OnPlaybackStop() SwarmMode {
	em.mu.Lock()
	defer em.mu.Unlock()

	current := atomic.AddInt32(&em.activePlaybacks, -1)
	if current < 0 {
		atomic.StoreInt32(&em.activePlaybacks, 0)
	}

	// Don't switch immediately — let modeSwitchInterval elapse
	// The CheckModeTransition method handles the actual switch
	return em.state
}

// CheckModeTransition checks if it's time to switch modes.
// Should be called periodically (e.g., every second).
// Returns the current mode after any transition.
func (em *EquilibriumManager) CheckModeTransition() SwarmMode {
	em.mu.Lock()
	defer em.mu.Unlock()

	// If we're in SequentialFirst and the timer expired, switch back
	if em.state == SequentialFirstMode && time.Now().After(em.nextSwitchAt) {
		if atomic.LoadInt32(&em.activePlaybacks) == 0 {
			em.state = RarestFirstMode
			log.Printf("p2p: equilibrium → %s (no active playbacks, timer expired)", em.state)
		}
	}

	// Force RarestFirst if peer health is too low
	if avg := em.averageHealthUnlocked(); avg < em.loadedThreshold && avg > 0 {
		if em.state == SequentialFirstMode {
			em.state = RarestFirstMode
			log.Printf("p2p: equilibrium → %s (low health: %.2f)", em.state, avg)
		}
	}

	return em.state
}

// UpdatePeerHealth records a peer's health based on heartbeat latency.
func (em *EquilibriumManager) UpdatePeerHealth(peerID string, heartbeatLatency time.Duration) {
	em.mu.Lock()
	defer em.mu.Unlock()

	// Score: <100ms = healthy (1), <500ms = degraded (0), >500ms = dead (-1)
	switch {
	case heartbeatLatency < 100*time.Millisecond:
		em.peerHealthScore[peerID] = 1
	case heartbeatLatency < 500*time.Millisecond:
		em.peerHealthScore[peerID] = 0
	default:
		em.peerHealthScore[peerID] = -1
	}
}

// RemovePeer removes a peer from health tracking.
func (em *EquilibriumManager) RemovePeer(peerID string) {
	em.mu.Lock()
	defer em.mu.Unlock()
	delete(em.peerHealthScore, peerID)
}

// AverageHealth returns the average health score across all tracked peers.
// Returns 0 if no peers are tracked.
func (em *EquilibriumManager) AverageHealth() float64 {
	em.mu.Lock()
	defer em.mu.Unlock()
	return em.averageHealthUnlocked()
}

// averageHealthUnlocked returns the average health score without acquiring the lock.
// Caller MUST hold em.mu.
func (em *EquilibriumManager) averageHealthUnlocked() float64 {
	if len(em.peerHealthScore) == 0 {
		return 0
	}

	total := 0
	for _, score := range em.peerHealthScore {
		total += score
	}
	return float64(total) / float64(len(em.peerHealthScore))
}

// CurrentMode returns the current swarm mode without acquiring locks.
func (em *EquilibriumManager) CurrentMode() SwarmMode {
	em.mu.Lock()
	defer em.mu.Unlock()
	return em.state
}

// ActivePlaybacks returns the count of active playbacks.
func (em *EquilibriumManager) ActivePlaybacks() int32 {
	return atomic.LoadInt32(&em.activePlaybacks)
}

// IsSequential returns true if the swarm is in sequential-first mode.
func (em *EquilibriumManager) IsSequential() bool {
	return em.CurrentMode() == SequentialFirstMode
}

// ShouldPrioritizeChunk returns true if the given chunk should get
// priority bandwidth allocation based on the current mode.
func (em *EquilibriumManager) ShouldPrioritizeChunk(isSequential bool) bool {
	if em.IsSequential() {
		return isSequential // sequential chunks get priority in SEQ_FIRST mode
	}
	return false // no priority in RAREST_FIRST mode
}

// Stats returns a snapshot of the equilibrium manager state.
type EquilibriumStats struct {
	Mode            string  `json:"mode"`
	ActivePlaybacks int32   `json:"active_playbacks"`
	AvgHealth       float64 `json:"avg_health"`
	PeerCount       int     `json:"peer_count"`
	NextSwitchAt    string  `json:"next_switch_at"`
}

// GetStats returns current stats for monitoring/logging.
func (em *EquilibriumManager) GetStats() EquilibriumStats {
	em.mu.Lock()
	mode := em.state.String()
	playbacks := atomic.LoadInt32(&em.activePlaybacks)
	avg := em.averageHealthUnlocked()
	peers := len(em.peerHealthScore)
	next := em.nextSwitchAt.Format(time.RFC3339)
	em.mu.Unlock()

	return EquilibriumStats{
		Mode:            mode,
		ActivePlaybacks: playbacks,
		AvgHealth:       avg,
		PeerCount:       peers,
		NextSwitchAt:    next,
	}
}
