package router_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rodrigoazlima/localrouter/internal/config"
	"github.com/rodrigoazlima/localrouter/internal/limits"
	"github.com/rodrigoazlima/localrouter/internal/metrics"
	"github.com/rodrigoazlima/localrouter/internal/provider"
	"github.com/rodrigoazlima/localrouter/internal/registry"
	"github.com/rodrigoazlima/localrouter/internal/router"
	"github.com/rodrigoazlima/localrouter/internal/state"
)

type fakeProvider struct {
	id       string
	endpoint string
	failWith error
}

func (f *fakeProvider) ID() string       { return f.id }
func (f *fakeProvider) Type() string     { return "fake" }
func (f *fakeProvider) Endpoint() string { return f.endpoint }
func (f *fakeProvider) HealthCheck(_ context.Context) error { return nil }
func (f *fakeProvider) Complete(_ context.Context, req *provider.Request) (*provider.Response, error) {
	if f.failWith != nil {
		return nil, f.failWith
	}
	return &provider.Response{ID: "r1", Model: req.Model, Content: "ok"}, nil
}
func (f *fakeProvider) Stream(_ context.Context, req *provider.Request) (<-chan provider.Chunk, error) {
	if f.failWith != nil {
		return nil, f.failWith
	}
	ch := make(chan provider.Chunk, 1)
	ch <- provider.Chunk{Delta: "ok", Done: true}
	close(ch)
	return ch, nil
}

type fakeHealth struct {
	ready map[string]bool
}

func (f *fakeHealth) IsReady(id string) bool {
	if f.ready == nil {
		return true
	}
	return f.ready[id]
}

func buildRouter(providers []config.ProviderConfig, defaultModel string, ps map[string]provider.Provider) *router.Router {
	reg := registry.Build(providers, defaultModel)
	h := &fakeHealth{}
	st := state.New(h)
	lim := limits.New(nil)
	m := metrics.New()
	cfg := router.Config{
		DefaultModel:    defaultModel,
		RecoveryWindows: map[string]time.Duration{},
	}
	return router.New(ps, reg, st, lim, nil, m, cfg)
}

func buildRouterWithHealth(providers []config.ProviderConfig, defaultModel string, ps map[string]provider.Provider, h state.HealthReader) *router.Router {
	reg := registry.Build(providers, defaultModel)
	st := state.New(h)
	lim := limits.New(nil)
	m := metrics.New()
	cfg := router.Config{
		DefaultModel:    defaultModel,
		RecoveryWindows: map[string]time.Duration{},
	}
	return router.New(ps, reg, st, lim, nil, m, cfg)
}

func TestRoute_ExplicitModel(t *testing.T) {
	fp := &fakeProvider{id: "p1", endpoint: "http://localhost"}
	providers := []config.ProviderConfig{{
		ID: "p1", Type: "ollama",
		Models: []config.ModelConfig{{ID: "llama3:latest", Priority: 1}},
	}}
	r := buildRouter(providers, "llama3:latest", map[string]provider.Provider{"p1": fp})

	resp, err := r.Route(context.Background(), &provider.Request{Model: "llama3:latest", Messages: []provider.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Model != "llama3:latest" {
		t.Errorf("want model llama3:latest, got %s", resp.Model)
	}
}

func TestRoute_AutoModel(t *testing.T) {
	fp1 := &fakeProvider{id: "p1"}
	fp2 := &fakeProvider{id: "p2"}
	providers := []config.ProviderConfig{
		{ID: "p1", Type: "ollama", Models: []config.ModelConfig{{ID: "m1", Priority: 1}}},
		{ID: "p2", Type: "ollama", Models: []config.ModelConfig{{ID: "m2", Priority: 2}}},
	}
	r := buildRouter(providers, "m1", map[string]provider.Provider{"p1": fp1, "p2": fp2})

	resp, err := r.Route(context.Background(), &provider.Request{Model: "auto"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Model != "m1" {
		t.Errorf("auto should pick highest priority model m1, got %s", resp.Model)
	}
}

func TestRoute_EmptyModel_UsesDefault(t *testing.T) {
	fp := &fakeProvider{id: "p1"}
	providers := []config.ProviderConfig{{
		ID: "p1", Type: "ollama",
		Models: []config.ModelConfig{{ID: "default-model", Priority: 1}},
	}}
	r := buildRouter(providers, "default-model", map[string]provider.Provider{"p1": fp})

	resp, err := r.Route(context.Background(), &provider.Request{Model: ""})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Model != "default-model" {
		t.Errorf("empty model should use default, got %s", resp.Model)
	}
}

func TestRoute_UnknownModel(t *testing.T) {
	fp := &fakeProvider{id: "p1"}
	providers := []config.ProviderConfig{{
		ID: "p1", Type: "ollama",
		Models: []config.ModelConfig{{ID: "m1", Priority: 1}},
	}}
	r := buildRouter(providers, "m1", map[string]provider.Provider{"p1": fp})

	_, err := r.Route(context.Background(), &provider.Request{Model: "unknown-model"})
	if !errors.Is(err, router.ErrModelNotFound) {
		t.Errorf("want ErrModelNotFound, got %v", err)
	}
}

func TestRoute_ProviderFailover(t *testing.T) {
	fp1 := &fakeProvider{id: "p1", failWith: &provider.HTTPError{StatusCode: 500, Body: "error"}}
	fp2 := &fakeProvider{id: "p2"}
	providers := []config.ProviderConfig{
		{ID: "p1", Type: "ollama", Models: []config.ModelConfig{{ID: "m1", Priority: 1}}},
		{ID: "p2", Type: "ollama", Models: []config.ModelConfig{{ID: "m1", Priority: 2}}},
	}
	r := buildRouter(providers, "m1", map[string]provider.Provider{"p1": fp1, "p2": fp2})

	resp, err := r.Route(context.Background(), &provider.Request{Model: "m1"})
	if err != nil {
		t.Fatalf("want failover to p2, got error: %v", err)
	}
	_ = resp
}

func TestRoute_AllProvidersFail(t *testing.T) {
	httpErr := &provider.HTTPError{StatusCode: 500, Body: "err"}
	fp := &fakeProvider{id: "p1", failWith: httpErr}
	providers := []config.ProviderConfig{{
		ID: "p1", Type: "ollama",
		Models: []config.ModelConfig{{ID: "m1", Priority: 1}},
	}}
	r := buildRouter(providers, "m1", map[string]provider.Provider{"p1": fp})

	_, err := r.Route(context.Background(), &provider.Request{Model: "m1"})
	if !errors.Is(err, router.ErrAllProvidersFailed) {
		t.Errorf("want ErrAllProvidersFailed, got %v", err)
	}
}

func TestRoute_UnhealthyProviderSkipped(t *testing.T) {
	fp1 := &fakeProvider{id: "p1"}
	fp2 := &fakeProvider{id: "p2"}
	providers := []config.ProviderConfig{
		{ID: "p1", Type: "ollama", Models: []config.ModelConfig{{ID: "m1", Priority: 1}}},
		{ID: "p2", Type: "ollama", Models: []config.ModelConfig{{ID: "m1", Priority: 2}}},
	}
	h := &fakeHealth{ready: map[string]bool{"p1": false, "p2": true}}
	r := buildRouterWithHealth(providers, "m1", map[string]provider.Provider{"p1": fp1, "p2": fp2}, h)

	resp, err := r.Route(context.Background(), &provider.Request{Model: "m1"})
	if err != nil {
		t.Fatalf("want p2 to serve (p1 unhealthy), got error: %v", err)
	}
	if resp.Model != "m1" {
		t.Errorf("want m1, got %s", resp.Model)
	}
}

func TestStream_SelectsProvider(t *testing.T) {
	fp := &fakeProvider{id: "p1"}
	providers := []config.ProviderConfig{{
		ID: "p1", Type: "ollama",
		Models: []config.ModelConfig{{ID: "m1", Priority: 1}},
	}}
	r := buildRouter(providers, "m1", map[string]provider.Provider{"p1": fp})

	_, ch, err := r.Stream(context.Background(), &provider.Request{Model: "m1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	chunk := <-ch
	if chunk.Delta != "ok" {
		t.Errorf("want chunk delta 'ok', got %q", chunk.Delta)
	}
}

func TestStream_Failover(t *testing.T) {
	fp1 := &fakeProvider{id: "p1", failWith: &provider.HTTPError{StatusCode: 500, Body: "error"}}
	fp2 := &fakeProvider{id: "p2"}
	providers := []config.ProviderConfig{
		{ID: "p1", Type: "ollama", Models: []config.ModelConfig{{ID: "m1", Priority: 1}}},
		{ID: "p2", Type: "ollama", Models: []config.ModelConfig{{ID: "m1", Priority: 2}}},
	}
	r := buildRouter(providers, "m1", map[string]provider.Provider{"p1": fp1, "p2": fp2})

	_, ch, err := r.Stream(context.Background(), &provider.Request{Model: "m1"})
	if err != nil {
		t.Fatalf("want failover to p2, got error: %v", err)
	}
	chunk := <-ch
	if chunk.Delta != "ok" {
		t.Errorf("want chunk delta 'ok', got %q", chunk.Delta)
	}
}
