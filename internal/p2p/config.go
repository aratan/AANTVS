package p2p

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config holds P2P and HTTP server configuration loaded from ~/.aantvs/config.json
type Config struct {
	HTTP       HTTPConfig   `json:"http"`
	P2P        P2PConfig    `json:"p2p"`
	P2PPort    int          `json:"p2p_port"`
	McastAddr  string       `json:"mcast_addr"`
	SeedPeers  []string     `json:"seed_peers"`
	StunServers []string    `json:"stun_servers"`
}

// HTTPConfig holds the HTTP server port configuration.
type HTTPConfig struct {
	Port int `json:"port"`
}

// P2PConfig holds additional P2P overlay settings.
type P2PConfig struct {
	Enabled           bool `json:"enabled"`
	MulticastGroup    string `json:"multicast_group,omitempty"`
	MulticastPort     int    `json:"multicast_port,omitempty"`
	HeartbeatInterval int    `json:"heartbeat_interval_ms,omitempty"`
	TTL               int    `json:"ttl,omitempty"`
}

// DefaultConfig returns Config with sensible built-in defaults.
func defaultConfig() Config {
	return Config{
		HTTP: HTTPConfig{
			Port: 80,
		},
		P2P: P2PConfig{
			Enabled:           true,
			MulticastGroup:    "239.0.0.1",
			MulticastPort:     5432,
			HeartbeatInterval: 250,
			TTL:               4,
		},
		P2PPort:     8080,
		McastAddr:   "239.0.0.1:5432",
		SeedPeers:   []string{},
		StunServers: []string{},
	}
}

// LoadConfig reads ~/.aantvs/config.json and merges it over default values.
// If the config file does not exist or cannot be parsed it falls back to defaults.
func LoadConfig() (Config, error) {
	cfg := defaultConfig()

	configPath, err := configPath()
	if err != nil {
		return cfg, nil // fall through with defaults
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		// Config file absent or unreadable — use defaults.
		return cfg, nil
	}

	var fileCfg Config
	if err := json.Unmarshal(data, &fileCfg); err != nil {
		// Malformed JSON — use defaults.
		return cfg, nil
	}

	// Merge file values over defaults (non-zero fields win).
	if fileCfg.HTTP.Port != 0 {
		cfg.HTTP.Port = fileCfg.HTTP.Port
	}
	if fileCfg.P2P.Enabled {
		cfg.P2P.Enabled = fileCfg.P2P.Enabled
	}
	if fileCfg.P2P.MulticastGroup != "" {
		cfg.P2P.MulticastGroup = fileCfg.P2P.MulticastGroup
	}
	if fileCfg.P2P.MulticastPort != 0 {
		cfg.P2P.MulticastPort = fileCfg.P2P.MulticastPort
	}
	if fileCfg.P2P.HeartbeatInterval != 0 {
		cfg.P2P.HeartbeatInterval = fileCfg.P2P.HeartbeatInterval
	}
	if fileCfg.P2P.TTL != 0 {
		cfg.P2P.TTL = fileCfg.P2P.TTL
	}
	if fileCfg.P2PPort != 0 {
		cfg.P2PPort = fileCfg.P2PPort
	}
	if fileCfg.McastAddr != "" {
		cfg.McastAddr = fileCfg.McastAddr
	}
	if len(fileCfg.SeedPeers) > 0 {
		cfg.SeedPeers = fileCfg.SeedPeers
	}
	if len(fileCfg.StunServers) > 0 {
		cfg.StunServers = fileCfg.StunServers
	}

	return cfg, nil
}

// configPath returns the path to ~/.aantvs/config.json resolving across platforms.
func configPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".aantvs", "config.json"), nil
}
