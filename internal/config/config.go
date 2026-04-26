package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level configuration structure.
// Supports two schemas:
//   - Version 2 (old): version: 2, routing.default_model, flat providers[] with models.
//   - New (no version): local.nodes + remote.providers, no models required.
type Config struct {
	Version   int              `yaml:"version"`
	Routing   RoutingConfig    `yaml:"routing"`
	Providers []ProviderConfig `yaml:"providers"` // old schema; populated by normaliseNewSchema for new schema
	Local     LocalConfig      `yaml:"local"`     // new schema
	Remote    RemoteConfig     `yaml:"remote"`    // new schema
}

// RoutingConfig holds global routing parameters.
type RoutingConfig struct {
	DefaultModel       string `yaml:"default_model"`
	LatencyThresholdMs int    `yaml:"latency_threshold_ms"`
	FallbackEnabled    bool   `yaml:"fallback_enabled"`
}

// LocalConfig contains local node definitions (new schema).
type LocalConfig struct {
	Nodes []LocalNodeConfig `yaml:"nodes"`
}

// LocalNodeConfig describes a locally-running LLM node (new schema).
type LocalNodeConfig struct {
	ID        string `yaml:"id"`
	Type      string `yaml:"type"`
	Endpoint  string `yaml:"endpoint"`
	TimeoutMs int    `yaml:"timeout_ms"`
	APIKey    string `yaml:"api_key"`
}

// RemoteConfig contains remote provider definitions (new schema).
type RemoteConfig struct {
	Providers []RemoteProviderConfig `yaml:"providers"`
}

// RemoteProviderConfig describes a remote LLM API provider (new schema).
type RemoteProviderConfig struct {
	ID        string `yaml:"id"`
	Type      string `yaml:"type"`
	Endpoint  string `yaml:"endpoint"`
	APIKey    string `yaml:"api_key"`
	TimeoutMs int    `yaml:"timeout_ms"`
}

// ProviderConfig describes a single LLM provider (used internally after normalisation).
type ProviderConfig struct {
	ID              string        `yaml:"id"`
	Type            string        `yaml:"type"`
	Endpoint        string        `yaml:"endpoint"`
	APIKey          string        `yaml:"api_key"`
	TimeoutMs       int           `yaml:"timeout_ms"`
	StreamTimeoutMs int           `yaml:"stream_timeout_ms"`
	RecoveryWindow  string        `yaml:"recovery_window"`
	Limits          *LimitsConfig `yaml:"limits"`
	Models          []ModelConfig `yaml:"models"`
	IsRemote        bool          `yaml:"-"` // true for remote providers (new schema)
	// Skipped is true when api_key was present in the YAML but resolved to empty
	// after environment variable expansion.
	Skipped bool `yaml:"-"`

	recoveryWindowDur time.Duration `yaml:"-"`
}

// StreamTimeoutMsOrDefault returns StreamTimeoutMs if set, else TimeoutMs.
func (p ProviderConfig) StreamTimeoutMsOrDefault() int {
	if p.StreamTimeoutMs > 0 {
		return p.StreamTimeoutMs
	}
	return p.TimeoutMs
}

// RecoveryWindowDur returns the parsed recovery window duration.
// Defaults to 1 hour if not configured.
func (p ProviderConfig) RecoveryWindowDur() time.Duration {
	if p.recoveryWindowDur == 0 {
		return time.Hour
	}
	return p.recoveryWindowDur
}

// Redacted returns a copy of the provider config with APIKey replaced by "[REDACTED]".
func (p ProviderConfig) Redacted() ProviderConfig {
	if p.APIKey != "" {
		p.APIKey = "[REDACTED]"
	}
	return p
}

// LimitsConfig defines rate-limit parameters for a provider.
type LimitsConfig struct {
	Requests  int    `yaml:"requests"`
	Window    string `yaml:"window"`
	windowDur time.Duration
}

// WindowDur returns the parsed window duration.
func (l *LimitsConfig) WindowDur() time.Duration {
	return l.windowDur
}

// ModelConfig describes a model offered by a provider.
type ModelConfig struct {
	ID          string   `yaml:"id"`
	Priority    int      `yaml:"priority"`
	IsFree      bool     `yaml:"is_free"`
	APIKey      string   `yaml:"api_key,omitempty"`
	Temperature *float64 `yaml:"temperature,omitempty"`
	TopP        *float64 `yaml:"top_p,omitempty"`
	MaxTokens   *int     `yaml:"max_tokens,omitempty"`
	Seed        *int     `yaml:"seed,omitempty"`
}

var validProviderTypes = map[string]bool{
	"ollama":            true,
	"openai-compatible": true,
	"anthropic":         true,
	"google":            true,
	"cohere":            true,
	"mistral":           true,
}

// endpointRequiredTypes lists provider types that must have a non-empty endpoint.
var endpointRequiredTypes = map[string]bool{
	"ollama":            true,
	"openai-compatible": true,
	"mistral":           true,
}

// Load reads, parses, validates, and returns the config at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	// Parse raw YAML before env expansion to capture original api_key values.
	rawAPIKeys, err := extractRawAPIKeys(data)
	if err != nil {
		return nil, fmt.Errorf("parse config (raw): %w", err)
	}

	// Expand environment variables in the raw bytes.
	expanded := []byte(os.ExpandEnv(string(data)))

	var cfg Config
	if err := yaml.Unmarshal(expanded, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if isNewSchema(&cfg) {
		if err := normaliseNewSchema(&cfg); err != nil {
			return nil, err
		}
		if err := validateNewSchema(&cfg); err != nil {
			return nil, err
		}
	} else {
		// Mark providers as skipped when api_key was present in YAML but expanded to empty.
		for i := range cfg.Providers {
			p := &cfg.Providers[i]
			if rawKey, had := rawAPIKeys[p.ID]; had && rawKey != "" && p.APIKey == "" {
				p.Skipped = true
			}
		}

		if err := parseDurations(&cfg); err != nil {
			return nil, err
		}

		if err := validate(&cfg); err != nil {
			return nil, err
		}
	}

	return &cfg, nil
}

// isNewSchema returns true when the config uses the local/remote section layout.
func isNewSchema(cfg *Config) bool {
	return len(cfg.Local.Nodes) > 0 || len(cfg.Remote.Providers) > 0
}

// normaliseNewSchema converts local/remote sections into the flat Providers slice.
func normaliseNewSchema(cfg *Config) error {
	priority := 1
	for _, n := range cfg.Local.Nodes {
		cfg.Providers = append(cfg.Providers, ProviderConfig{
			ID:        n.ID,
			Type:      n.Type,
			Endpoint:  n.Endpoint,
			TimeoutMs: n.TimeoutMs,
			APIKey:    n.APIKey,
			IsRemote:  false,
		})
		priority++
	}
	for _, r := range cfg.Remote.Providers {
		cfg.Providers = append(cfg.Providers, ProviderConfig{
			ID:        r.ID,
			Type:      r.Type,
			Endpoint:  r.Endpoint,
			TimeoutMs: r.TimeoutMs,
			APIKey:    r.APIKey,
			IsRemote:  true,
		})
		priority++
	}
	return nil
}

// validateNewSchema enforces rules for the new local/remote schema.
func validateNewSchema(cfg *Config) error {
	ids := make(map[string]bool)
	for _, p := range cfg.Providers {
		if p.ID == "" {
			return fmt.Errorf("provider missing id")
		}
		if ids[p.ID] {
			return fmt.Errorf("duplicate provider id: %s", p.ID)
		}
		ids[p.ID] = true
		if !validProviderTypes[p.Type] {
			return fmt.Errorf("provider %q: unknown type %q", p.ID, p.Type)
		}
		if endpointRequiredTypes[p.Type] && p.Endpoint == "" {
			return fmt.Errorf("provider %q (type %q): endpoint is required", p.ID, p.Type)
		}
	}
	return nil
}

// extractRawAPIKeys parses the YAML without env expansion and returns a map of
// provider id → raw api_key value (possibly containing ${VAR} references).
func extractRawAPIKeys(data []byte) (map[string]string, error) {
	type rawProvider struct {
		ID     string `yaml:"id"`
		APIKey string `yaml:"api_key"`
	}
	type rawRemoteProvider struct {
		ID     string `yaml:"id"`
		APIKey string `yaml:"api_key"`
	}
	type rawConfig struct {
		Providers []rawProvider `yaml:"providers"`
		Remote    struct {
			Providers []rawRemoteProvider `yaml:"providers"`
		} `yaml:"remote"`
	}

	var raw rawConfig
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	result := make(map[string]string)
	for _, p := range raw.Providers {
		result[p.ID] = p.APIKey
	}
	for _, p := range raw.Remote.Providers {
		result[p.ID] = p.APIKey
	}
	return result, nil
}

// parseDurations parses duration strings in providers and their limits.
func parseDurations(cfg *Config) error {
	for i := range cfg.Providers {
		p := &cfg.Providers[i]

		if p.RecoveryWindow != "" {
			d, err := time.ParseDuration(p.RecoveryWindow)
			if err != nil {
				return fmt.Errorf("provider %q: invalid recovery_window %q: %w", p.ID, p.RecoveryWindow, err)
			}
			p.recoveryWindowDur = d
		}

		if p.Limits != nil {
			if p.Limits.Window != "" {
				d, err := time.ParseDuration(p.Limits.Window)
				if err != nil {
					return fmt.Errorf("provider %q: invalid limits.window %q: %w", p.ID, p.Limits.Window, err)
				}
				p.Limits.windowDur = d
			}
		}
	}
	return nil
}

// validate enforces all schema rules for the old (version 2) schema.
func validate(cfg *Config) error {
	// Rule 1: version must be 2.
	if cfg.Version != 2 {
		return fmt.Errorf("config version must be 2, got %d", cfg.Version)
	}

	// Rule 5: routing.default_model must be non-empty.
	if strings.TrimSpace(cfg.Routing.DefaultModel) == "" {
		return fmt.Errorf("routing.default_model must not be empty")
	}

	// Rule 2: provider IDs must be unique.
	ids := make(map[string]bool, len(cfg.Providers))
	for _, p := range cfg.Providers {
		if p.ID == "" {
			return fmt.Errorf("provider missing id")
		}
		if ids[p.ID] {
			return fmt.Errorf("duplicate provider id: %s", p.ID)
		}
		ids[p.ID] = true
	}

	// Per-provider validation.
	for _, p := range cfg.Providers {
		if !validProviderTypes[p.Type] {
			return fmt.Errorf("provider %q: unknown type %q", p.ID, p.Type)
		}

		// Rule 3: endpoint required for certain types.
		if endpointRequiredTypes[p.Type] && p.Endpoint == "" {
			return fmt.Errorf("provider %q (type %q): endpoint is required", p.ID, p.Type)
		}

		// Rule 4: every model must have priority > 0.
		for _, m := range p.Models {
			if m.Priority <= 0 {
				return fmt.Errorf("provider %q: model %q must have priority > 0", p.ID, m.ID)
			}
		}

		// Rule 7: limits validation.
		if p.Limits != nil {
			if p.Limits.Requests <= 0 {
				return fmt.Errorf("provider %q: limits.requests must be > 0", p.ID)
			}
			if p.Limits.Window == "" {
				return fmt.Errorf("provider %q: limits.window must be set when limits block is present", p.ID)
			}
		}
	}

	// Rule 5: default_model must exist in at least one non-skipped provider.
	defaultModel := cfg.Routing.DefaultModel
	found := false
outer:
	for _, p := range cfg.Providers {
		if p.Skipped {
			continue
		}
		for _, m := range p.Models {
			if m.ID == defaultModel {
				found = true
				break outer
			}
		}
	}
	if !found {
		return fmt.Errorf("routing.default_model %q not found in any non-skipped provider", defaultModel)
	}

	return nil
}
