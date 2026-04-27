package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rodrigoazlima/localrouter/internal/config"
	"github.com/rodrigoazlima/localrouter/internal/health"
	"github.com/rodrigoazlima/localrouter/internal/metrics"
	"github.com/rodrigoazlima/localrouter/internal/registry"
	"github.com/rodrigoazlima/localrouter/internal/server"
	"github.com/rodrigoazlima/localrouter/internal/state"
)

type fakeHealthReader struct{}

func (f *fakeHealthReader) IsReady(_ string) bool { return true }

func buildHandlerTestServer(t *testing.T) *server.Server {
	t.Helper()
	m := metrics.New()
	mon := health.New(m, 2000)
	reg := registry.Build([]config.ProviderConfig{}, "")
	st := state.New(&fakeHealthReader{})
	return server.New(nil, mon, st, reg, m, "", false)
}

func TestHealthEndpoint_ReturnsJSON(t *testing.T) {
	srv := buildHandlerTestServer(t)

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
	m.Requests.Add(5)
	mon := health.New(m, 2000)
	reg := registry.Build([]config.ProviderConfig{}, "")
	st := state.New(&fakeHealthReader{})
	srv := server.New(nil, mon, st, reg, m, "", false)

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
	if snap.Requests != 5 {
		t.Fatalf("expected 5 requests, got %d", snap.Requests)
	}
}
