package config_test

import (
	"os"
	"testing"
	"time"

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
  default_model: gpt-4o
  latency_threshold_ms: 2000
providers:
  - id: openai-1
    type: openai-compatible
    endpoint: https://api.openai.com/v1
    api_key: sk-test
    timeout_ms: 30000
    recovery_window: "10m"
    limits:
      requests: 100
      window: "1m"
    models:
      - id: gpt-4o
        priority: 1
        is_free: false
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Version != 2 {
		t.Fatalf("expected version 2, got %d", cfg.Version)
	}
	if cfg.Routing.DefaultModel != "gpt-4o" {
		t.Fatalf("expected default_model gpt-4o, got %q", cfg.Routing.DefaultModel)
	}
	if cfg.Routing.LatencyThresholdMs != 2000 {
		t.Fatalf("expected latency_threshold_ms 2000, got %d", cfg.Routing.LatencyThresholdMs)
	}
	if len(cfg.Providers) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(cfg.Providers))
	}
	p := cfg.Providers[0]
	if p.ID != "openai-1" {
		t.Fatalf("expected id openai-1, got %q", p.ID)
	}
	if p.Type != "openai-compatible" {
		t.Fatalf("expected type openai-compatible, got %q", p.Type)
	}
	if p.APIKey != "sk-test" {
		t.Fatalf("expected api_key sk-test, got %q", p.APIKey)
	}
	if p.TimeoutMs != 30000 {
		t.Fatalf("expected timeout_ms 30000, got %d", p.TimeoutMs)
	}
	if p.Limits == nil {
		t.Fatal("expected non-nil limits")
	}
	if p.Limits.Requests != 100 {
		t.Fatalf("expected limits.requests 100, got %d", p.Limits.Requests)
	}
	if p.Limits.Window != "1m" {
		t.Fatalf("expected limits.window 1m, got %q", p.Limits.Window)
	}
	if len(p.Models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(p.Models))
	}
	m := p.Models[0]
	if m.ID != "gpt-4o" {
		t.Fatalf("expected model id gpt-4o, got %q", m.ID)
	}
	if m.Priority != 1 {
		t.Fatalf("expected priority 1, got %d", m.Priority)
	}
}

func TestLoad_WrongVersion(t *testing.T) {
	path := writeConfig(t, `
version: 1
routing:
  default_model: gpt-4o
providers:
  - id: openai-1
    type: openai-compatible
    endpoint: https://api.openai.com/v1
    models:
      - id: gpt-4o
        priority: 1
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
  default_model: gpt-4o
providers:
  - id: dup
    type: openai-compatible
    endpoint: https://api.openai.com/v1
    models:
      - id: gpt-4o
        priority: 1
  - id: dup
    type: anthropic
    models:
      - id: gpt-4o
        priority: 1
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for duplicate provider id")
	}
}

func TestLoad_MissingPriority(t *testing.T) {
	path := writeConfig(t, `
version: 2
routing:
  default_model: gpt-4o
providers:
  - id: openai-1
    type: openai-compatible
    endpoint: https://api.openai.com/v1
    models:
      - id: gpt-4o
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Providers[0].Models[0].Priority != 1 {
		t.Fatalf("expected auto-filled priority=1, got %d", cfg.Providers[0].Models[0].Priority)
	}
}

func TestLoad_DefaultModelMissing(t *testing.T) {
	path := writeConfig(t, `
version: 2
routing:
  default_model: nonexistent-model
providers:
  - id: openai-1
    type: openai-compatible
    endpoint: https://api.openai.com/v1
    models:
      - id: gpt-4o
        priority: 1
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error when default_model not found in any provider")
	}
}

func TestLoad_SkipsProviderWithEmptyAPIKey(t *testing.T) {
	// Make sure UNSET_VAR_XYZ is not set
	os.Unsetenv("UNSET_VAR_XYZ")

	// The default_model must exist in at least one non-skipped provider.
	// We add an ollama provider (no api_key needed) to satisfy that requirement.
	path := writeConfig(t, `
version: 2
routing:
  default_model: llama3
providers:
  - id: skipped-provider
    type: anthropic
    api_key: ${UNSET_VAR_XYZ}
    models:
      - id: claude-3
        priority: 1
  - id: local-ollama
    type: ollama
    endpoint: http://localhost:11434
    models:
      - id: llama3
        priority: 1
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Providers) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(cfg.Providers))
	}
	// Find skipped-provider
	var found bool
	for _, p := range cfg.Providers {
		if p.ID == "skipped-provider" {
			found = true
			if !p.Skipped {
				t.Fatal("expected skipped-provider to have Skipped=true")
			}
			if p.APIKey != "" {
				t.Fatalf("expected empty api_key after expansion, got %q", p.APIKey)
			}
		}
	}
	if !found {
		t.Fatal("skipped-provider not found in config")
	}
}

func TestLoad_LimitsWindow(t *testing.T) {
	path := writeConfig(t, `
version: 2
routing:
  default_model: gpt-4o
providers:
  - id: openai-1
    type: openai-compatible
    endpoint: https://api.openai.com/v1
    limits:
      requests: 60
      window: "1m"
    models:
      - id: gpt-4o
        priority: 1
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	p := cfg.Providers[0]
	if p.Limits == nil {
		t.Fatal("expected non-nil limits")
	}
	if p.Limits.WindowDur() != time.Minute {
		t.Fatalf("expected WindowDur() == time.Minute, got %v", p.Limits.WindowDur())
	}
}

func TestLoad_EnvExpansion(t *testing.T) {
	t.Setenv("MY_KEY", "sk-expanded-value")
	path := writeConfig(t, `
version: 2
routing:
  default_model: gpt-4o
providers:
  - id: openai-1
    type: openai-compatible
    endpoint: https://api.openai.com/v1
    api_key: ${MY_KEY}
    models:
      - id: gpt-4o
        priority: 1
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Providers[0].APIKey != "sk-expanded-value" {
		t.Fatalf("expected expanded api_key, got %q", cfg.Providers[0].APIKey)
	}
	// Skipped must be false since env var was set
	if cfg.Providers[0].Skipped {
		t.Fatal("expected Skipped=false when env var is set")
	}
}

func TestLoad_RecoveryWindowDur(t *testing.T) {
	path := writeConfig(t, `
version: 2
routing:
  default_model: gpt-4o
providers:
  - id: openai-1
    type: openai-compatible
    endpoint: https://api.openai.com/v1
    recovery_window: "5m"
    models:
      - id: gpt-4o
        priority: 1
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	p := cfg.Providers[0]
	if p.RecoveryWindowDur() != 5*time.Minute {
		t.Fatalf("expected RecoveryWindowDur() == 5m, got %v", p.RecoveryWindowDur())
	}
}

func TestLoad_RecoveryWindowDefault(t *testing.T) {
	path := writeConfig(t, `
version: 2
routing:
  default_model: gpt-4o
providers:
  - id: openai-1
    type: openai-compatible
    endpoint: https://api.openai.com/v1
    models:
      - id: gpt-4o
        priority: 1
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	p := cfg.Providers[0]
	if p.RecoveryWindowDur() != time.Hour {
		t.Fatalf("expected RecoveryWindowDur() == 1h (default), got %v", p.RecoveryWindowDur())
	}
}

func TestLoad_LimitsNoWindow(t *testing.T) {
	path := writeConfig(t, `
version: 2
routing:
  default_model: gpt-4o
providers:
  - id: openai-1
    type: openai-compatible
    endpoint: https://api.openai.com/v1
    limits:
      requests: 60
    models:
      - id: gpt-4o
        priority: 1
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error when limits block is set but window is missing")
	}
}
