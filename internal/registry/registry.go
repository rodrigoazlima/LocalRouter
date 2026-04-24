package registry

import (
	"sort"

	"github.com/rodrigoazlima/localrouter/internal/config"
)

// Entry represents a single (provider, model) pair in the registry.
type Entry struct {
	ProviderID string
	ModelID    string
	Priority   int
	IsFree     bool
	IsDefault  bool
}

// Registry is a sorted index of all (provider, model) pairs.
type Registry struct {
	// all holds the fully sorted global list.
	all []Entry
	// byModel maps model ID → sorted entries for that model.
	byModel map[string][]Entry
	// byProvider maps provider ID → sorted entries for that provider.
	byProvider map[string][]Entry
	// providerOrder preserves insertion order of non-skipped provider IDs.
	providerOrder []string
}

// Build creates a Registry from provider configs. Providers with Skipped=true
// are excluded. defaultModel is the model ID that should have IsDefault=true
// on its entries.
func Build(providers []config.ProviderConfig, defaultModel string) *Registry {
	var all []Entry

	// Track insertion order for non-skipped providers.
	seenProviders := make(map[string]bool)
	var providerOrder []string

	for _, p := range providers {
		if p.Skipped {
			continue
		}
		if !seenProviders[p.ID] {
			seenProviders[p.ID] = true
			providerOrder = append(providerOrder, p.ID)
		}
		for _, m := range p.Models {
			e := Entry{
				ProviderID: p.ID,
				ModelID:    m.ID,
				Priority:   m.Priority,
				IsFree:     m.IsFree,
				IsDefault:  m.ID == defaultModel,
			}
			all = append(all, e)
		}
	}

	// Sort globally by (Priority ASC, ProviderID ASC, ModelID ASC).
	sort.Slice(all, func(i, j int) bool {
		return entryLess(all[i], all[j])
	})

	// Build per-model index — reuse the already-sorted global list order.
	byModel := make(map[string][]Entry)
	for _, e := range all {
		byModel[e.ModelID] = append(byModel[e.ModelID], e)
	}

	// Build per-provider index — each slice is already in global sort order
	// (which is priority ASC within a provider, then model ID ASC for ties).
	byProvider := make(map[string][]Entry)
	for _, e := range all {
		byProvider[e.ProviderID] = append(byProvider[e.ProviderID], e)
	}

	return &Registry{
		all:           all,
		byModel:       byModel,
		byProvider:    byProvider,
		providerOrder: providerOrder,
	}
}

// entryLess defines the canonical sort order: Priority ASC, ProviderID ASC, ModelID ASC.
func entryLess(a, b Entry) bool {
	if a.Priority != b.Priority {
		return a.Priority < b.Priority
	}
	if a.ProviderID != b.ProviderID {
		return a.ProviderID < b.ProviderID
	}
	return a.ModelID < b.ModelID
}

// GlobalList returns all entries sorted by (Priority ASC, ProviderID ASC, ModelID ASC).
// Used for model=auto routing.
func (r *Registry) GlobalList() []Entry {
	out := make([]Entry, len(r.all))
	copy(out, r.all)
	return out
}

// ForModel returns entries for a specific model ID, sorted by the canonical order.
// Returns nil if model is unknown.
func (r *Registry) ForModel(id string) []Entry {
	entries, ok := r.byModel[id]
	if !ok {
		return nil
	}
	out := make([]Entry, len(entries))
	copy(out, entries)
	return out
}

// ProviderIDs returns unique provider IDs in insertion order (non-skipped only).
func (r *Registry) ProviderIDs() []string {
	out := make([]string, len(r.providerOrder))
	copy(out, r.providerOrder)
	return out
}

// ForProviderID returns all entries for a specific provider, sorted by (Priority ASC, ModelID ASC). Returns nil if unknown.
func (r *Registry) ForProviderID(providerID string) []Entry {
	entries, ok := r.byProvider[providerID]
	if !ok {
		return nil
	}
	out := make([]Entry, len(entries))
	copy(out, entries)
	return out
}
