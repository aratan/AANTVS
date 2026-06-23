package p2p

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config holds P2P and HTTP server configuration loaded from ~/.aantvs/config.json
type Config struct {
	HTTP        HTTPConfig    `json:"http"`
	P2P         P2PConfig     `json:"p2p"`
	P2PPort     int           `json:"p2p_port"`
	SeedPeers   []string      `json:"seed_peers"`   // libp2p multiaddrs
	StunServers []string      `json:"stun_servers"`
	McastAddr   string        `json:"mcast_addr"`   // deprecated: kept for config compatibility
}

// HTTPConfig holds the HTTP server port configuration.
type HTTPConfig struct {
	Port int `json:"port"`
}

// P2PConfig holds additional P2P overlay settings.
type P2PConfig struct {
	Enabled           bool     `json:"enabled"`
	DiscoveryMode     string   `json:"discovery_mode"`     // "mdns", "dht", "both"
	ListenAddr        string   `json:"listen_addr"`        // e.g., "/ip4/0.0.0.0/tcp/0"
	BootstrapPeers    []string `json:"bootstrap_peers"`     // DHT bootstrap peers
	// Legacy fields kept for config compatibility
	MulticastGroup    string `json:"multicast_group,omitempty"`
	MulticastPort     int    `json:"multicast_port,omitempty"`
	HeartbeatInterval int    `json:"heartbeat_interval_ms,omitempty"`
	TTL               int    `json:"ttl,omitempty"`
}

// DefaultConfig returns Config with sensible built-in defaults.
func DefaultConfig() Config {
	return Config{
		HTTP: HTTPConfig{
			Port: 80,
		},
		P2P: P2PConfig{
			Enabled:           true,
			DiscoveryMode:     "mdns",
			ListenAddr:        "/ip4/0.0.0.0/tcp/0",
			MulticastGroup:    "239.0.0.1",
			MulticastPort:     5432,
			HeartbeatInterval: 250,
			TTL:               4,
		},
		P2PPort:     8080,
		SeedPeers:   []string{},
		StunServers: []string{},
	}
}

// LoadConfig reads ~/.aantvs/config.json and merges it over default values.
// If the config file does not exist or cannot be parsed it falls back to defaults.
func LoadConfig() (Config, error) {
	return LoadConfigFrom("")
}

// LoadConfigFrom reads a specific config file path and merges it over defaults.
// If path is empty, falls back to ~/.aantvs/config.json.
func LoadConfigFrom(path string) (Config, error) {
	cfg := DefaultConfig()

	cfgPath := path
	if cfgPath == "" {
		var err error
		cfgPath, err = configPath()
		if err != nil {
			return cfg, nil
		}
	}

	data, err := os.ReadFile(cfgPath)
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
	cfg.P2P.Enabled = fileCfg.P2P.Enabled
	if fileCfg.P2P.DiscoveryMode != "" {
		cfg.P2P.DiscoveryMode = fileCfg.P2P.DiscoveryMode
	}
	if fileCfg.P2P.ListenAddr != "" {
		cfg.P2P.ListenAddr = fileCfg.P2P.ListenAddr
	}
	if len(fileCfg.P2P.BootstrapPeers) > 0 {
		cfg.P2P.BootstrapPeers = fileCfg.P2P.BootstrapPeers
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
