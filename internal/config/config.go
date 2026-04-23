package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Version int           `yaml:"-"`
	Local   LocalConfig   `yaml:"local"`
	Remote  RemoteConfig  `yaml:"remote"`
	Routing RoutingConfig `yaml:"routing"`
}

type LocalConfig struct {
	Nodes []NodeConfig `yaml:"nodes"`
}

type RemoteConfig struct {
	Providers []ProviderConfig `yaml:"providers"`
}

type NodeConfig struct {
	ID        string `yaml:"id"`
	Type      string `yaml:"type"`
	Endpoint  string `yaml:"endpoint"`
	APIKey    string `yaml:"api_key"`
	TimeoutMs int    `yaml:"timeout_ms"`
}

type ProviderConfig struct {
	ID       string `yaml:"id"`
	Type     string `yaml:"type"`
	Endpoint string `yaml:"endpoint"`
	APIKey   string `yaml:"api_key"`
}

type RoutingConfig struct {
	LatencyThresholdMs int  `yaml:"latency_threshold_ms"`
	FallbackEnabled    bool `yaml:"fallback_enabled"`
}

var validNodeTypes = map[string]bool{
	"ollama": true, "openai-compatible": true,
}
var validProviderTypes = map[string]bool{
	"openai-compatible": true, "anthropic": true, "google": true, "cohere": true,
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	expandEnv(&cfg)
	if err := validate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func expandEnv(cfg *Config) {
	for i := range cfg.Local.Nodes {
		cfg.Local.Nodes[i].APIKey = expand(cfg.Local.Nodes[i].APIKey)
		cfg.Local.Nodes[i].Endpoint = expand(cfg.Local.Nodes[i].Endpoint)
	}
	for i := range cfg.Remote.Providers {
		cfg.Remote.Providers[i].APIKey = expand(cfg.Remote.Providers[i].APIKey)
		cfg.Remote.Providers[i].Endpoint = expand(cfg.Remote.Providers[i].Endpoint)
	}
}

func expand(s string) string {
	if !strings.Contains(s, "${") {
		return s
	}
	return os.ExpandEnv(s)
}

func validate(cfg *Config) error {
	ids := make(map[string]bool)
	for _, n := range cfg.Local.Nodes {
		if n.ID == "" {
			return fmt.Errorf("node missing id")
		}
		if ids[n.ID] {
			return fmt.Errorf("duplicate id: %s", n.ID)
		}
		ids[n.ID] = true
		if !validNodeTypes[n.Type] {
			return fmt.Errorf("unknown node type %q for %s", n.Type, n.ID)
		}
		if n.Endpoint == "" {
			return fmt.Errorf("node %s missing endpoint", n.ID)
		}
	}
	for _, p := range cfg.Remote.Providers {
		if p.ID == "" {
			return fmt.Errorf("provider missing id")
		}
		if ids[p.ID] {
			return fmt.Errorf("duplicate id: %s", p.ID)
		}
		ids[p.ID] = true
		if !validProviderTypes[p.Type] {
			return fmt.Errorf("unknown provider type %q for %s", p.Type, p.ID)
		}
	}
	return nil
}

func (n NodeConfig) Redacted() NodeConfig {
	if n.APIKey != "" {
		n.APIKey = "[REDACTED]"
	}
	return n
}

func (p ProviderConfig) Redacted() ProviderConfig {
	if p.APIKey != "" {
		p.APIKey = "[REDACTED]"
	}
	return p
}
