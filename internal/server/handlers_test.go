// internal/server/handlers_test.go
package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rodrigoazlima/localrouter/internal/cache"
	"github.com/rodrigoazlima/localrouter/internal/health"
	"github.com/rodrigoazlima/localrouter/internal/metrics"
	"github.com/rodrigoazlima/localrouter/internal/server"
)

func TestHealthEndpoint_ReturnsJSON(t *testing.T) {
	m := metrics.New()
	c := cache.New()
	mon := health.New(m, 2000)
	srv := server.New(nil, mon, c, m)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if _, ok := body["local"]; !ok {
		t.Fatal("missing 'local' key in health response")
	}
	if _, ok := body["remote"]; !ok {
		t.Fatal("missing 'remote' key in health response")
	}
}

func TestMetricsEndpoint_ReturnsJSON(t *testing.T) {
	m := metrics.New()
	m.LocalRequests.Add(5)
	srv := server.New(nil, health.New(m, 2000), cache.New(), m)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var snap metrics.Snapshot
	if err := json.NewDecoder(rr.Body).Decode(&snap); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if snap.LocalRequests != 5 {
		t.Fatalf("expected 5, got %d", snap.LocalRequests)
	}
}
