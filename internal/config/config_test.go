package config_test

import (
	"os"
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

func TestLoad_ValidConfig(t *testing.T) {
	path := writeConfig(t, `
local:
  nodes:
    - id: ollama-1
      type: ollama
      endpoint: http://localhost:11434
      timeout_ms: 3000
remote:
  providers:
    - id: openai-1
      type: openai-compatible
      endpoint: https://api.openai.com
      api_key: sk-test
routing:
  latency_threshold_ms: 2000
  fallback_enabled: true
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Local.Nodes) != 1 || cfg.Local.Nodes[0].ID != "ollama-1" {
		t.Fatalf("unexpected nodes: %+v", cfg.Local.Nodes)
	}
	if len(cfg.Remote.Providers) != 1 || cfg.Remote.Providers[0].ID != "openai-1" {
		t.Fatalf("unexpected providers: %+v", cfg.Remote.Providers)
	}
}

func TestLoad_EnvExpansion(t *testing.T) {
	t.Setenv("TEST_KEY", "sk-expanded")
	path := writeConfig(t, `
local:
  nodes: []
remote:
  providers:
    - id: openai-1
      type: openai-compatible
      endpoint: https://api.openai.com
      api_key: ${TEST_KEY}
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Remote.Providers[0].APIKey != "sk-expanded" {
		t.Fatalf("env not expanded: %s", cfg.Remote.Providers[0].APIKey)
	}
}

func TestLoad_DuplicateID_Error(t *testing.T) {
	path := writeConfig(t, `
local:
  nodes:
    - id: dup
      type: ollama
      endpoint: http://localhost:11434
remote:
  providers:
    - id: dup
      type: openai-compatible
      endpoint: https://api.openai.com
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for duplicate id")
	}
}

func TestLoad_UnknownType_Error(t *testing.T) {
	path := writeConfig(t, `
local:
  nodes:
    - id: n1
      type: unknown
      endpoint: http://localhost:11434
remote:
  providers: []
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for unknown type")
	}
}

func TestLoad_MissingEndpoint_Error(t *testing.T) {
	path := writeConfig(t, `
local:
  nodes:
    - id: n1
      type: ollama
remote:
  providers: []
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for missing endpoint")
	}
}

func TestNodeConfig_Redacted_DoesNotMutate(t *testing.T) {
	n := config.NodeConfig{ID: "n1", APIKey: "secret"}
	r := n.Redacted()
	if r.APIKey != "[REDACTED]" {
		t.Fatalf("expected [REDACTED], got %s", r.APIKey)
	}
	if n.APIKey != "secret" {
		t.Fatal("Redacted must not modify original")
	}
}
