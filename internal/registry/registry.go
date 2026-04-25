package registry

import (
	"sort"

	"github.com/rodrigoazlima/localrouter/internal/config"
)

// Entry represents a single (provider, model) pair in the registry.
type Entry struct {
	ProviderID  string
	ModelID     string
	Priority    int
	IsFree      bool
	IsDefault   bool
	IsRemote    bool
	Temperature *float64
	TopP        *float64
	MaxTokens   *int
	Seed        *int
}

// anyModelEntry tracks a provider that accepts any model (configured without an explicit model list).
type anyModelEntry struct {
	ProviderID string
	Priority   int
	IsFree     bool
	IsRemote   bool
}

// Registry is a sorted index of all (provider, model) pairs.
type Registry struct {
	// all holds the fully sorted global list of explicit (provider, model) entries.
	all []Entry
	// byModel maps model ID → sorted entries for that model.
	byModel map[string][]Entry
	// byProvider maps provider ID → sorted entries for that provider.
	byProvider map[string][]Entry
	// providerOrder preserves insertion order of non-skipped provider IDs.
	providerOrder []string
	// localIDs holds provider IDs that are local (in insertion order).
	localIDs []string
	// remoteIDs holds provider IDs that are remote (in insertion order).
	remoteIDs []string
	// anyModel holds providers with no explicit model list, sorted by priority.
	anyModel []anyModelEntry
}

// Build creates a Registry from provider configs. Providers with Skipped=true
// are excluded. defaultModel is the model ID that should have IsDefault=true
// on its entries.
func Build(providers []config.ProviderConfig, defaultModel string) *Registry {
	var all []Entry
	var anyModel []anyModelEntry

	seenProviders := make(map[string]bool)
	var providerOrder []string
	var localIDs []string
	var remoteIDs []string

	for _, p := range providers {
		if p.Skipped {
			continue
		}
		if !seenProviders[p.ID] {
			seenProviders[p.ID] = true
			providerOrder = append(providerOrder, p.ID)
			if p.IsRemote {
				remoteIDs = append(remoteIDs, p.ID)
			} else {
				localIDs = append(localIDs, p.ID)
			}
		}

		if len(p.Models) == 0 {
			// Provider has no explicit model list — it serves any model.
			priority := len(anyModel) + len(all) + 1
			anyModel = append(anyModel, anyModelEntry{
				ProviderID: p.ID,
				Priority:   priority,
				IsRemote:   p.IsRemote,
			})
		} else {
			for _, m := range p.Models {
				e := Entry{
					ProviderID:  p.ID,
					ModelID:     m.ID,
					Priority:    m.Priority,
					IsFree:      m.IsFree,
					IsDefault:   m.ID == defaultModel,
					IsRemote:    p.IsRemote,
					Temperature: m.Temperature,
					TopP:        m.TopP,
					MaxTokens:   m.MaxTokens,
					Seed:        m.Seed,
				}
				all = append(all, e)
			}
		}
	}

	// Sort explicit entries globally by (Priority ASC, ProviderID ASC, ModelID ASC).
	sort.Slice(all, func(i, j int) bool {
		return entryLess(all[i], all[j])
	})

	// Sort any-model entries by priority.
	sort.Slice(anyModel, func(i, j int) bool {
		return anyModel[i].Priority < anyModel[j].Priority
	})

	// Build per-model index from explicit entries.
	byModel := make(map[string][]Entry)
	for _, e := range all {
		byModel[e.ModelID] = append(byModel[e.ModelID], e)
	}

	// Build per-provider index from explicit entries.
	byProvider := make(map[string][]Entry)
	for _, e := range all {
		byProvider[e.ProviderID] = append(byProvider[e.ProviderID], e)
	}

	return &Registry{
		all:           all,
		byModel:       byModel,
		byProvider:    byProvider,
		providerOrder: providerOrder,
		localIDs:      localIDs,
		remoteIDs:     remoteIDs,
		anyModel:      anyModel,
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

// GlobalList returns all explicit entries sorted by (Priority ASC, ProviderID ASC, ModelID ASC).
// Used for model=auto routing.
func (r *Registry) GlobalList() []Entry {
	out := make([]Entry, len(r.all))
	copy(out, r.all)
	return out
}

// ForModel returns entries for a specific model ID.
// Returns explicit entries for that model plus any-model (wildcard) providers.
// Returns nil if neither explicit entries nor any-model providers exist.
func (r *Registry) ForModel(id string) []Entry {
	explicit := r.byModel[id]
	if len(explicit) == 0 && len(r.anyModel) == 0 {
		return nil
	}

	// Build result: explicit entries first, then any-model providers with the requested model ID.
	result := make([]Entry, 0, len(explicit)+len(r.anyModel))
	result = append(result, explicit...)
	for _, a := range r.anyModel {
		result = append(result, Entry{
			ProviderID: a.ProviderID,
			ModelID:    id,
			Priority:   a.Priority,
			IsRemote:   a.IsRemote,
		})
	}

	// Re-sort merged result.
	sort.Slice(result, func(i, j int) bool {
		return entryLess(result[i], result[j])
	})
	return result
}

// ProviderIDs returns unique provider IDs in insertion order (non-skipped only).
func (r *Registry) ProviderIDs() []string {
	out := make([]string, len(r.providerOrder))
	copy(out, r.providerOrder)
	return out
}

// LocalIDs returns IDs of local providers in insertion order.
func (r *Registry) LocalIDs() []string {
	out := make([]string, len(r.localIDs))
	copy(out, r.localIDs)
	return out
}

// RemoteIDs returns IDs of remote providers in insertion order.
func (r *Registry) RemoteIDs() []string {
	out := make([]string, len(r.remoteIDs))
	copy(out, r.remoteIDs)
	return out
}

// ForProviderID returns all entries for a specific provider, sorted by (Priority ASC, ModelID ASC).
// Returns nil if unknown.
func (r *Registry) ForProviderID(providerID string) []Entry {
	entries, ok := r.byProvider[providerID]
	if !ok {
		return nil
	}
	out := make([]Entry, len(entries))
	copy(out, entries)
	return out
}
