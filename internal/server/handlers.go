// internal/server/handlers.go
package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/rodrigoazlima/localrouter/internal/health"
	"github.com/rodrigoazlima/localrouter/internal/state"
)

// ---- /health ----

type healthResponse struct {
	Local  localHealthInfo  `json:"local"`
	Remote []remoteHealthInfo `json:"remote"`
}

type localHealthInfo struct {
	Status string           `json:"status"`
	Nodes  []localNodeInfo  `json:"nodes"`
}

type localNodeInfo struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	LatencyMs int64  `json:"latency_ms"`
}

type remoteHealthInfo struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	TTLRemaining *int64 `json:"ttl_remaining,omitempty"`
}

func nodeStateString(s health.NodeState) string {
	switch s {
	case health.StateReady:
		return "ready"
	case health.StateDegraded:
		return "degraded"
	default:
		return "unavailable"
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	monSnap := s.monitor.Snapshot()

	// Build local node info.
	localNodes := make([]localNodeInfo, 0)
	for _, id := range s.registry.LocalIDs() {
		ns := monSnap[id]
		localNodes = append(localNodes, localNodeInfo{
			ID:        id,
			Status:    nodeStateString(ns.State),
			LatencyMs: ns.LatencyMs,
		})
	}

	// Compute overall local status.
	localStatus := "unavailable"
	for _, n := range localNodes {
		if n.Status == "ready" {
			localStatus = "healthy"
			break
		} else if n.Status == "degraded" {
			localStatus = "degraded"
		}
	}
	if len(localNodes) == 0 {
		localStatus = "healthy"
	}

	// Build remote provider list — only include blocked providers.
	remoteProviders := make([]remoteHealthInfo, 0)
	for _, id := range s.registry.RemoteIDs() {
		st := s.state.GetState(id)
		if st != state.StateBlocked {
			continue
		}
		bu := s.state.BlockedUntil(id)
		ttl := int64(time.Until(bu).Seconds())
		if ttl < 0 {
			ttl = 0
		}
		remoteProviders = append(remoteProviders, remoteHealthInfo{
			ID:           id,
			Status:       "blocked",
			TTLRemaining: &ttl,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(healthResponse{
		Local:  localHealthInfo{Status: localStatus, Nodes: localNodes},
		Remote: remoteProviders,
	})
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
