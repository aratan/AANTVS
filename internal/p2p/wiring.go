package p2p

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// StartP2P loads config, creates a Swarm, and starts gossip if enabled.
// Returns a shutdown function that should be called on process exit,
// and a reference to the swarm for handler access.
// If P2P is disabled in config, returns a no-op shutdown, nil swarm, and nil error.
func StartP2P() (shutdown func(), swarm *Swarm, err error) {
	// No-op shutdown for when P2P is disabled
	noOp := func() {}

	cfg, err := LoadConfig()
	if err != nil {
		log.Printf("p2p: config load failed, using defaults: %v", err)
	}

	if !cfg.P2P.Enabled {
		log.Println("p2p: disabled in config (p2p.enabled=false)")
		return noOp, nil, nil
	}

	swarm, err = NewSwarm(cfg)
	if err != nil {
		return noOp, nil, fmt.Errorf("p2p: create swarm: %w", err)
	}

	if err := swarm.Start(); err != nil {
		return noOp, nil, fmt.Errorf("p2p: start swarm: %w", err)
	}

	// Return a shutdown function that stops the swarm gracefully
	ctx, cancel := context.WithCancel(context.Background())
	_ = ctx // used for future peer lifecycle management

	return func() {
		log.Println("p2p: shutting down swarm...")
		cancel()
		swarm.Stop()
	}, swarm, nil
}

// Allowed extensions for inventory items
var inventoryExtAllowed = map[string]string{
	".mp4":  "video/mp4",
	".webm": "video/webm",
	".ogg":  "video/ogg",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".gif":  "image/gif",
	".webp": "image/webp",
	".csv":  "text/csv",
	".json": "application/json",
	".txt":  "text/plain",
	".pdf":  "application/pdf",
}

// buildLocalInventory scans the api/ directory and returns inventory items for local files.
func buildLocalInventory() []InventoryItem {
	items := make([]InventoryItem, 0)
	entries, err := os.ReadDir("api")
	if err != nil {
		return items
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if _, allowed := inventoryExtAllowed[ext]; !allowed {
			continue
		}
		items = append(items, InventoryItem{
			Name: name,
			Path: "/api/" + name,
			Size: info.Size(),
			Type: inventoryExtAllowed[ext],
		})
	}
	return items
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
