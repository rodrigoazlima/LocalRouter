# Routing Refactor: Priority + Limits System — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace tier-based local/remote routing with a unified priority + limits driven system with full model awareness.

**Architecture:** Single `providers:` config list (no local/remote split) feeds a model registry that builds a globally sorted `(priority, provider_id, model_id)` list. A state manager tracks AVAILABLE/UNHEALTHY/EXHAUSTED/BLOCKED per provider. The router iterates the sorted list, skips unavailable providers, and retries on failure.

**Tech Stack:** Go 1.22, chi router, yaml.v3 — no new dependencies.

**Spec:** `docs/superpowers/specs/2026-04-24-routing-refactor-design.md`

---

## File Map

| Action | Path | Responsibility |
|--------|------|----------------|
| Rewrite | `internal/config/config.go` | v2 schema: unified providers, models, limits, recovery_window |
| New | `internal/limits/tracker.go` | Fixed-window request counter per provider |
| New | `internal/limits/tracker_test.go` | Tests for limits tracker |
| New | `internal/registry/registry.go` | Sorted (provider, model) index |
| New | `internal/registry/registry_test.go` | Tests for registry |
| New | `internal/state/manager.go` | Unified BLOCKED/EXHAUSTED/UNHEALTHY/AVAILABLE state machine |
| New | `internal/state/manager_test.go` | Tests for state manager |
| Rewrite | `internal/metrics/metrics.go` | New counters (Requests, Failures; drop Tier1/2, Local/Remote) |
| Rewrite | `internal/provider/factory/factory.go` | Single `New(ProviderConfig)` function |
| Rewrite | `internal/router/router.go` | Model-aware routing via registry + state + limits |
| New | `internal/router/router_test.go` | Tests for router |
| Rewrite | `internal/server/server.go` | New Server struct (drop cache, add state+registry) |
| Rewrite | `internal/server/handlers.go` | New /health shape + /models handler |
| Rewrite | `cmd/localrouter/main.go` | New init flow; inline startup probe |
| Rewrite | `config.yaml` | v2 schema with free models and priorities |
| Delete | `internal/cache/cache.go` | Replaced by state.Manager |
| Delete | `internal/startup/probe.go` | Replaced by inline startup probe in main.go |

**Untouched:** `internal/provider/provider.go`, `internal/server/sse.go`, all provider adapters, `internal/health/monitor.go`, `internal/reqid/`.

---

## Task 1: Config v2 Schema

**Files:**
- Rewrite: `internal/config/config.go`
- Rewrite: `internal/config/config_test.go`

- [ ] **Step 1: Write failing tests**

```go
// internal/config/config_test.go
package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rodrigoazlima/localrouter/internal/config"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "config*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(content)
	f.Close()
	return f.Name()
}

func TestLoad_ValidV2(t *testing.T) {
	path := writeConfig(t, `
version: 2
routing:
  default_model: llama3.2:latest
  latency_threshold_ms: 2000
providers:
  - id: ollama-local
    type: ollama
    endpoint: http://localhost:11434
    timeout_ms: 3000
    recovery_window: 5m
    models:
      - id: llama3.2:latest
        priority: 1
        is_free: true
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Providers) != 1 {
		t.Fatalf("want 1 provider, got %d", len(cfg.Providers))
	}
	if cfg.Routing.DefaultModel != "llama3.2:latest" {
		t.Errorf("want default_model llama3.2:latest, got %s", cfg.Routing.DefaultModel)
	}
	if cfg.Providers[0].RecoveryWindowDur().Minutes() != 5 {
		t.Errorf("want 5m recovery window, got %v", cfg.Providers[0].RecoveryWindowDur())
	}
	if len(cfg.Providers[0].Models) != 1 {
		t.Fatalf("want 1 model, got %d", len(cfg.Providers[0].Models))
	}
	if cfg.Providers[0].Models[0].Priority != 1 {
		t.Errorf("want priority 1, got %d", cfg.Providers[0].Models[0].Priority)
	}
}

func TestLoad_WrongVersion(t *testing.T) {
	path := writeConfig(t, `
version: 1
routing:
  default_model: gpt-4
providers: []
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for version != 2")
	}
}

func TestLoad_DuplicateID(t *testing.T) {
	path := writeConfig(t, `
version: 2
routing:
  default_model: llama3.2:latest
providers:
  - id: same
    type: ollama
    endpoint: http://localhost:11434
    models:
      - id: llama3.2:latest
        priority: 1
  - id: same
    type: ollama
    endpoint: http://localhost:11435
    models:
      - id: llama3.2:latest
        priority: 2
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for duplicate id")
	}
}

func TestLoad_MissingPriority(t *testing.T) {
	path := writeConfig(t, `
version: 2
routing:
  default_model: llama3.2:latest
providers:
  - id: p1
    type: ollama
    endpoint: http://localhost:11434
    models:
      - id: llama3.2:latest
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for model with no priority")
	}
}

func TestLoad_DefaultModelMissing(t *testing.T) {
	path := writeConfig(t, `
version: 2
routing:
  default_model: does-not-exist
providers:
  - id: p1
    type: ollama
    endpoint: http://localhost:11434
    models:
      - id: llama3.2:latest
        priority: 1
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error: default_model not found in any provider")
	}
}

func TestLoad_SkipsProviderWithEmptyAPIKey(t *testing.T) {
	// api_key set but env var resolves to empty → provider marked as skipped
	os.Unsetenv("TEST_EMPTY_KEY")
	path := writeConfig(t, `
version: 2
routing:
  default_model: llama3.2:latest
providers:
  - id: p1
    type: ollama
    endpoint: http://localhost:11434
    models:
      - id: llama3.2:latest
        priority: 1
  - id: p2
    type: openai-compatible
    endpoint: https://api.example.com
    api_key: ${TEST_EMPTY_KEY}
    models:
      - id: some-model
        priority: 2
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// p2 should be in Providers but marked Skipped
	for _, p := range cfg.Providers {
		if p.ID == "p2" && !p.Skipped {
			t.Error("want p2 to be marked Skipped (empty api_key)")
		}
	}
}

func TestLoad_LimitsWindow(t *testing.T) {
	path := writeConfig(t, `
version: 2
routing:
  default_model: llama3.2:latest
providers:
  - id: groq
    type: openai-compatible
    endpoint: https://api.groq.com/openai
    api_key: somekey
    limits:
      requests: 100
      window: 1m
    models:
      - id: llama3.2:latest
        priority: 1
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	lim := cfg.Providers[0].Limits
	if lim == nil {
		t.Fatal("expected limits to be set")
	}
	if lim.Requests != 100 {
		t.Errorf("want 100 requests, got %d", lim.Requests)
	}
	if lim.WindowDur().Minutes() != 1 {
		t.Errorf("want 1m window, got %v", lim.WindowDur())
	}
}

func TestLoad_EnvExpansion(t *testing.T) {
	t.Setenv("MY_KEY", "secret")
	path := writeConfig(t, `
version: 2
routing:
  default_model: llama3.2:latest
providers:
  - id: p1
    type: openai-compatible
    endpoint: https://api.example.com
    api_key: ${MY_KEY}
    models:
      - id: llama3.2:latest
        priority: 1
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Providers[0].APIKey != "secret" {
		t.Errorf("want api_key=secret, got %s", cfg.Providers[0].APIKey)
	}
}

// Keep: verify config watcher still works — just check the Watcher type exists.
func TestNewWatcher_Exists(t *testing.T) {
	path := writeConfig(t, `
version: 2
routing:
  default_model: llama3.2:latest
providers:
  - id: p1
    type: ollama
    endpoint: http://localhost:11434
    models:
      - id: llama3.2:latest
        priority: 1
`)
	cfg, _ := config.Load(path)
	w, err := config.NewWatcher(path, cfg, func(_, _ *config.Config) {})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	w.Stop()
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/config/... -v 2>&1 | head -40
```

Expected: compile errors or test failures because structs don't exist yet.

- [ ] **Step 3: Rewrite `internal/config/config.go`**

```go
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Version   int              `yaml:"version"`
	Routing   RoutingConfig    `yaml:"routing"`
	Providers []ProviderConfig `yaml:"providers"`
}

type RoutingConfig struct {
	DefaultModel       string `yaml:"default_model"`
	LatencyThresholdMs int    `yaml:"latency_threshold_ms"`
}

type ProviderConfig struct {
	ID             string       `yaml:"id"`
	Type           string       `yaml:"type"`
	Endpoint       string       `yaml:"endpoint"`
	APIKey         string       `yaml:"api_key"`
	TimeoutMs      int          `yaml:"timeout_ms"`
	RecoveryWindow string       `yaml:"recovery_window"`
	Limits         *LimitsConfig `yaml:"limits"`
	Models         []ModelConfig `yaml:"models"`

	Skipped           bool          `yaml:"-"`
	recoveryWindowDur time.Duration `yaml:"-"`
}

func (p ProviderConfig) RecoveryWindowDur() time.Duration {
	if p.recoveryWindowDur == 0 {
		return time.Hour
	}
	return p.recoveryWindowDur
}

func (p ProviderConfig) Redacted() ProviderConfig {
	if p.APIKey != "" {
		p.APIKey = "[REDACTED]"
	}
	return p
}

type LimitsConfig struct {
	Requests  int    `yaml:"requests"`
	Window    string `yaml:"window"`
	windowDur time.Duration
}

func (l *LimitsConfig) WindowDur() time.Duration {
	return l.windowDur
}

type ModelConfig struct {
	ID       string `yaml:"id"`
	Priority int    `yaml:"priority"`
	IsFree   bool   `yaml:"is_free"`
}

var validTypes = map[string]bool{
	"ollama": true, "openai-compatible": true,
	"anthropic": true, "google": true, "cohere": true, "mistral": true,
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
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		p.APIKey = expand(p.APIKey)
		p.Endpoint = expand(p.Endpoint)
	}
}

func expand(s string) string {
	if !strings.Contains(s, "${") {
		return s
	}
	return os.ExpandEnv(s)
}

func validate(cfg *Config) error {
	if cfg.Version != 2 {
		return fmt.Errorf("config version %d not supported: migrate to version 2", cfg.Version)
	}

	ids := make(map[string]bool)
	allModelIDs := make(map[string]bool)

	for i := range cfg.Providers {
		p := &cfg.Providers[i]

		if p.ID == "" {
			return fmt.Errorf("provider missing id")
		}
		if ids[p.ID] {
			return fmt.Errorf("duplicate provider id: %s", p.ID)
		}
		ids[p.ID] = true

		if !validTypes[p.Type] {
			return fmt.Errorf("provider %s: unknown type %q", p.ID, p.Type)
		}
		if p.Type == "ollama" || p.Type == "openai-compatible" || p.Type == "mistral" {
			if p.Endpoint == "" {
				return fmt.Errorf("provider %s: endpoint required for type %s", p.ID, p.Type)
			}
		}

		// api_key set but resolves to empty → mark skipped
		if p.APIKey == "" && hasAPIKeyField(cfg, i) {
			// handled below via Skipped
		}
		// Mark skipped if api_key was configured (non-empty raw yaml) but resolved to empty.
		// Since we already expanded env vars, check if result is empty but original had ${...}.
		// Simplest approach: if api_key is empty after expansion AND was non-empty before, skip.
		// We detect this by checking if the raw yaml had an api_key key — we can't know post-expand.
		// Instead: any provider with api_key field present (even if empty after expansion) that is empty → skip.
		// Since yaml.Unmarshal gives "" for both "absent" and "empty-string", we use a sentinel approach:
		// if Endpoint is "" but type requires it, we already error above.
		// For api_key: the provider was explicitly configured with an api_key field if it was non-empty in yaml.
		// After ExpandEnv, if it resolves to "" we mark Skipped.
		// We cannot distinguish "api_key was absent" from "api_key: ${EMPTY_VAR}" post-expansion.
		// Design decision: if api_key == "" after expansion, assume no key needed (public provider).
		// If api_key != "" → key present and valid.
		// This means: to skip a provider with missing key, user sets api_key: ${MISSING_VAR}.
		// After expansion: APIKey == "" → Skipped = true.
		// We only set Skipped if the raw yaml had api_key set. We detect this by checking if
		// the unexpanded value was non-empty. We do this via re-reading... which is complex.
		// SIMPLIFICATION per design: if api_key field is present in config and resolves empty → skip.
		// We approximate this: mark Skipped if APIKey == "" AND there's a dollar sign pattern.
		// Actually: the cleanest approach is to track the raw value before expansion.
		// Let's track it with a separate field in a two-pass parse.
		//
		// For now: we implement the simpler rule: if APIKey == "" → not skipped (public).
		// If user sets api_key: ${MISSING_KEY} and the var is unset, expansion gives "", so Skipped=true.
		// We detect this by re-reading the raw yaml. See rawAPIKey handling below.

		if p.RecoveryWindow != "" {
			d, err := time.ParseDuration(p.RecoveryWindow)
			if err != nil {
				return fmt.Errorf("provider %s: invalid recovery_window %q: %w", p.ID, p.RecoveryWindow, err)
			}
			p.recoveryWindowDur = d
		}

		if p.Limits != nil {
			if p.Limits.Requests <= 0 {
				return fmt.Errorf("provider %s: limits.requests must be > 0", p.ID)
			}
			if p.Limits.Window == "" {
				return fmt.Errorf("provider %s: limits.window required when limits set", p.ID)
			}
			d, err := time.ParseDuration(p.Limits.Window)
			if err != nil {
				return fmt.Errorf("provider %s: invalid limits.window %q: %w", p.ID, p.Limits.Window, err)
			}
			p.Limits.windowDur = d
		}

		for _, m := range p.Models {
			if m.ID == "" {
				return fmt.Errorf("provider %s: model missing id", p.ID)
			}
			if m.Priority <= 0 {
				return fmt.Errorf("provider %s model %s: priority must be > 0", p.ID, m.ID)
			}
			allModelIDs[m.ID] = true
		}
	}

	// Validate default_model exists in at least one non-skipped provider.
	if cfg.Routing.DefaultModel == "" {
		return fmt.Errorf("routing.default_model is required")
	}
	if !allModelIDs[cfg.Routing.DefaultModel] {
		return fmt.Errorf("routing.default_model %q not found in any provider", cfg.Routing.DefaultModel)
	}

	// Mark providers with empty APIKey that had api_key configured.
	// We re-parse raw to detect which providers had an api_key key set.
	markSkipped(data, cfg)

	return nil
}

// markSkipped uses a lightweight re-parse to detect which providers
// had api_key explicitly set in yaml (even if it resolved to empty after env expansion).
func markSkipped(data []byte, cfg *Config) {
	// Parse raw to get pre-expansion api_key values.
	var raw struct {
		Providers []struct {
			ID     string `yaml:"id"`
			APIKey string `yaml:"api_key"`
		} `yaml:"providers"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return
	}
	rawKeys := make(map[string]string, len(raw.Providers))
	for _, p := range raw.Providers {
		rawKeys[p.ID] = p.APIKey
	}
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		rawKey := rawKeys[p.ID]
		// If yaml had a non-empty api_key value (e.g. "${MISSING_VAR}") but it expanded to empty → skip.
		if rawKey != "" && p.APIKey == "" {
			p.Skipped = true
		}
	}
}

// hasAPIKeyField is unused — see markSkipped instead.
func hasAPIKeyField(_ *Config, _ int) bool { return false }
```

Wait — there's a bug: `validate` function references `data` which is not in scope. The data is in `Load`. Restructure:

```go
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Version   int              `yaml:"version"`
	Routing   RoutingConfig    `yaml:"routing"`
	Providers []ProviderConfig `yaml:"providers"`
}

type RoutingConfig struct {
	DefaultModel       string `yaml:"default_model"`
	LatencyThresholdMs int    `yaml:"latency_threshold_ms"`
}

type ProviderConfig struct {
	ID             string        `yaml:"id"`
	Type           string        `yaml:"type"`
	Endpoint       string        `yaml:"endpoint"`
	APIKey         string        `yaml:"api_key"`
	TimeoutMs      int           `yaml:"timeout_ms"`
	RecoveryWindow string        `yaml:"recovery_window"`
	Limits         *LimitsConfig `yaml:"limits"`
	Models         []ModelConfig `yaml:"models"`
	Skipped        bool          `yaml:"-"`

	recoveryWindowDur time.Duration
}

func (p ProviderConfig) RecoveryWindowDur() time.Duration {
	if p.recoveryWindowDur == 0 {
		return time.Hour
	}
	return p.recoveryWindowDur
}

func (p ProviderConfig) Redacted() ProviderConfig {
	if p.APIKey != "" {
		p.APIKey = "[REDACTED]"
	}
	return p
}

type LimitsConfig struct {
	Requests  int    `yaml:"requests"`
	Window    string `yaml:"window"`
	windowDur time.Duration
}

func (l *LimitsConfig) WindowDur() time.Duration {
	return l.windowDur
}

type ModelConfig struct {
	ID       string `yaml:"id"`
	Priority int    `yaml:"priority"`
	IsFree   bool   `yaml:"is_free"`
}

var validTypes = map[string]bool{
	"ollama": true, "openai-compatible": true,
	"anthropic": true, "google": true, "cohere": true, "mistral": true,
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
	// detect which providers had api_key set in yaml before env expansion
	rawKeys := parseRawAPIKeys(data)
	expandEnv(&cfg)
	if err := validate(&cfg, rawKeys); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func parseRawAPIKeys(data []byte) map[string]string {
	var raw struct {
		Providers []struct {
			ID     string `yaml:"id"`
			APIKey string `yaml:"api_key"`
		} `yaml:"providers"`
	}
	yaml.Unmarshal(data, &raw) //nolint:errcheck
	out := make(map[string]string, len(raw.Providers))
	for _, p := range raw.Providers {
		out[p.ID] = p.APIKey
	}
	return out
}

func expandEnv(cfg *Config) {
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		p.APIKey = expand(p.APIKey)
		p.Endpoint = expand(p.Endpoint)
	}
}

func expand(s string) string {
	if !strings.Contains(s, "${") {
		return s
	}
	return os.ExpandEnv(s)
}

func validate(cfg *Config, rawKeys map[string]string) error {
	if cfg.Version != 2 {
		return fmt.Errorf("config version %d not supported: migrate to version 2", cfg.Version)
	}

	ids := make(map[string]bool)
	allModelIDs := make(map[string]bool)

	for i := range cfg.Providers {
		p := &cfg.Providers[i]

		if p.ID == "" {
			return fmt.Errorf("provider missing id")
		}
		if ids[p.ID] {
			return fmt.Errorf("duplicate provider id: %s", p.ID)
		}
		ids[p.ID] = true

		if !validTypes[p.Type] {
			return fmt.Errorf("provider %s: unknown type %q", p.ID, p.Type)
		}
		if p.Type == "ollama" || p.Type == "openai-compatible" || p.Type == "mistral" {
			if p.Endpoint == "" {
				return fmt.Errorf("provider %s: endpoint required for type %s", p.ID, p.Type)
			}
		}

		// If api_key was explicitly set in yaml (rawKey != "") but resolved to empty → skip provider.
		if rawKeys[p.ID] != "" && p.APIKey == "" {
			p.Skipped = true
		}

		if p.RecoveryWindow != "" {
			d, err := time.ParseDuration(p.RecoveryWindow)
			if err != nil {
				return fmt.Errorf("provider %s: invalid recovery_window %q: %w", p.ID, p.RecoveryWindow, err)
			}
			p.recoveryWindowDur = d
		}

		if p.Limits != nil {
			if p.Limits.Requests <= 0 {
				return fmt.Errorf("provider %s: limits.requests must be > 0", p.ID)
			}
			if p.Limits.Window == "" {
				return fmt.Errorf("provider %s: limits.window required when limits set", p.ID)
			}
			d, err := time.ParseDuration(p.Limits.Window)
			if err != nil {
				return fmt.Errorf("provider %s: invalid limits.window %q: %w", p.ID, p.Limits.Window, err)
			}
			p.Limits.windowDur = d
		}

		for _, m := range p.Models {
			if m.ID == "" {
				return fmt.Errorf("provider %s: model missing id", p.ID)
			}
			if m.Priority <= 0 {
				return fmt.Errorf("provider %s model %s: priority must be > 0", p.ID, m.ID)
			}
			if !p.Skipped {
				allModelIDs[m.ID] = true
			}
		}
	}

	if cfg.Routing.DefaultModel == "" {
		return fmt.Errorf("routing.default_model is required")
	}
	if !allModelIDs[cfg.Routing.DefaultModel] {
		return fmt.Errorf("routing.default_model %q not found in any non-skipped provider", cfg.Routing.DefaultModel)
	}
	return nil
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/config/... -v
```

Expected: all tests pass. If `TestLoad_SkipsProviderWithEmptyAPIKey` fails, verify `TEST_EMPTY_KEY` env var is unset.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): rewrite to v2 schema — unified providers list"
```

---

## Task 2: Limits Tracker

**Files:**
- Create: `internal/limits/tracker.go`
- Create: `internal/limits/tracker_test.go`

- [ ] **Step 1: Write failing test**

```go
// internal/limits/tracker_test.go
package limits_test

import (
	"testing"
	"time"

	"github.com/rodrigoazlima/localrouter/internal/limits"
)

func TestTracker_NoLimits(t *testing.T) {
	tr := limits.New(nil)
	for i := 0; i < 1000; i++ {
		exhausted, resetAt := tr.Record("p1")
		if exhausted {
			t.Fatal("should never be exhausted when no limits configured")
		}
		if !resetAt.IsZero() {
			t.Fatal("resetAt should be zero when no limits")
		}
	}
}

func TestTracker_WithinLimit(t *testing.T) {
	tr := limits.New(map[string]limits.Config{
		"p1": {Requests: 3, Window: time.Minute},
	})
	for i := 0; i < 3; i++ {
		exhausted, _ := tr.Record("p1")
		if exhausted {
			t.Fatalf("call %d: should not be exhausted (limit is 3)", i+1)
		}
	}
}

func TestTracker_Exhausted(t *testing.T) {
	tr := limits.New(map[string]limits.Config{
		"p1": {Requests: 2, Window: time.Minute},
	})
	tr.Record("p1") // 1
	tr.Record("p1") // 2
	exhausted, resetAt := tr.Record("p1") // 3 — over limit
	if !exhausted {
		t.Fatal("expected exhausted=true on 3rd call with limit 2")
	}
	if resetAt.IsZero() {
		t.Fatal("expected non-zero resetAt when exhausted")
	}
	if resetAt.Before(time.Now()) {
		t.Fatal("resetAt should be in the future")
	}
}

func TestTracker_WindowReset(t *testing.T) {
	tr := limits.New(map[string]limits.Config{
		"p1": {Requests: 1, Window: 50 * time.Millisecond},
	})
	tr.Record("p1") // 1 — at limit
	exhausted, _ := tr.Record("p1") // 2 — over limit
	if !exhausted {
		t.Fatal("expected exhausted before window reset")
	}

	time.Sleep(60 * time.Millisecond)

	// Window should have reset — counter back to 0.
	exhausted, _ = tr.Record("p1") // 1 again
	if exhausted {
		t.Fatal("expected not exhausted after window reset")
	}
}

func TestTracker_ResetAt(t *testing.T) {
	tr := limits.New(map[string]limits.Config{
		"p1": {Requests: 5, Window: time.Hour},
	})
	if !tr.ResetAt("p1").IsZero() {
		t.Error("ResetAt should be zero before first Record")
	}
	tr.Record("p1")
	if tr.ResetAt("p1").IsZero() {
		t.Error("ResetAt should be non-zero after first Record")
	}
}

func TestTracker_IndependentProviders(t *testing.T) {
	tr := limits.New(map[string]limits.Config{
		"p1": {Requests: 1, Window: time.Minute},
		"p2": {Requests: 10, Window: time.Minute},
	})
	tr.Record("p1")
	exhausted, _ := tr.Record("p1")
	if !exhausted {
		t.Error("p1 should be exhausted")
	}
	exhausted, _ = tr.Record("p2")
	if exhausted {
		t.Error("p2 should not be exhausted")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/limits/... -v 2>&1 | head -20
```

Expected: compile error — package `limits` not found.

- [ ] **Step 3: Implement `internal/limits/tracker.go`**

```go
package limits

import (
	"sync"
	"time"
)

type Config struct {
	Requests int
	Window   time.Duration
}

type window struct {
	count   int
	resetAt time.Time
	max     int
	dur     time.Duration
}

type Tracker struct {
	mu      sync.Mutex
	windows map[string]*window
	configs map[string]Config
}

func New(configs map[string]Config) *Tracker {
	if configs == nil {
		configs = make(map[string]Config)
	}
	return &Tracker{
		windows: make(map[string]*window),
		configs: configs,
	}
}

// Record increments the counter for id.
// Returns (exhausted, resetAt). If no limit configured, always (false, zero).
func (t *Tracker) Record(id string) (bool, time.Time) {
	cfg, ok := t.configs[id]
	if !ok || cfg.Requests <= 0 {
		return false, time.Time{}
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	w, exists := t.windows[id]
	if !exists || now.After(w.resetAt) {
		w = &window{
			max:     cfg.Requests,
			dur:     cfg.Window,
			resetAt: now.Add(cfg.Window),
		}
		t.windows[id] = w
	}

	w.count++
	if w.count > w.max {
		return true, w.resetAt
	}
	return false, time.Time{}
}

// ResetAt returns when the current window expires for id (zero if no window started).
func (t *Tracker) ResetAt(id string) time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	w, ok := t.windows[id]
	if !ok {
		return time.Time{}
	}
	return w.resetAt
}

// ConfigFor returns the limits Config for the given provider id (for /models endpoint).
func (t *Tracker) ConfigFor(id string) (Config, bool) {
	cfg, ok := t.configs[id]
	return cfg, ok
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/limits/... -v
```

Expected: all 6 tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/limits/tracker.go internal/limits/tracker_test.go
git commit -m "feat(limits): add fixed-window request tracker per provider"
```

---

## Task 3: Model Registry

**Files:**
- Create: `internal/registry/registry.go`
- Create: `internal/registry/registry_test.go`

- [ ] **Step 1: Write failing tests**

```go
// internal/registry/registry_test.go
package registry_test

import (
	"testing"

	"github.com/rodrigoazlima/localrouter/internal/config"
	"github.com/rodrigoazlima/localrouter/internal/registry"
)

func makeProviders() []config.ProviderConfig {
	return []config.ProviderConfig{
		{
			ID:   "zzz-provider", // alphabetically last ID
			Type: "ollama",
			Models: []config.ModelConfig{
				{ID: "llama3:latest", Priority: 3, IsFree: true},
				{ID: "qwen:7b", Priority: 1, IsFree: true},
			},
		},
		{
			ID:   "aaa-provider", // alphabetically first ID
			Type: "openai-compatible",
			Models: []config.ModelConfig{
				{ID: "gpt-4o-mini", Priority: 2, IsFree: false},
				{ID: "llama3:latest", Priority: 5, IsFree: true},
			},
		},
	}
}

func TestBuild_SortOrder(t *testing.T) {
	reg := registry.Build(makeProviders(), "qwen:7b")
	list := reg.GlobalList()

	// Expected order: (priority ASC, providerID ASC, modelID ASC)
	// priority=1: qwen:7b / zzz-provider
	// priority=2: gpt-4o-mini / aaa-provider
	// priority=3: llama3:latest / zzz-provider
	// priority=5: llama3:latest / aaa-provider
	if len(list) != 4 {
		t.Fatalf("want 4 entries, got %d", len(list))
	}
	if list[0].ModelID != "qwen:7b" || list[0].ProviderID != "zzz-provider" {
		t.Errorf("entry 0: got %+v", list[0])
	}
	if list[1].ModelID != "gpt-4o-mini" || list[1].ProviderID != "aaa-provider" {
		t.Errorf("entry 1: got %+v", list[1])
	}
	if list[2].ProviderID != "zzz-provider" {
		t.Errorf("entry 2: want zzz-provider, got %s", list[2].ProviderID)
	}
	if list[3].ProviderID != "aaa-provider" {
		t.Errorf("entry 3: want aaa-provider, got %s", list[3].ProviderID)
	}
}

func TestBuild_IsDefault(t *testing.T) {
	reg := registry.Build(makeProviders(), "gpt-4o-mini")
	for _, e := range reg.GlobalList() {
		if e.ModelID == "gpt-4o-mini" && !e.IsDefault {
			t.Errorf("gpt-4o-mini at provider %s should have IsDefault=true", e.ProviderID)
		}
		if e.ModelID != "gpt-4o-mini" && e.IsDefault {
			t.Errorf("model %s at provider %s should not be default", e.ModelID, e.ProviderID)
		}
	}
}

func TestBuild_ForModel(t *testing.T) {
	reg := registry.Build(makeProviders(), "qwen:7b")
	entries := reg.ForModel("llama3:latest")
	if len(entries) != 2 {
		t.Fatalf("want 2 entries for llama3:latest, got %d", len(entries))
	}
	// zzz-provider has priority 3, aaa-provider has priority 5 → zzz first
	if entries[0].ProviderID != "zzz-provider" {
		t.Errorf("want zzz-provider first (lower priority), got %s", entries[0].ProviderID)
	}
}

func TestBuild_ForModel_Unknown(t *testing.T) {
	reg := registry.Build(makeProviders(), "qwen:7b")
	if entries := reg.ForModel("does-not-exist"); len(entries) != 0 {
		t.Errorf("want empty slice for unknown model, got %v", entries)
	}
}

func TestBuild_SkipsSkippedProviders(t *testing.T) {
	providers := []config.ProviderConfig{
		{
			ID:      "active",
			Type:    "ollama",
			Skipped: false,
			Models:  []config.ModelConfig{{ID: "m1", Priority: 1}},
		},
		{
			ID:      "skipped",
			Type:    "ollama",
			Skipped: true,
			Models:  []config.ModelConfig{{ID: "m1", Priority: 2}},
		},
	}
	reg := registry.Build(providers, "m1")
	for _, e := range reg.GlobalList() {
		if e.ProviderID == "skipped" {
			t.Error("skipped provider should not appear in registry")
		}
	}
}

func TestBuild_Providers(t *testing.T) {
	reg := registry.Build(makeProviders(), "qwen:7b")
	ids := reg.ProviderIDs()
	if len(ids) != 2 {
		t.Fatalf("want 2 provider IDs, got %d", len(ids))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/registry/... -v 2>&1 | head -20
```

Expected: compile error — package `registry` not found.

- [ ] **Step 3: Implement `internal/registry/registry.go`**

```go
package registry

import (
	"sort"

	"github.com/rodrigoazlima/localrouter/internal/config"
)

type Entry struct {
	ProviderID string
	ModelID    string
	Priority   int
	IsFree     bool
	IsDefault  bool
}

type Registry struct {
	ordered   []Entry
	byModel   map[string][]Entry
	providers []string // unique provider IDs in order they appear
}

func Build(providers []config.ProviderConfig, defaultModel string) *Registry {
	var all []Entry
	seen := make(map[string]bool)
	var providerIDs []string

	for _, p := range providers {
		if p.Skipped {
			continue
		}
		if !seen[p.ID] {
			seen[p.ID] = true
			providerIDs = append(providerIDs, p.ID)
		}
		for _, m := range p.Models {
			all = append(all, Entry{
				ProviderID: p.ID,
				ModelID:    m.ID,
				Priority:   m.Priority,
				IsFree:     m.IsFree,
				IsDefault:  m.ID == defaultModel,
			})
		}
	}

	sort.Slice(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		if a.ProviderID != b.ProviderID {
			return a.ProviderID < b.ProviderID
		}
		return a.ModelID < b.ModelID
	})

	byModel := make(map[string][]Entry)
	for _, e := range all {
		byModel[e.ModelID] = append(byModel[e.ModelID], e)
	}

	return &Registry{ordered: all, byModel: byModel, providers: providerIDs}
}

// GlobalList returns all entries sorted by (priority, providerID, modelID). Used for model=auto routing.
func (r *Registry) GlobalList() []Entry {
	return r.ordered
}

// ForModel returns entries for a specific model id, sorted by (priority, providerID, modelID).
// Returns nil if model is unknown.
func (r *Registry) ForModel(id string) []Entry {
	return r.byModel[id]
}

// ProviderIDs returns unique provider IDs that have at least one model in the registry.
func (r *Registry) ProviderIDs() []string {
	return r.providers
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/registry/... -v
```

Expected: all 6 tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/registry/registry.go internal/registry/registry_test.go
git commit -m "feat(registry): add model-to-provider sorted index"
```

---

## Task 4: State Manager

**Files:**
- Create: `internal/state/manager.go`
- Create: `internal/state/manager_test.go`

- [ ] **Step 1: Write failing tests**

```go
// internal/state/manager_test.go
package state_test

import (
	"testing"
	"time"

	"github.com/rodrigoazlima/localrouter/internal/state"
)

// fakeHealth implements state.HealthReader.
type fakeHealth struct {
	ready map[string]bool
}

func (f *fakeHealth) IsReady(id string) bool {
	return f.ready[id]
}

func TestGetState_Available(t *testing.T) {
	h := &fakeHealth{ready: map[string]bool{"p1": true}}
	m := state.New(h)
	if got := m.GetState("p1"); got != state.StateAvailable {
		t.Errorf("want Available, got %v", got)
	}
}

func TestGetState_Unhealthy(t *testing.T) {
	h := &fakeHealth{ready: map[string]bool{"p1": false}}
	m := state.New(h)
	if got := m.GetState("p1"); got != state.StateUnhealthy {
		t.Errorf("want Unhealthy, got %v", got)
	}
}

func TestBlock_BeforeExpiry(t *testing.T) {
	h := &fakeHealth{ready: map[string]bool{"p1": true}}
	m := state.New(h)
	m.Block("p1", time.Minute)
	if got := m.GetState("p1"); got != state.StateBlocked {
		t.Errorf("want Blocked, got %v", got)
	}
}

func TestBlock_AfterExpiry(t *testing.T) {
	h := &fakeHealth{ready: map[string]bool{"p1": true}}
	m := state.New(h)
	m.Block("p1", 50*time.Millisecond)
	time.Sleep(60 * time.Millisecond)
	if got := m.GetState("p1"); got != state.StateAvailable {
		t.Errorf("want Available after expiry, got %v", got)
	}
}

func TestExhausted_BeforeReset(t *testing.T) {
	h := &fakeHealth{ready: map[string]bool{"p1": true}}
	m := state.New(h)
	m.SetExhausted("p1", time.Now().Add(time.Minute))
	if got := m.GetState("p1"); got != state.StateExhausted {
		t.Errorf("want Exhausted, got %v", got)
	}
}

func TestExhausted_AfterReset(t *testing.T) {
	h := &fakeHealth{ready: map[string]bool{"p1": true}}
	m := state.New(h)
	m.SetExhausted("p1", time.Now().Add(50*time.Millisecond))
	time.Sleep(60 * time.Millisecond)
	if got := m.GetState("p1"); got != state.StateAvailable {
		t.Errorf("want Available after reset, got %v", got)
	}
}

func TestPrecedence_BlockedOverExhausted(t *testing.T) {
	h := &fakeHealth{ready: map[string]bool{"p1": true}}
	m := state.New(h)
	m.Block("p1", time.Minute)
	m.SetExhausted("p1", time.Now().Add(time.Minute))
	if got := m.GetState("p1"); got != state.StateBlocked {
		t.Errorf("want Blocked (highest precedence), got %v", got)
	}
}

func TestPrecedence_ExhaustedOverUnhealthy(t *testing.T) {
	h := &fakeHealth{ready: map[string]bool{"p1": false}} // unhealthy
	m := state.New(h)
	m.SetExhausted("p1", time.Now().Add(time.Minute))
	if got := m.GetState("p1"); got != state.StateExhausted {
		t.Errorf("want Exhausted over Unhealthy, got %v", got)
	}
}

func TestStateString(t *testing.T) {
	cases := []struct {
		s    state.State
		want string
	}{
		{state.StateAvailable, "available"},
		{state.StateUnhealthy, "unhealthy"},
		{state.StateExhausted, "exhausted"},
		{state.StateBlocked, "blocked"},
	}
	for _, c := range cases {
		if got := c.s.String(); got != c.want {
			t.Errorf("State(%d).String() = %q, want %q", c.s, got, c.want)
		}
	}
}

func TestBlockedUntil(t *testing.T) {
	h := &fakeHealth{ready: map[string]bool{"p1": true}}
	m := state.New(h)
	if !m.BlockedUntil("p1").IsZero() {
		t.Error("BlockedUntil should be zero before any Block call")
	}
	m.Block("p1", time.Hour)
	if m.BlockedUntil("p1").IsZero() {
		t.Error("BlockedUntil should be non-zero after Block call")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/state/... -v 2>&1 | head -20
```

Expected: compile error — package `state` not found.

- [ ] **Step 3: Implement `internal/state/manager.go`**

```go
package state

import (
	"sync"
	"time"
)

type State int

const (
	StateAvailable State = iota
	StateUnhealthy
	StateExhausted
	StateBlocked
)

func (s State) String() string {
	switch s {
	case StateAvailable:
		return "available"
	case StateUnhealthy:
		return "unhealthy"
	case StateExhausted:
		return "exhausted"
	case StateBlocked:
		return "blocked"
	default:
		return "unknown"
	}
}

// HealthReader is the subset of health.Monitor used by the state manager.
type HealthReader interface {
	IsReady(id string) bool
}

type Manager struct {
	mu        sync.RWMutex
	blocked   map[string]time.Time // provider id → blocked until
	exhausted map[string]time.Time // provider id → exhausted until
	health    HealthReader
}

func New(h HealthReader) *Manager {
	return &Manager{
		blocked:   make(map[string]time.Time),
		exhausted: make(map[string]time.Time),
		health:    h,
	}
}

// GetState returns the effective state for a provider.
// Precedence: BLOCKED > EXHAUSTED > UNHEALTHY > AVAILABLE.
func (m *Manager) GetState(id string) State {
	now := time.Now()

	m.mu.RLock()
	bu := m.blocked[id]
	eu := m.exhausted[id]
	m.mu.RUnlock()

	if now.Before(bu) {
		return StateBlocked
	}
	if now.Before(eu) {
		return StateExhausted
	}
	if !m.health.IsReady(id) {
		return StateUnhealthy
	}
	return StateAvailable
}

// Block sets the provider to BLOCKED state for duration d.
func (m *Manager) Block(id string, d time.Duration) {
	m.mu.Lock()
	m.blocked[id] = time.Now().Add(d)
	m.mu.Unlock()
}

// SetExhausted sets the provider to EXHAUSTED state until resetAt.
func (m *Manager) SetExhausted(id string, resetAt time.Time) {
	m.mu.Lock()
	m.exhausted[id] = resetAt
	m.mu.Unlock()
}

// BlockedUntil returns the time until which the provider is blocked (zero if not blocked).
func (m *Manager) BlockedUntil(id string) time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.blocked[id]
}

// ExhaustedUntil returns the time until which the provider is exhausted (zero if not exhausted).
func (m *Manager) ExhaustedUntil(id string) time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.exhausted[id]
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/state/... -v
```

Expected: all 9 tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/state/manager.go internal/state/manager_test.go
git commit -m "feat(state): add unified provider state machine"
```

---

## Task 5: Metrics v2

**Files:**
- Rewrite: `internal/metrics/metrics.go`

No new test file needed — metrics is covered by integration tests. The `/metrics` endpoint test is in Task 9.

- [ ] **Step 1: Rewrite `internal/metrics/metrics.go`**

Replace tier/local/remote terminology. New counters: `Requests`, `Failures`, `ProviderExhaustedEvents`.

```go
package metrics

import (
	"sync"
	"sync/atomic"
)

type Collector struct {
	Requests                atomic.Int64
	Failures                atomic.Int64
	NoCapacity              atomic.Int64
	StreamsStarted          atomic.Int64
	StreamsCompleted        atomic.Int64
	StreamsDisconnected     atomic.Int64
	StreamDuration          atomic.Int64
	ProviderBlockEvents     atomic.Int64
	ProviderExhaustedEvents atomic.Int64

	mu                 sync.RWMutex
	providerChecksOK   map[string]*atomic.Int64
	providerChecksFail map[string]*atomic.Int64
	providerLatencyMs  map[string]*atomic.Int64
}

type Snapshot struct {
	Requests                int64                       `json:"requests"`
	Failures                int64                       `json:"failures"`
	NoCapacity              int64                       `json:"no_capacity"`
	StreamsStarted          int64                       `json:"streams_started"`
	StreamsCompleted        int64                       `json:"streams_completed"`
	StreamsDisconnected     int64                       `json:"streams_disconnected"`
	StreamDurationMs        int64                       `json:"stream_duration_ms"`
	ProviderBlockEvents     int64                       `json:"provider_block_events"`
	ProviderExhaustedEvents int64                       `json:"provider_exhausted_events"`
	Providers               map[string]ProviderSnapshot `json:"providers"`
}

type ProviderSnapshot struct {
	ChecksOK   int64 `json:"checks_ok"`
	ChecksFail int64 `json:"checks_fail"`
	LatencyMs  int64 `json:"latency_ms"`
}

func New() *Collector {
	return &Collector{
		providerChecksOK:   make(map[string]*atomic.Int64),
		providerChecksFail: make(map[string]*atomic.Int64),
		providerLatencyMs:  make(map[string]*atomic.Int64),
	}
}

func (c *Collector) ensureProvider(id string) {
	if _, ok := c.providerChecksOK[id]; !ok {
		c.providerChecksOK[id] = &atomic.Int64{}
		c.providerChecksFail[id] = &atomic.Int64{}
		c.providerLatencyMs[id] = &atomic.Int64{}
	}
}

func (c *Collector) ProviderOK(id string, latencyMs int64) {
	c.mu.Lock()
	c.ensureProvider(id)
	ok := c.providerChecksOK[id]
	lat := c.providerLatencyMs[id]
	c.mu.Unlock()
	ok.Add(1)
	lat.Store(latencyMs)
}

func (c *Collector) ProviderFail(id string) {
	c.mu.Lock()
	c.ensureProvider(id)
	fail := c.providerChecksFail[id]
	c.mu.Unlock()
	fail.Add(1)
}

func (c *Collector) Snapshot() Snapshot {
	c.mu.RLock()
	providers := make(map[string]ProviderSnapshot, len(c.providerChecksOK))
	for id := range c.providerChecksOK {
		providers[id] = ProviderSnapshot{
			ChecksOK:   c.providerChecksOK[id].Load(),
			ChecksFail: c.providerChecksFail[id].Load(),
			LatencyMs:  c.providerLatencyMs[id].Load(),
		}
	}
	c.mu.RUnlock()
	return Snapshot{
		Requests:                c.Requests.Load(),
		Failures:                c.Failures.Load(),
		NoCapacity:              c.NoCapacity.Load(),
		StreamsStarted:          c.StreamsStarted.Load(),
		StreamsCompleted:        c.StreamsCompleted.Load(),
		StreamsDisconnected:     c.StreamsDisconnected.Load(),
		StreamDurationMs:        c.StreamDuration.Load(),
		ProviderBlockEvents:     c.ProviderBlockEvents.Load(),
		ProviderExhaustedEvents: c.ProviderExhaustedEvents.Load(),
		Providers:               providers,
	}
}
```

- [ ] **Step 2: Fix health/monitor.go — rename NodeOK/NodeFail to ProviderOK/ProviderFail**

In `internal/health/monitor.go` lines 150 and 168, change:
```go
// Line 150 (in failure branch):
mon.metrics.NodeFail(id)
// →
mon.metrics.ProviderFail(id)

// Line 168 (in success branch):
mon.metrics.NodeOK(id, latency)
// →
mon.metrics.ProviderOK(id, latency)
```

- [ ] **Step 3: Verify compilation**

```bash
go build ./...
```

Expected: no errors.

- [ ] **Step 4: Commit**

```bash
git add internal/metrics/metrics.go internal/health/monitor.go
git commit -m "feat(metrics): replace tier/local/remote counters with unified provider metrics"
```

---

## Task 6: Factory Unification

**Files:**
- Rewrite: `internal/provider/factory/factory.go`

The old factory had `NewFromNode` and `NewFromRemote`. Replace with a single `New(ProviderConfig)` function.

- [ ] **Step 1: Rewrite `internal/provider/factory/factory.go`**

```go
package factory

import (
	"fmt"

	"github.com/rodrigoazlima/localrouter/internal/config"
	"github.com/rodrigoazlima/localrouter/internal/provider"
	"github.com/rodrigoazlima/localrouter/internal/provider/anthropic"
	"github.com/rodrigoazlima/localrouter/internal/provider/cohere"
	"github.com/rodrigoazlima/localrouter/internal/provider/google"
	"github.com/rodrigoazlima/localrouter/internal/provider/ollama"
	"github.com/rodrigoazlima/localrouter/internal/provider/openaicompat"
)

const defaultTimeoutMs = 30000

// New creates a provider.Provider from a ProviderConfig.
// Returns an error if the type is unknown.
func New(p config.ProviderConfig) (provider.Provider, error) {
	timeoutMs := p.TimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = defaultTimeoutMs
	}
	switch p.Type {
	case "ollama":
		return ollama.New(p.ID, p.Endpoint, p.APIKey, timeoutMs), nil
	case "openai-compatible", "mistral":
		return openaicompat.New(p.ID, p.Endpoint, p.APIKey, timeoutMs), nil
	case "anthropic":
		return anthropic.New(p.ID, p.APIKey, ""), nil
	case "google":
		return google.New(p.ID, p.APIKey, ""), nil
	case "cohere":
		return cohere.New(p.ID, p.APIKey, ""), nil
	default:
		return nil, fmt.Errorf("unknown provider type: %s", p.Type)
	}
}
```

- [ ] **Step 2: Verify compilation**

```bash
go build ./...
```

Expected: compile errors from `main.go` calling the old `NewFromNode`/`NewFromRemote` — this is expected, will fix in Task 10.

Check factory itself compiles:

```bash
go build ./internal/provider/factory/...
```

Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add internal/provider/factory/factory.go
git commit -m "feat(factory): unify NewFromNode+NewFromRemote into single New(ProviderConfig)"
```

---

## Task 7: Router Rewrite

**Files:**
- Rewrite: `internal/router/router.go`
- Create: `internal/router/router_test.go`

- [ ] **Step 1: Write failing tests**

```go
// internal/router/router_test.go
package router_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rodrigoazlima/localrouter/internal/config"
	"github.com/rodrigoazlima/localrouter/internal/limits"
	"github.com/rodrigoazlima/localrouter/internal/metrics"
	"github.com/rodrigoazlima/localrouter/internal/provider"
	"github.com/rodrigoazlima/localrouter/internal/registry"
	"github.com/rodrigoazlima/localrouter/internal/router"
	"github.com/rodrigoazlima/localrouter/internal/state"
)

// fakeProvider is a controllable provider for tests.
type fakeProvider struct {
	id       string
	endpoint string
	failWith error
}

func (f *fakeProvider) ID() string       { return f.id }
func (f *fakeProvider) Type() string     { return "fake" }
func (f *fakeProvider) Endpoint() string { return f.endpoint }
func (f *fakeProvider) HealthCheck(_ context.Context) error { return nil }
func (f *fakeProvider) Complete(_ context.Context, req *provider.Request) (*provider.Response, error) {
	if f.failWith != nil {
		return nil, f.failWith
	}
	return &provider.Response{ID: "r1", Model: req.Model, Content: "ok"}, nil
}
func (f *fakeProvider) Stream(_ context.Context, req *provider.Request) (<-chan provider.Chunk, error) {
	if f.failWith != nil {
		return nil, f.failWith
	}
	ch := make(chan provider.Chunk, 1)
	ch <- provider.Chunk{Delta: "ok", Done: true}
	close(ch)
	return ch, nil
}

// fakeHealth makes all providers ready.
type fakeHealth struct{}

func (f *fakeHealth) IsReady(_ string) bool { return true }

func buildRouter(providers []config.ProviderConfig, defaultModel string, ps map[string]provider.Provider) *router.Router {
	reg := registry.Build(providers, defaultModel)
	h := &fakeHealth{}
	st := state.New(h)
	lim := limits.New(nil)
	m := metrics.New()
	cfg := router.Config{
		DefaultModel:    defaultModel,
		RecoveryWindows: map[string]time.Duration{},
	}
	return router.New(ps, reg, st, lim, m, cfg)
}

func TestRoute_ExplicitModel(t *testing.T) {
	fp := &fakeProvider{id: "p1", endpoint: "http://localhost"}
	providers := []config.ProviderConfig{{
		ID: "p1", Type: "ollama",
		Models: []config.ModelConfig{{ID: "llama3:latest", Priority: 1}},
	}}
	r := buildRouter(providers, "llama3:latest", map[string]provider.Provider{"p1": fp})

	resp, err := r.Route(context.Background(), &provider.Request{Model: "llama3:latest", Messages: []provider.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Model != "llama3:latest" {
		t.Errorf("want model llama3:latest, got %s", resp.Model)
	}
}

func TestRoute_AutoModel(t *testing.T) {
	// model=auto → use GlobalList → picks highest priority (p1/m1 priority=1)
	fp1 := &fakeProvider{id: "p1"}
	fp2 := &fakeProvider{id: "p2"}
	providers := []config.ProviderConfig{
		{ID: "p1", Type: "ollama", Models: []config.ModelConfig{{ID: "m1", Priority: 1}}},
		{ID: "p2", Type: "ollama", Models: []config.ModelConfig{{ID: "m2", Priority: 2}}},
	}
	r := buildRouter(providers, "m1", map[string]provider.Provider{"p1": fp1, "p2": fp2})

	resp, err := r.Route(context.Background(), &provider.Request{Model: "auto"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Model != "m1" {
		t.Errorf("auto should pick highest priority model m1, got %s", resp.Model)
	}
}

func TestRoute_EmptyModel_UsesDefault(t *testing.T) {
	fp := &fakeProvider{id: "p1"}
	providers := []config.ProviderConfig{{
		ID: "p1", Type: "ollama",
		Models: []config.ModelConfig{{ID: "default-model", Priority: 1}},
	}}
	r := buildRouter(providers, "default-model", map[string]provider.Provider{"p1": fp})

	resp, err := r.Route(context.Background(), &provider.Request{Model: ""})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Model != "default-model" {
		t.Errorf("empty model should use default, got %s", resp.Model)
	}
}

func TestRoute_UnknownModel(t *testing.T) {
	fp := &fakeProvider{id: "p1"}
	providers := []config.ProviderConfig{{
		ID: "p1", Type: "ollama",
		Models: []config.ModelConfig{{ID: "m1", Priority: 1}},
	}}
	r := buildRouter(providers, "m1", map[string]provider.Provider{"p1": fp})

	_, err := r.Route(context.Background(), &provider.Request{Model: "unknown-model"})
	if !errors.Is(err, router.ErrModelNotFound) {
		t.Errorf("want ErrModelNotFound, got %v", err)
	}
}

func TestRoute_ProviderFailover(t *testing.T) {
	// p1 fails, p2 succeeds — both support same model.
	fp1 := &fakeProvider{id: "p1", failWith: &provider.HTTPError{StatusCode: 500, Body: "error"}}
	fp2 := &fakeProvider{id: "p2"}
	providers := []config.ProviderConfig{
		{ID: "p1", Type: "ollama", Models: []config.ModelConfig{{ID: "m1", Priority: 1}}},
		{ID: "p2", Type: "ollama", Models: []config.ModelConfig{{ID: "m1", Priority: 2}}},
	}
	r := buildRouter(providers, "m1", map[string]provider.Provider{"p1": fp1, "p2": fp2})

	resp, err := r.Route(context.Background(), &provider.Request{Model: "m1"})
	if err != nil {
		t.Fatalf("want failover to p2, got error: %v", err)
	}
	_ = resp
}

func TestRoute_AllProvidersFail(t *testing.T) {
	httpErr := &provider.HTTPError{StatusCode: 500, Body: "err"}
	fp := &fakeProvider{id: "p1", failWith: httpErr}
	providers := []config.ProviderConfig{{
		ID: "p1", Type: "ollama",
		Models: []config.ModelConfig{{ID: "m1", Priority: 1}},
	}}
	r := buildRouter(providers, "m1", map[string]provider.Provider{"p1": fp})

	_, err := r.Route(context.Background(), &provider.Request{Model: "m1"})
	if !errors.Is(err, router.ErrAllProvidersFailed) {
		t.Errorf("want ErrAllProvidersFailed, got %v", err)
	}
}

func TestStream_SelectsProvider(t *testing.T) {
	fp := &fakeProvider{id: "p1"}
	providers := []config.ProviderConfig{{
		ID: "p1", Type: "ollama",
		Models: []config.ModelConfig{{ID: "m1", Priority: 1}},
	}}
	r := buildRouter(providers, "m1", map[string]provider.Provider{"p1": fp})

	ch, err := r.Stream(context.Background(), &provider.Request{Model: "m1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	chunk := <-ch
	if chunk.Delta != "ok" {
		t.Errorf("want chunk delta 'ok', got %q", chunk.Delta)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/router/... -v 2>&1 | head -30
```

Expected: compile errors — old router.New signature doesn't match.

- [ ] **Step 3: Rewrite `internal/router/router.go`**

```go
package router

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/rodrigoazlima/localrouter/internal/limits"
	"github.com/rodrigoazlima/localrouter/internal/metrics"
	"github.com/rodrigoazlima/localrouter/internal/provider"
	"github.com/rodrigoazlima/localrouter/internal/registry"
	"github.com/rodrigoazlima/localrouter/internal/reqid"
	"github.com/rodrigoazlima/localrouter/internal/state"
)

var (
	ErrModelNotFound      = errors.New("model not found or no providers configured")
	ErrAllProvidersFailed = errors.New("all providers failed or unavailable")
)

type Config struct {
	DefaultModel    string
	RecoveryWindows map[string]time.Duration // provider id → recovery_window duration
}

type Router struct {
	mu        sync.RWMutex
	providers map[string]provider.Provider
	registry  *registry.Registry
	state     *state.Manager
	limits    *limits.Tracker
	metrics   *metrics.Collector
	cfg       Config
}

func New(
	providers map[string]provider.Provider,
	reg *registry.Registry,
	st *state.Manager,
	lim *limits.Tracker,
	m *metrics.Collector,
	cfg Config,
) *Router {
	return &Router{
		providers: providers,
		registry:  reg,
		state:     st,
		limits:    lim,
		metrics:   m,
		cfg:       cfg,
	}
}

func (r *Router) Update(providers map[string]provider.Provider, reg *registry.Registry, lim *limits.Tracker, cfg Config) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers = providers
	r.registry = reg
	r.limits = lim
	r.cfg = cfg
}

func (r *Router) resolve(model string) []registry.Entry {
	r.mu.RLock()
	reg := r.registry
	defaultModel := r.cfg.DefaultModel
	r.mu.RUnlock()

	switch model {
	case "":
		return reg.ForModel(defaultModel)
	case "auto":
		return reg.GlobalList()
	default:
		return reg.ForModel(model)
	}
}

func (r *Router) selectProvider(entries []registry.Entry) (provider.Provider, string, error) {
	r.mu.RLock()
	providers := r.providers
	r.mu.RUnlock()

	for _, e := range entries {
		if r.state.GetState(e.ProviderID) != state.StateAvailable {
			continue
		}
		exhausted, resetAt := r.limits.Record(e.ProviderID)
		if exhausted {
			r.state.SetExhausted(e.ProviderID, resetAt)
			r.metrics.ProviderExhaustedEvents.Add(1)
			continue
		}
		p, ok := providers[e.ProviderID]
		if !ok {
			continue
		}
		return p, e.ModelID, nil
	}
	return nil, "", ErrAllProvidersFailed
}

func (r *Router) recoveryWindow(providerID string) time.Duration {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if d, ok := r.cfg.RecoveryWindows[providerID]; ok && d > 0 {
		return d
	}
	return time.Hour
}

func (r *Router) Route(ctx context.Context, req *provider.Request) (*provider.Response, error) {
	rid := reqid.From(ctx)
	entries := r.resolve(req.Model)
	if len(entries) == 0 {
		r.metrics.NoCapacity.Add(1)
		return nil, ErrModelNotFound
	}

	for len(entries) > 0 {
		p, modelID, err := r.selectProvider(entries)
		if err != nil {
			r.metrics.NoCapacity.Add(1)
			return nil, ErrAllProvidersFailed
		}

		reqCopy := *req
		reqCopy.Model = modelID

		resp, err := p.Complete(ctx, &reqCopy)
		if err != nil {
			log.Printf("[%s] %s failed: %v", rid, p.ID(), err)
			r.metrics.Failures.Add(1)
			r.state.Block(p.ID(), r.recoveryWindow(p.ID()))
			r.metrics.ProviderBlockEvents.Add(1)
			entries = filterProvider(entries, p.ID())
			continue
		}

		r.metrics.Requests.Add(1)
		log.Printf("[%s] → %s model=%q", rid, p.ID(), resp.Model)
		return resp, nil
	}

	r.metrics.NoCapacity.Add(1)
	return nil, ErrAllProvidersFailed
}

func (r *Router) Stream(ctx context.Context, req *provider.Request) (<-chan provider.Chunk, error) {
	rid := reqid.From(ctx)
	entries := r.resolve(req.Model)
	if len(entries) == 0 {
		r.metrics.NoCapacity.Add(1)
		return nil, ErrModelNotFound
	}

	for len(entries) > 0 {
		p, modelID, err := r.selectProvider(entries)
		if err != nil {
			r.metrics.NoCapacity.Add(1)
			return nil, ErrAllProvidersFailed
		}

		reqCopy := *req
		reqCopy.Model = modelID

		ch, err := p.Stream(ctx, &reqCopy)
		if err != nil {
			log.Printf("[%s] %s failed: %v", rid, p.ID(), err)
			r.metrics.Failures.Add(1)
			r.state.Block(p.ID(), r.recoveryWindow(p.ID()))
			r.metrics.ProviderBlockEvents.Add(1)
			entries = filterProvider(entries, p.ID())
			continue
		}

		r.metrics.Requests.Add(1)
		log.Printf("[%s] → %s model=%q stream=true", rid, p.ID(), req.Model)
		return ch, nil
	}

	r.metrics.NoCapacity.Add(1)
	return nil, ErrAllProvidersFailed
}

func filterProvider(entries []registry.Entry, providerID string) []registry.Entry {
	result := make([]registry.Entry, 0, len(entries))
	for _, e := range entries {
		if e.ProviderID != providerID {
			result = append(result, e)
		}
	}
	return result
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/router/... -v
```

Expected: all 7 tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/router/router.go internal/router/router_test.go
git commit -m "feat(router): rewrite with model-aware routing via registry+state+limits"
```

---

## Task 8: Server v2 — /models + Updated /health

**Files:**
- Rewrite: `internal/server/server.go`
- Rewrite: `internal/server/handlers.go`

- [ ] **Step 1: Rewrite `internal/server/server.go`**

```go
package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/rodrigoazlima/localrouter/internal/health"
	"github.com/rodrigoazlima/localrouter/internal/metrics"
	"github.com/rodrigoazlima/localrouter/internal/registry"
	"github.com/rodrigoazlima/localrouter/internal/router"
	"github.com/rodrigoazlima/localrouter/internal/state"
)

type Server struct {
	*http.Server
	router   *router.Router
	monitor  *health.Monitor
	state    *state.Manager
	registry *registry.Registry
	metrics  *metrics.Collector
}

func New(r *router.Router, mon *health.Monitor, st *state.Manager, reg *registry.Registry, m *metrics.Collector, addr string) *Server {
	if addr == "" {
		addr = ":8080"
	}
	s := &Server{router: r, monitor: mon, state: st, registry: reg, metrics: m}

	mux := chi.NewRouter()
	mux.Use(middleware.Recoverer)
	mux.Get("/health", s.handleHealth)
	mux.Get("/metrics", s.handleMetrics)
	mux.Get("/models", s.handleModels)
	mux.Get("/v1/models", s.handleModels)
	mux.Post("/v1/chat/completions", s.handleCompletions)

	s.Server = &http.Server{Addr: addr, Handler: mux}
	return s
}
```

- [ ] **Step 2: Rewrite `internal/server/handlers.go`**

```go
package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/rodrigoazlima/localrouter/internal/state"
)

// ---- /health ----

type healthResponse struct {
	Providers []providerHealth `json:"providers"`
}

type providerHealth struct {
	ID           string `json:"id"`
	State        string `json:"state"`
	LatencyMs    int64  `json:"latency_ms,omitempty"`
	BlockedUntil string `json:"blocked_until,omitempty"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	metricsSnap := s.metrics.Snapshot()
	providerIDs := s.registry.ProviderIDs()

	out := make([]providerHealth, 0, len(providerIDs))
	for _, id := range providerIDs {
		st := s.state.GetState(id)
		ph := providerHealth{
			ID:        id,
			State:     st.String(),
			LatencyMs: metricsSnap.Providers[id].LatencyMs,
		}
		if st == state.StateBlocked {
			bu := s.state.BlockedUntil(id)
			if !bu.IsZero() {
				ph.BlockedUntil = bu.UTC().Format(time.RFC3339)
			}
		}
		out = append(out, ph)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(healthResponse{Providers: out})
}

// ---- /metrics ----

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.metrics.Snapshot())
}

// ---- /models and /v1/models ----

type modelsResponse struct {
	Object string       `json:"object"`
	Data   []modelEntry `json:"data"`
}

type modelEntry struct {
	ID         string      `json:"id"`
	Object     string      `json:"object"`
	IsAuto     bool        `json:"is_auto,omitempty"`
	ProviderID string      `json:"provider_id,omitempty"`
	Priority   int         `json:"priority,omitempty"`
	IsFree     bool        `json:"is_free,omitempty"`
	IsDefault  bool        `json:"is_default,omitempty"`
	State      string      `json:"state,omitempty"`
	Limits     *limitsInfo `json:"limits,omitempty"`
}

type limitsInfo struct {
	Requests int    `json:"requests"`
	Window   string `json:"window"`
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	data := []modelEntry{
		{ID: "auto", Object: "model", IsAuto: true},
	}

	for _, e := range s.registry.GlobalList() {
		if s.state.GetState(e.ProviderID) != state.StateAvailable {
			continue
		}
		entry := modelEntry{
			ID:         e.ModelID,
			Object:     "model",
			ProviderID: e.ProviderID,
			Priority:   e.Priority,
			IsFree:     e.IsFree,
			IsDefault:  e.IsDefault,
			State:      "available",
		}
		data = append(data, entry)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(modelsResponse{Object: "list", Data: data})
}
```

Note: `handleCompletions` stays in `internal/server/sse.go` unchanged.

- [ ] **Step 3: Verify compilation (expect main.go errors — OK)**

```bash
go build ./internal/server/...
```

Expected: no errors in server package.

- [ ] **Step 4: Commit**

```bash
git add internal/server/server.go internal/server/handlers.go
git commit -m "feat(server): add /models endpoint, new /health shape, drop cache dependency"
```

---

## Task 9: Main.go Rewrite

**Files:**
- Rewrite: `cmd/localrouter/main.go`

- [ ] **Step 1: Rewrite `cmd/localrouter/main.go`**

```go
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/rodrigoazlima/localrouter/internal/config"
	"github.com/rodrigoazlima/localrouter/internal/health"
	"github.com/rodrigoazlima/localrouter/internal/limits"
	"github.com/rodrigoazlima/localrouter/internal/metrics"
	"github.com/rodrigoazlima/localrouter/internal/provider"
	"github.com/rodrigoazlima/localrouter/internal/provider/factory"
	"github.com/rodrigoazlima/localrouter/internal/registry"
	"github.com/rodrigoazlima/localrouter/internal/router"
	"github.com/rodrigoazlima/localrouter/internal/server"
	"github.com/rodrigoazlima/localrouter/internal/state"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config file")
	port := flag.String("port", "8080", "HTTP listen port")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	m := metrics.New()

	latency := int64(cfg.Routing.LatencyThresholdMs)
	if latency == 0 {
		latency = 2000
	}
	mon := health.New(m, latency)

	providers, limCfgs, recWindows, err := buildProviders(cfg, mon)
	if err != nil {
		log.Fatalf("build providers: %v", err)
	}

	runStartupProbes(context.Background(), providers, mon, 10000)

	reg := registry.Build(cfg.Providers, cfg.Routing.DefaultModel)
	lim := limits.New(limCfgs)
	st := state.New(mon)

	rCfg := router.Config{
		DefaultModel:    cfg.Routing.DefaultModel,
		RecoveryWindows: recWindows,
	}
	r := router.New(providers, reg, st, lim, m, rCfg)
	srv := server.New(r, mon, st, reg, m, ":"+*port)

	logAvailableProviders(cfg, st, reg)

	watcher, err := config.NewWatcher(*cfgPath, cfg, func(oldCfg, newCfg *config.Config) {
		newProviders, newLimCfgs, newRecWindows, err := buildProviders(newCfg, mon)
		if err != nil {
			log.Printf("reload: build providers: %v", err)
			return
		}

		// Sync health monitor: add new, remove old.
		oldIDs := providerIDSet(oldCfg)
		for _, p := range newCfg.Providers {
			if p.Skipped {
				continue
			}
			if !oldIDs[p.ID] {
				prov, err := factory.New(p)
				if err != nil {
					log.Printf("reload: build provider %s: %v", p.ID, err)
					continue
				}
				mon.AddNode(p.ID, prov, timeoutMs(p), 10000)
			}
		}
		newIDs := providerIDSet(newCfg)
		for _, p := range oldCfg.Providers {
			if !newIDs[p.ID] {
				mon.RemoveNode(p.ID)
			}
		}

		newReg := registry.Build(newCfg.Providers, newCfg.Routing.DefaultModel)
		newLim := limits.New(newLimCfgs)
		newRCfg := router.Config{
			DefaultModel:    newCfg.Routing.DefaultModel,
			RecoveryWindows: newRecWindows,
		}
		r.Update(newProviders, newReg, newLim, newRCfg)
		log.Printf("[RELOAD] config reloaded")
	})
	if err != nil {
		log.Fatalf("start config watcher: %v", err)
	}
	defer watcher.Stop()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("[INIT] listening on %s", srv.Addr)
		if err := srv.ListenAndServe(); err != nil {
			log.Printf("server: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down...")
	shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	srv.Shutdown(shutCtx) //nolint:errcheck
	mon.Stop()
}

func buildProviders(cfg *config.Config, mon *health.Monitor) (
	map[string]provider.Provider,
	map[string]limits.Config,
	map[string]time.Duration,
	error,
) {
	providers := make(map[string]provider.Provider, len(cfg.Providers))
	limCfgs := make(map[string]limits.Config)
	recWindows := make(map[string]time.Duration)

	for _, p := range cfg.Providers {
		if p.Skipped {
			log.Printf("[DEBUG] %s: skipped (api_key set but resolves empty)", p.ID)
			continue
		}
		prov, err := factory.New(p)
		if err != nil {
			return nil, nil, nil, err
		}
		providers[p.ID] = prov

		// Register with health monitor (idempotent: only adds if not already tracked).
		mon.AddNode(p.ID, prov, timeoutMs(p), 10000)

		if p.Limits != nil {
			limCfgs[p.ID] = limits.Config{
				Requests: p.Limits.Requests,
				Window:   p.Limits.WindowDur(),
			}
		}
		recWindows[p.ID] = p.RecoveryWindowDur()
	}
	return providers, limCfgs, recWindows, nil
}

func runStartupProbes(ctx context.Context, providers map[string]provider.Provider, mon *health.Monitor, timeoutMs int) {
	var wg sync.WaitGroup
	for _, p := range providers {
		wg.Add(1)
		go func(p provider.Provider) {
			defer wg.Done()
			pCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
			defer cancel()
			start := time.Now()
			if err := p.HealthCheck(pCtx); err != nil {
				log.Printf("[INIT] %s: probe failed: %v", p.ID(), err)
				return
			}
			mon.SetReady(p.ID())
			log.Printf("[INIT] %s: probe OK (%dms)", p.ID(), time.Since(start).Milliseconds())
		}(p)
	}
	wg.Wait()
}

func logAvailableProviders(cfg *config.Config, st *state.Manager, reg *registry.Registry) {
	for _, id := range reg.ProviderIDs() {
		s := st.GetState(id)
		var modelList string
		for i, e := range reg.ForProviderID(id) {
			if i > 0 {
				modelList += " "
			}
			modelList += e.ModelID + "(p=" + itoa(e.Priority) + ")"
		}
		log.Printf("[INIT] %s: %s — %s", id, s, modelList)
	}
	log.Printf("[INIT] default model: %s", cfg.Routing.DefaultModel)
}

func providerIDSet(cfg *config.Config) map[string]bool {
	out := make(map[string]bool, len(cfg.Providers))
	for _, p := range cfg.Providers {
		out[p.ID] = true
	}
	return out
}

func timeoutMs(p config.ProviderConfig) int {
	if p.TimeoutMs > 0 {
		return p.TimeoutMs
	}
	return 30000
}

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}
```

Add the missing `fmt` import and add `ForProviderID` to registry. Also note `health.Monitor.AddNode` currently doesn't guard against double-adding. Fix that now.

- [ ] **Step 2: Add `ForProviderID` to `internal/registry/registry.go`**

Add after `ProviderIDs()`:

```go
// ForProviderID returns all entries belonging to a specific provider, in priority order.
func (r *Registry) ForProviderID(providerID string) []Entry {
	var out []Entry
	for _, e := range r.ordered {
		if e.ProviderID == providerID {
			out = append(out, e)
		}
	}
	return out
}
```

- [ ] **Step 3: Guard `health.Monitor.AddNode` against duplicate IDs**

In `internal/health/monitor.go`, change `AddNode`:

```go
func (mon *Monitor) AddNode(id string, hc HealthChecker, timeoutMs, intervalMs int) {
	mon.mu.Lock()
	if _, exists := mon.workers[id]; exists {
		mon.mu.Unlock()
		return // already monitored
	}
	ctx, cancel := context.WithCancel(context.Background())
	mon.states[id] = NodeStatus{State: StateUnavailable}
	mon.workers[id] = &nodeWorker{cancel: cancel}
	mon.mu.Unlock()
	go mon.runNode(ctx, id, hc, timeoutMs, intervalMs)
}
```

- [ ] **Step 4: Add `fmt` import to main.go and fix `itoa`**

Replace `itoa` helper with `strconv.Itoa` and add `strconv` to imports:

```go
import (
    // ... existing imports ...
    "fmt"
    "strconv"
)

// Replace itoa function with:
func itoa(n int) string {
    return strconv.Itoa(n)
}
```

Remove the `fmt` import if only used via `itoa`. Check actual usage.

- [ ] **Step 5: Build entire project**

```bash
go build ./...
```

Expected: no errors. If `cmd/localrouter` still references deleted types (`cache`, `startup`), fix those import errors now by removing unused imports.

- [ ] **Step 6: Commit**

```bash
git add cmd/localrouter/main.go internal/registry/registry.go internal/health/monitor.go
git commit -m "feat(main): rewrite init flow — unified startup probe, provider logging, config reload"
```

---

## Task 10: Delete Old Packages

**Files:**
- Delete: `internal/cache/cache.go`
- Delete: `internal/startup/probe.go`

- [ ] **Step 1: Delete files**

```bash
git rm internal/cache/cache.go
git rm internal/startup/probe.go
```

If there's a `internal/startup/` directory with only `probe.go`, git rm removes it automatically.

- [ ] **Step 2: Check for any remaining references**

```bash
grep -r "internal/cache\|internal/startup" --include="*.go" .
```

Expected: no output. If any file still imports these packages, fix those imports (remove or replace).

- [ ] **Step 3: Build and test**

```bash
go build ./...
go test ./...
```

Expected: all pass. No reference to deleted packages.

- [ ] **Step 4: Commit**

```bash
git commit -m "chore: delete cache and startup packages (replaced by state.Manager)"
```

---

## Task 11: Migrate config.yaml

**Files:**
- Rewrite: `config.yaml`

- [ ] **Step 1: Rewrite `config.yaml` with v2 schema and free model inventory**

```yaml
version: 2

routing:
  default_model: llama3.2:latest
  latency_threshold_ms: 2000

providers:
  # --- Local nodes (priority 1-9) ---
  - id: ollama-local
    type: ollama
    endpoint: http://localhost:11434
    timeout_ms: 3000
    recovery_window: 2m
    models:
      - id: llama3.2:latest
        priority: 1
        is_free: true
      - id: qwen2.5:7b
        priority: 2
        is_free: true

  - id: lmstudio-local
    type: openai-compatible
    endpoint: http://localhost:1234
    timeout_ms: 3000
    recovery_window: 2m
    models:
      - id: llama3.2:latest
        priority: 3
        is_free: true

  - id: vllm-local
    type: openai-compatible
    endpoint: http://localhost:8000
    api_key: ${VLLM_KEY}
    timeout_ms: 3000
    recovery_window: 2m
    models:
      - id: llama3.2:latest
        priority: 4
        is_free: true

  # --- Remote: free aggregators (priority 10-29) ---
  - id: openrouter-1
    type: openai-compatible
    endpoint: https://openrouter.ai/api/v1
    api_key: ${OPENROUTER_API_KEY}
    limits:
      requests: 200
      window: 1h
    recovery_window: 15m
    models:
      - id: meta-llama/llama-3.2-3b-instruct:free
        priority: 10
        is_free: true
      - id: meta-llama/llama-3.3-70b-instruct:free
        priority: 11
        is_free: true
      - id: deepseek/deepseek-r1:free
        priority: 12
        is_free: true
      - id: google/gemini-2.0-flash-exp:free
        priority: 13
        is_free: true
      - id: moonshotai/kimi-k2:free
        priority: 14
        is_free: true

  # --- Remote: high-RPM free inference (priority 20-39) ---
  - id: groq-1
    type: openai-compatible
    endpoint: https://api.groq.com/openai/v1
    api_key: ${GROQ_API_KEY}
    limits:
      requests: 100
      window: 1m
    recovery_window: 10m
    models:
      - id: llama-3.1-8b-instant
        priority: 20
        is_free: true
      - id: llama-3.3-70b-versatile
        priority: 21
        is_free: true

  - id: nvidia-1
    type: openai-compatible
    endpoint: https://integrate.api.nvidia.com/v1
    api_key: ${NVIDIA_API_KEY}
    limits:
      requests: 100
      window: 1m
    recovery_window: 10m
    models:
      - id: meta/llama-3.1-8b-instruct
        priority: 22
        is_free: true
      - id: meta/llama-3.3-70b-instruct
        priority: 23
        is_free: true

  - id: github-models-1
    type: openai-compatible
    endpoint: https://models.inference.ai.azure.com
    api_key: ${GITHUB_TOKEN}
    limits:
      requests: 50
      window: 1m
    recovery_window: 15m
    models:
      - id: gpt-4o
        priority: 24
        is_free: true
      - id: meta-llama-3-70b-instruct
        priority: 25
        is_free: true

  # --- Remote: rate-limited direct APIs (priority 30-49) ---
  - id: mistral-1
    type: openai-compatible
    endpoint: https://api.mistral.ai/v1
    api_key: ${MISTRAL_API_KEY}
    limits:
      requests: 10
      window: 1m
    recovery_window: 15m
    models:
      - id: mistral-small-latest
        priority: 30
        is_free: true
      - id: open-mistral-nemo
        priority: 31
        is_free: true

  - id: google-1
    type: google
    api_key: ${GOOGLE_API_KEY}
    limits:
      requests: 10
      window: 1m
    recovery_window: 15m
    models:
      - id: gemini-2.0-flash
        priority: 32
        is_free: true
      - id: gemini-1.5-flash
        priority: 33
        is_free: true

  - id: cohere-1
    type: cohere
    api_key: ${COHERE_API_KEY}
    limits:
      requests: 20
      window: 1m
    recovery_window: 15m
    models:
      - id: command-r
        priority: 34
        is_free: true
      - id: command-r-plus
        priority: 35
        is_free: true

  - id: zhipu-1
    type: openai-compatible
    endpoint: https://open.bigmodel.cn/api/paas/v4
    api_key: ${ZHIPU_API_KEY}
    limits:
      requests: 20
      window: 1m
    recovery_window: 15m
    models:
      - id: glm-4-flash
        priority: 36
        is_free: true

  # --- Paid providers (uncomment to enable, set priority as needed) ---
  # - id: openai-1
  #   type: openai-compatible
  #   endpoint: https://api.openai.com/v1
  #   api_key: ${OPENAI_KEY}
  #   recovery_window: 10m
  #   models:
  #     - id: gpt-4o-mini
  #       priority: 50
  #       is_free: false
  #     - id: gpt-4o
  #       priority: 51
  #       is_free: false

  # - id: anthropic-1
  #   type: anthropic
  #   api_key: ${ANTHROPIC_KEY}
  #   recovery_window: 10m
  #   models:
  #     - id: claude-3-5-haiku-20241022
  #       priority: 52
  #       is_free: false

  # - id: deepseek-1
  #   type: openai-compatible
  #   endpoint: https://api.deepseek.com/v1
  #   api_key: ${DEEPSEEK_KEY}
  #   recovery_window: 10m
  #   models:
  #     - id: deepseek-chat
  #       priority: 53
  #       is_free: false
```

- [ ] **Step 2: Validate config loads**

```bash
go run ./cmd/localrouter -config config.yaml -port 18080 &
sleep 2
curl -s http://localhost:18080/models | head -20
kill %1
```

Expected: server starts, /models returns JSON with `auto` and any providers where api_keys are set.

- [ ] **Step 3: Commit**

```bash
git add config.yaml
git commit -m "config: migrate to v2 schema with model priorities and free model inventory"
```

---

## Task 12: End-to-End Verification

- [ ] **Step 1: Run full test suite**

```bash
go test ./... -v 2>&1 | tail -30
```

Expected: all tests pass, no compilation errors.

- [ ] **Step 2: Run with a real provider (if available)**

If Ollama is running locally with llama3.2:latest pulled:

```bash
go run ./cmd/localrouter -config config.yaml
```

In another terminal:

```bash
# Check models
curl -s http://localhost:8080/models | jq '.data[].id'

# Route with explicit model
curl -s -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"llama3.2:latest","messages":[{"role":"user","content":"Say hi in one word"}]}' | jq .

# Route with auto
curl -s -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"auto","messages":[{"role":"user","content":"Say hi in one word"}]}' | jq .

# Route with no model (uses default)
curl -s -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"messages":[{"role":"user","content":"Say hi in one word"}]}' | jq .

# Check health
curl -s http://localhost:8080/health | jq .

# Check metrics
curl -s http://localhost:8080/metrics | jq .
```

Expected:
- `/models` lists `auto` + all models where provider is available
- Explicit model routes to correct provider
- `auto` routes to highest-priority available model
- Empty model routes to `routing.default_model`
- `/health` shows `{"providers": [...]}`
- `/metrics` shows `requests`, `failures`, no `tier1_failures`/`local_requests`

- [ ] **Step 3: Final commit**

```bash
git add -A
git status  # verify nothing unexpected
git commit -m "feat: complete routing refactor — priority+limits system, unified providers"
```

---

## Self-Review Notes

**Spec coverage check:**
- ✅ Sec 1: Routing model — model-first + auto routes in Task 7
- ✅ Sec 2: Priority system — registry sort by (priority, providerID, modelID) in Task 3
- ✅ Sec 3: Limits system — fixed-window tracker in Task 2, wired in Task 7+9
- ✅ Sec 4: Provider state machine — state.Manager in Task 4
- ✅ Sec 5: Config schema v2 — Task 1
- ✅ Sec 6: Default model — routing.default_model, wired in router.resolve()
- ✅ Sec 7: /models endpoint — Task 8
- ✅ Sec 8: Startup logging — Task 9 logAvailableProviders()
- ✅ Sec 9: Free models — Task 11 config.yaml
- ✅ Sec 10: Compatibility — /v1/chat/completions and SSE unchanged; /health and /metrics preserved with extended shapes

**Type consistency across tasks:**
- `limits.Config{Requests, Window}` — defined Task 2, used Task 9 ✅
- `registry.Entry{ProviderID, ModelID, Priority, IsFree, IsDefault}` — defined Task 3, used Tasks 7, 8 ✅
- `state.StateAvailable/Unhealthy/Exhausted/Blocked` — defined Task 4, used Tasks 7, 8 ✅
- `router.Config{DefaultModel, RecoveryWindows}` — defined Task 7, used Task 9 ✅
- `metrics.Collector.Requests/Failures` (not LocalRequests/Tier1Failures) — defined Task 5, used Task 7 ✅
- `server.New(r, mon, st, reg, m, addr)` — defined Task 8, used Task 9 ✅
- `factory.New(ProviderConfig)` — defined Task 6, used Task 9 ✅
- `registry.ForProviderID(id)` — added Task 9 step 2, used in `logAvailableProviders` ✅
