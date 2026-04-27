package server_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rodrigoazlima/localrouter/internal/config"
	"github.com/rodrigoazlima/localrouter/internal/health"
	"github.com/rodrigoazlima/localrouter/internal/limits"
	"github.com/rodrigoazlima/localrouter/internal/metrics"
	"github.com/rodrigoazlima/localrouter/internal/provider"
	"github.com/rodrigoazlima/localrouter/internal/registry"
	"github.com/rodrigoazlima/localrouter/internal/router"
	"github.com/rodrigoazlima/localrouter/internal/server"
	"github.com/rodrigoazlima/localrouter/internal/state"
)

type succeedingProvider struct{}

func (s *succeedingProvider) ID() string       { return "ok" }
func (s *succeedingProvider) Type() string     { return "mock" }
func (s *succeedingProvider) Endpoint() string { return "http://mock" }
func (s *succeedingProvider) Complete(_ context.Context, req *provider.Request) (*provider.Response, error) {
	return &provider.Response{Content: "Hello!", Model: req.Model}, nil
}
func (s *succeedingProvider) Stream(_ context.Context, _ *provider.Request) (<-chan provider.Chunk, error) {
	ch := make(chan provider.Chunk, 3)
	ch <- provider.Chunk{Delta: "He"}
	ch <- provider.Chunk{Delta: "llo"}
	close(ch)
	return ch, nil
}
func (s *succeedingProvider) HealthCheck(_ context.Context) error { return nil }

func buildTestServer(p provider.Provider) *server.Server {
	m := metrics.New()
	mon := health.New(m, 2000)
	providerCfgs := []config.ProviderConfig{{
		ID:   p.ID(),
		Type: "openai-compatible",
		Models: []config.ModelConfig{{ID: "test-model", Priority: 1}},
	}}
	reg := registry.Build(providerCfgs, "test-model")
	st := state.New(&fakeHealthReader{})
	lim := limits.New(nil)
	rCfg := router.Config{
		DefaultModel:    "test-model",
		RecoveryWindows: map[string]time.Duration{},
	}
	r := router.New(map[string]provider.Provider{p.ID(): p}, reg, st, lim, m, rCfg)
	return server.New(r, mon, st, reg, m, "", false)
}

func TestCompletions_NonStream_ReturnsJSON(t *testing.T) {
	srv := buildTestServer(&succeedingProvider{})
	body := `{"model":"auto","messages":[{"role":"user","content":"Hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	choices, _ := resp["choices"].([]any)
	if len(choices) == 0 {
		t.Fatal("expected at least one choice")
	}
}

func TestCompletions_Stream_SendsSSEEvents(t *testing.T) {
	srv := buildTestServer(&succeedingProvider{})
	body := `{"model":"auto","messages":[{"role":"user","content":"Hi"}],"stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("expected text/event-stream, got %s", ct)
	}

	scanner := bufio.NewScanner(bytes.NewReader(rr.Body.Bytes()))
	var dataLines []string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			dataLines = append(dataLines, strings.TrimPrefix(line, "data: "))
		}
	}
	if len(dataLines) == 0 {
		t.Fatal("no data lines in SSE response")
	}
	last := dataLines[len(dataLines)-1]
	if last != "[DONE]" {
		t.Fatalf("last data line must be [DONE], got %s", last)
	}
}

func TestCompletions_MissingBody_Returns400(t *testing.T) {
	srv := buildTestServer(&succeedingProvider{})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}
