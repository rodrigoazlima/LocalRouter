// internal/server/sse_test.go
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

	"github.com/rodrigoazlima/localrouter/internal/cache"
	"github.com/rodrigoazlima/localrouter/internal/health"
	"github.com/rodrigoazlima/localrouter/internal/metrics"
	"github.com/rodrigoazlima/localrouter/internal/provider"
	"github.com/rodrigoazlima/localrouter/internal/router"
	"github.com/rodrigoazlima/localrouter/internal/server"
)

type succeedingProvider struct{}

func (s *succeedingProvider) ID() string   { return "ok" }
func (s *succeedingProvider) Type() string { return "mock" }
func (s *succeedingProvider) Complete(_ context.Context, _ *provider.Request) (*provider.Response, error) {
	return &provider.Response{Content: "Hello!"}, nil
}
func (s *succeedingProvider) Stream(_ context.Context, _ *provider.Request) (<-chan provider.Chunk, error) {
	ch := make(chan provider.Chunk, 3)
	ch <- provider.Chunk{Delta: "He"}
	ch <- provider.Chunk{Delta: "llo"}
	close(ch)
	return ch, nil
}
func (s *succeedingProvider) HealthCheck(_ context.Context) error { return nil }

type alwaysReady struct{}

func (a *alwaysReady) IsReady(_ string) bool { return true }

func buildTestServer(p provider.Provider) *server.Server {
	m := metrics.New()
	c := cache.New()
	mon := health.New(m, 2000)
	r := router.New([]provider.Provider{p}, nil, c, &alwaysReady{}, m, true)
	return server.New(r, mon, c, m)
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
