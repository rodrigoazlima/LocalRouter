// internal/server/handlers.go
package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/rodrigoazlima/localrouter/internal/health"
)

type healthResponse struct {
	Local  localHealth    `json:"local"`
	Remote []remoteHealth `json:"remote"`
}

type localHealth struct {
	Status string     `json:"status"`
	Nodes  []nodeInfo `json:"nodes"`
}

type nodeInfo struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	LatencyMs int64  `json:"latency_ms"`
}

type remoteHealth struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	TTLRemaining int64  `json:"ttl_remaining,omitempty"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	snap := s.monitor.Snapshot()
	cacheSnap := s.cache.Snapshot()
	metricsSnap := s.metrics.Snapshot()

	nodes := make([]nodeInfo, 0, len(snap))
	overallStatus := "unavailable"
	for id, st := range snap {
		var status string
		switch st.State {
		case health.StateReady:
			status = "ready"
			overallStatus = "healthy"
		case health.StateDegraded:
			status = "degraded"
			if overallStatus != "healthy" {
				overallStatus = "degraded"
			}
		default:
			status = "unavailable"
		}
		latency := metricsSnap.Nodes[id].LatencyMs
		nodes = append(nodes, nodeInfo{ID: id, Status: status, LatencyMs: latency})
	}

	remotes := make([]remoteHealth, 0)
	for id, entry := range cacheSnap {
		rh := remoteHealth{ID: id}
		if entry.State == "blocked" && time.Now().Before(entry.ExpiresAt) {
			rh.Status = "blocked"
			rh.TTLRemaining = int64(time.Until(entry.ExpiresAt).Seconds())
		} else {
			rh.Status = "available"
		}
		remotes = append(remotes, rh)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(healthResponse{
		Local:  localHealth{Status: overallStatus, Nodes: nodes},
		Remote: remotes,
	})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.metrics.Snapshot())
}
