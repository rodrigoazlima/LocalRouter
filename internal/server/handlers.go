// internal/server/handlers.go
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
