// internal/server/handlers.go - HTTP handlers for the server
package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"

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

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"ok"}`)
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
