// internal/server/handlers.go - HTTP handlers for the server
package server

import (
	"bytes"
	"encoding/json"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/rodrigoazlima/localrouter/internal/state"
)

// ReadTemplate reads the HTML template from file
func ReadTemplate() (*template.Template, error) {
	cwd, _ := os.Getwd()
	tmplPath := filepath.Join(cwd, "templates", "report.html")
	return template.ParseFiles(tmplPath)
}

type ReportData struct {
	Global                   state.GlobalState
	HealthyProviders         []state.ProviderState
	DegradedBlocked          []state.ProviderState
	UnreachableMisconfigured []state.ProviderState
	AllProviders             []state.ProviderState
}

type modelEntry struct {
	ID       string `json:"id"`
	Object   string `json:"object"`
	Priority int    `json:"priority,omitempty"`
	IsFree   bool   `json:"is_free,omitempty"`
	State    string `json:"state,omitempty"`
}

type modelsResponse struct {
	Object string       `json:"object"`
	Data   []modelEntry `json:"data"`
}

type modelLimitWindow struct {
	Requests int        `json:"requests"`
	Window   string     `json:"window"`
	Used     int        `json:"used,omitempty"`
	ResetAt  *time.Time `json:"reset_at,omitempty"`
}

type modelHealthEntry struct {
	ID           string              `json:"id"`
	Priority     int                 `json:"priority,omitempty"`
	State        string              `json:"state"`
	BlockedUntil *time.Time          `json:"blocked_until,omitempty"`
	Limits       []modelLimitWindow  `json:"limits,omitempty"`
}

type healthProviderEntry struct {
	ID               string             `json:"id"`
	IsRemote         bool               `json:"is_remote"`
	State            string             `json:"state"`
	LatencyMs        int64              `json:"latency_ms,omitempty"`
	BlockedUntil     *time.Time         `json:"blocked_until,omitempty"`
	ConcurrentActive int64              `json:"concurrent_active,omitempty"`
	ConcurrentLimit  int64              `json:"concurrent_limit,omitempty"`
	Models           []modelHealthEntry `json:"models,omitempty"`
}

type healthResponse struct {
	Status    string                `json:"status"`
	Providers []healthProviderEntry `json:"providers"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	snap := s.metrics.Snapshot()
	providerIDs := s.registry.ProviderIDs()

	remoteIDSet := make(map[string]bool, len(s.registry.RemoteIDs()))
	for _, id := range s.registry.RemoteIDs() {
		remoteIDSet[id] = true
	}

	entries := make([]healthProviderEntry, 0, len(providerIDs))
	for _, id := range providerIDs {
		st := s.state.GetState(id)
		entry := healthProviderEntry{
			ID:       id,
			IsRemote: remoteIDSet[id],
			State:    st.String(),
		}
		if ps, ok := snap.Providers[id]; ok {
			entry.LatencyMs = ps.LatencyMs
			entry.ConcurrentActive = ps.ConcurrentActive
		}
		if s.limits != nil {
			entry.ConcurrentLimit = s.limits.ConcurrencyLimit(id)
		}
		if st == state.StateBlocked {
			until := s.state.BlockedUntil(id)
			entry.BlockedUntil = &until
		}

		if s.modelLimits != nil {
			models := s.registry.ForProviderID(id)
			if len(models) > 0 {
				modelEntries := make([]modelHealthEntry, 0, len(models))
				for _, m := range models {
					key := id + "/" + m.ModelID
					mState := "available"
					var blockedUntil *time.Time
					if s.modelLimits.IsBlocked(key) {
						mState = "blocked"
						u := s.modelLimits.ModelBlockedUntil(key)
						if !u.IsZero() {
							blockedUntil = &u
						}
					}
					cfgs, hasCfgs := s.modelLimits.ConfigsFor(key)
					var limitWindows []modelLimitWindow
					if hasCfgs {
						ws := s.modelLimits.WindowStates(key)
						for i, cfg := range cfgs {
							lw := modelLimitWindow{
								Requests: cfg.Requests,
								Window:   cfg.Window.String(),
							}
							if i < len(ws) && !ws[i].ResetAt.IsZero() {
								lw.Used = ws[i].Count
								t := ws[i].ResetAt
								lw.ResetAt = &t
							}
							limitWindows = append(limitWindows, lw)
						}
					}
					modelEntries = append(modelEntries, modelHealthEntry{
						ID:           m.ModelID,
						Priority:     m.Priority,
						State:        mState,
						BlockedUntil: blockedUntil,
						Limits:       limitWindows,
					})
				}
				entry.Models = modelEntries
			}
		}

		entries = append(entries, entry)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(healthResponse{Status: "ok", Providers: entries})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.metrics.Snapshot())
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	data := []modelEntry{
		{ID: "auto", Object: "model"},
	}
	for _, e := range s.registry.GlobalList() {
		if s.state.GetState(e.ProviderID) != state.StateAvailable {
			continue
		}
		data = append(data, modelEntry{
			ID:       e.ModelID,
			Object:   "model",
			Priority: e.Priority,
			IsFree:   e.IsFree,
			State:    "available",
		})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(modelsResponse{Object: "list", Data: data})
}

// handleReport handles the /report endpoint
func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if s.reportState == nil {
		http.Error(w, "Reporting not available", http.StatusInternalServerError)
		return
	}

	allStates := s.reportState.GetAllProviderStates()

	var healthy []state.ProviderState
	var degradedBlocked []state.ProviderState
	var unreachableMisconfigured []state.ProviderState

	for _, p := range allStates {
		switch p.Status {
		case state.StatusHealthy:
			healthy = append(healthy, p)
		case state.StatusDegraded, state.StatusBlocked:
			degradedBlocked = append(degradedBlocked, p)
		default:
			unreachableMisconfigured = append(unreachableMisconfigured, p)
		}
	}

	data := ReportData{
		Global:                   s.reportState.GetGlobalState(),
		HealthyProviders:         healthy,
		DegradedBlocked:          degradedBlocked,
		UnreachableMisconfigured: unreachableMisconfigured,
		AllProviders:             allStates,
	}

	tmpl, err := ReadTemplate()
	if err != nil {
		log.Printf("template parse error: %v", err)
		http.Error(w, "Template error", http.StatusInternalServerError)
		return
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		log.Printf("template execute error: %v", err)
		http.Error(w, "Template execution error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	buf.WriteTo(w)
}
