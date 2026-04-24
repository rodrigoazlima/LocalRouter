package registry_test

import (
	"testing"

	"github.com/rodrigoazlima/localrouter/internal/config"
	"github.com/rodrigoazlima/localrouter/internal/registry"
)

func makeProviders() []config.ProviderConfig {
	return []config.ProviderConfig{
		{
			ID:   "zzz-provider",
			Type: "ollama",
			Models: []config.ModelConfig{
				{ID: "llama3:latest", Priority: 3, IsFree: true},
				{ID: "qwen:7b", Priority: 1, IsFree: true},
			},
		},
		{
			ID:   "aaa-provider",
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
	// Expected order by (priority ASC, providerID ASC, modelID ASC):
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
			t.Errorf("gpt-4o-mini at %s should have IsDefault=true", e.ProviderID)
		}
		if e.ModelID != "gpt-4o-mini" && e.IsDefault {
			t.Errorf("model %s at %s should not be default", e.ModelID, e.ProviderID)
		}
	}
}

func TestBuild_ForModel(t *testing.T) {
	reg := registry.Build(makeProviders(), "qwen:7b")
	entries := reg.ForModel("llama3:latest")
	if len(entries) != 2 {
		t.Fatalf("want 2 entries for llama3:latest, got %d", len(entries))
	}
	// zzz-provider priority=3, aaa-provider priority=5 → zzz first
	if entries[0].ProviderID != "zzz-provider" {
		t.Errorf("want zzz-provider first, got %s", entries[0].ProviderID)
	}
}

func TestBuild_ForModel_Unknown(t *testing.T) {
	reg := registry.Build(makeProviders(), "qwen:7b")
	if entries := reg.ForModel("does-not-exist"); len(entries) != 0 {
		t.Errorf("want nil for unknown model, got %v", entries)
	}
}

func TestBuild_SkipsSkippedProviders(t *testing.T) {
	providers := []config.ProviderConfig{
		{ID: "active", Type: "ollama", Skipped: false, Models: []config.ModelConfig{{ID: "m1", Priority: 1}}},
		{ID: "skipped", Type: "ollama", Skipped: true, Models: []config.ModelConfig{{ID: "m1", Priority: 2}}},
	}
	reg := registry.Build(providers, "m1")
	for _, e := range reg.GlobalList() {
		if e.ProviderID == "skipped" {
			t.Error("skipped provider should not appear in registry")
		}
	}
}

func TestBuild_ProviderIDs(t *testing.T) {
	reg := registry.Build(makeProviders(), "qwen:7b")
	ids := reg.ProviderIDs()
	if len(ids) != 2 {
		t.Fatalf("want 2 provider IDs, got %d", len(ids))
	}
	if ids[0] != "zzz-provider" || ids[1] != "aaa-provider" {
		t.Errorf("want insertion order [zzz-provider aaa-provider], got %v", ids)
	}
}

func TestBuild_ForProviderID(t *testing.T) {
	reg := registry.Build(makeProviders(), "qwen:7b")
	entries := reg.ForProviderID("zzz-provider")
	if len(entries) != 2 {
		t.Fatalf("want 2 entries for zzz-provider, got %d", len(entries))
	}
	// zzz-provider has qwen:7b(p=1) and llama3:latest(p=3) → qwen first
	if entries[0].ModelID != "qwen:7b" {
		t.Errorf("want qwen:7b first, got %s", entries[0].ModelID)
	}
}

func TestBuild_ForProviderID_Unknown(t *testing.T) {
	reg := registry.Build(makeProviders(), "qwen:7b")
	if entries := reg.ForProviderID("unknown"); len(entries) != 0 {
		t.Errorf("want empty for unknown provider, got %v", entries)
	}
}
