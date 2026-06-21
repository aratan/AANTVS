package p2p

import (
	"log"
	"sync"
	"time"
)

// PeerReputation tracks the reputation score of a peer.
type PeerReputation struct {
	PeerID          string
	Score           float64 // -100 to 100, starts at 50
	TotalChunks     int
	BadChunks       int
	AvgLatencyMs    float64
	LastSeen        time.Time
	DecayFactor     float64 // multiplied on each heartbeat
}

// ReputationManager manages peer reputation scores across the swarm.
type ReputationManager struct {
	mu      sync.RWMutex
	peers   map[string]*PeerReputation
	// Thresholds
	goodThreshold   float64 // above this = trusted
	badThreshold    float64 // below this = degraded
	banThreshold    float64 // below this = disconnected
}

// NewReputationManager creates a reputation manager with default thresholds.
func NewReputationManager() *ReputationManager {
	return &ReputationManager{
		peers:         make(map[string]*PeerReputation),
		goodThreshold: 60,
		badThreshold:  30,
		banThreshold:  10,
	}
}

// RecordGoodChunk records a successful chunk delivery from a peer.
func (rm *ReputationManager) RecordGoodChunk(peerID string, latencyMs float64) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	pr := rm.getOrCreate(peerID)
	pr.TotalChunks++
	pr.LastSeen = time.Now()

	// Reward: +2 points for good chunk, bonus for low latency
	pr.Score += 2
	if latencyMs < 100 {
		pr.Score += 1 // bonus for fast delivery
	}
	if pr.Score > 100 {
		pr.Score = 100
	}

	// Update rolling average latency
	if pr.AvgLatencyMs == 0 {
		pr.AvgLatencyMs = latencyMs
	} else {
		pr.AvgLatencyMs = (pr.AvgLatencyMs*0.8 + latencyMs*0.2)
	}
}

// RecordBadChunk records a corrupted or invalid chunk from a peer.
func (rm *ReputationManager) RecordBadChunk(peerID string, reason string) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	pr := rm.getOrCreate(peerID)
	pr.TotalChunks++
	pr.BadChunks++
	pr.LastSeen = time.Now()

	// Penalty: -10 points for bad chunk
	pr.Score -= 10
	if pr.Score < -100 {
		pr.Score = -100
	}

	log.Printf("p2p: reputation — peer %s bad chunk (%s), score=%.1f, bad/total=%d/%d",
		peerID, reason, pr.Score, pr.BadChunks, pr.TotalChunks)
}

// RecordTimeout records a timeout from a peer.
func (rm *ReputationManager) RecordTimeout(peerID string) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	pr := rm.getOrCreate(peerID)
	pr.Score -= 5
	if pr.Score < -100 {
		pr.Score = -100
	}
}

// DecayScores applies time-based decay to all peer scores.
// Should be called periodically (e.g., every 60 seconds).
func (rm *ReputationManager) DecayScores() {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	now := time.Now()
	for _, pr := range rm.peers {
		// Decay by 1% per minute since last seen
		minutesSince := now.Sub(pr.LastSeen).Minutes()
		if minutesSince > 1 {
			pr.Score *= 0.99
		}
	}
}

// ShouldBan returns true if the peer should be disconnected.
func (rm *ReputationManager) ShouldBan(peerID string) bool {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	pr, exists := rm.peers[peerID]
	if !exists {
		return false
	}
	return pr.Score < rm.banThreshold
}

// IsTrusted returns true if the peer has a good reputation.
func (rm *ReputationManager) IsTrusted(peerID string) bool {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	pr, exists := rm.peers[peerID]
	if !exists {
		return false
	}
	return pr.Score >= rm.goodThreshold
}

// IsDegraded returns true if the peer has a bad reputation.
func (rm *ReputationManager) IsDegraded(peerID string) bool {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	pr, exists := rm.peers[peerID]
	if !exists {
		return false
	}
	return pr.Score < rm.badThreshold
}

// GetScore returns the reputation score for a peer.
func (rm *ReputationManager) GetScore(peerID string) float64 {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	pr, exists := rm.peers[peerID]
	if !exists {
		return 50 // default score
	}
	return pr.Score
}

// RemovePeer removes a peer from reputation tracking.
func (rm *ReputationManager) RemovePeer(peerID string) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	delete(rm.peers, peerID)
}

// GetBadPeers returns peers with score below bad threshold.
func (rm *ReputationManager) GetBadPeers() []string {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	var bad []string
	for id, pr := range rm.peers {
		if pr.Score < rm.badThreshold {
			bad = append(bad, id)
		}
	}
	return bad
}

// PeerStats returns reputation stats for all peers.
type PeerReputationStats struct {
	PeerID      string  `json:"peer_id"`
	Score       float64 `json:"score"`
	TotalChunks int     `json:"total_chunks"`
	BadChunks   int     `json:"bad_chunks"`
	AvgLatency  float64 `json:"avg_latency_ms"`
	Status      string  `json:"status"`
}

// GetStats returns stats for all tracked peers.
func (rm *ReputationManager) GetStats() []PeerReputationStats {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	stats := make([]PeerReputationStats, 0, len(rm.peers))
	for _, pr := range rm.peers {
		status := "good"
		if pr.Score < rm.banThreshold {
			status = "banned"
		} else if pr.Score < rm.badThreshold {
			status = "degraded"
		} else if pr.Score >= rm.goodThreshold {
			status = "trusted"
		}

		stats = append(stats, PeerReputationStats{
			PeerID:      pr.PeerID,
			Score:       pr.Score,
			TotalChunks: pr.TotalChunks,
			BadChunks:   pr.BadChunks,
			AvgLatency:  pr.AvgLatencyMs,
			Status:      status,
		})
	}
	return stats
}

func (rm *ReputationManager) getOrCreate(peerID string) *PeerReputation {
	pr, exists := rm.peers[peerID]
	if !exists {
		pr = &PeerReputation{
			PeerID:      peerID,
			Score:       50, // start neutral
			DecayFactor: 0.99,
			LastSeen:    time.Now(),
		}
		rm.peers[peerID] = pr
	}
	return pr
}
