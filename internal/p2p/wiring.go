package p2p

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// StartP2P loads config, creates a Swarm, and starts gossip if enabled.
// Returns a shutdown function that should be called on process exit.
// If P2P is disabled in config, returns a no-op shutdown and nil error.
func StartP2P() (shutdown func(), err error) {
	// No-op shutdown for when P2P is disabled
	noOp := func() {}

	cfg, err := LoadConfig()
	if err != nil {
		log.Printf("p2p: config load failed, using defaults: %v", err)
	}

	if !cfg.P2P.Enabled {
		log.Println("p2p: disabled in config (p2p.enabled=false)")
		return noOp, nil
	}

	swarm, err := NewSwarm(cfg)
	if err != nil {
		return noOp, fmt.Errorf("p2p: create swarm: %w", err)
	}

	if err := swarm.Start(); err != nil {
		return noOp, fmt.Errorf("p2p: start swarm: %w", err)
	}

	log.Printf("p2p: swarm started (mcast=%s, peers=%d)", cfg.McastAddr, len(cfg.SeedPeers))

	// Return a shutdown function that stops the swarm gracefully
	ctx, cancel := context.WithCancel(context.Background())
	_ = ctx // used for future peer lifecycle management

	return func() {
		log.Println("p2p: shutting down swarm...")
		cancel()
		swarm.Stop()
	}, nil
}

// WaitForShutdown blocks until SIGINT/SIGTERM is received, then returns.
// Useful for main() to block until the user kills the process.
func WaitForShutdown() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("received signal %v, shutting down...", sig)
}

// SleepWithContext respects context cancellation — useful for retry loops.
func SleepWithContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
