// internal/router/router_test.go
package router_test

import (
	"context"
	"errors"
	"testing"

	"github.com/rodrigoazlima/localrouter/internal/cache"
	"github.com/rodrigoazlima/localrouter/internal/metrics"
	"github.com/rodrigoazlima/localrouter/internal/provider"
	"github.com/rodrigoazlima/localrouter/internal/router"
)

type mockProvider struct {
	id           string
	completeErr  error
	completeResp *provider.Response
}

func (m *mockProvider) ID() string       { return m.id }
func (m *mockProvider) Type() string     { return "mock" }
func (m *mockProvider) Endpoint() string { return "http://mock" }
func (m *mockProvider) Complete(_ context.Context, _ *provider.Request) (*provider.Response, error) {
	return m.completeResp, m.completeErr
}
func (m *mockProvider) Stream(_ context.Context, _ *provider.Request) (<-chan provider.Chunk, error) {
	return nil, nil
}
func (m *mockProvider) HealthCheck(_ context.Context) error { return nil }

type alwaysReadyMonitor struct{}

func (a *alwaysReadyMonitor) IsReady(id string) bool { return true }

type neverReadyMonitor struct{}

func (n *neverReadyMonitor) IsReady(id string) bool { return false }

func TestRoute_LocalSuccess_ReturnsLocalResponse(t *testing.T) {
	local := &mockProvider{id: "local-1", completeResp: &provider.Response{Content: "from-local"}}
	r := router.New(
		[]provider.Provider{local},
		nil,
		cache.New(),
		&alwaysReadyMonitor{},
		metrics.New(),
		true,
	)
	resp, err := r.Route(context.Background(), &provider.Request{Model: "auto"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "from-local" {
		t.Fatalf("expected from-local, got %s", resp.Content)
	}
}

func TestRoute_LocalFail_FallsBackToRemote(t *testing.T) {
	local := &mockProvider{id: "local-1", completeErr: errors.New("timeout")}
	remote := &mockProvider{id: "remote-1", completeResp: &provider.Response{Content: "from-remote"}}
	r := router.New(
		[]provider.Provider{local},
		[]provider.Provider{remote},
		cache.New(),
		&alwaysReadyMonitor{},
		metrics.New(),
		true,
	)
	resp, err := r.Route(context.Background(), &provider.Request{Model: "auto"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "from-remote" {
		t.Fatalf("expected from-remote, got %s", resp.Content)
	}
}

func TestRoute_LocalNotReady_SkipsToRemote(t *testing.T) {
	local := &mockProvider{id: "local-1", completeResp: &provider.Response{Content: "from-local"}}
	remote := &mockProvider{id: "remote-1", completeResp: &provider.Response{Content: "from-remote"}}
	r := router.New(
		[]provider.Provider{local},
		[]provider.Provider{remote},
		cache.New(),
		&neverReadyMonitor{},
		metrics.New(),
		true,
	)
	resp, err := r.Route(context.Background(), &provider.Request{Model: "auto"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "from-remote" {
		t.Fatalf("expected from-remote, got %s", resp.Content)
	}
}

func TestRoute_BlockedRemote_Skipped(t *testing.T) {
	local := &mockProvider{id: "local-1", completeErr: errors.New("down")}
	blocked := &mockProvider{id: "remote-blocked"}
	good := &mockProvider{id: "remote-good", completeResp: &provider.Response{Content: "ok"}}

	c := cache.New()
	c.Block("remote-blocked", cache.TierA)

	r := router.New(
		[]provider.Provider{local},
		[]provider.Provider{blocked, good},
		c,
		&alwaysReadyMonitor{},
		metrics.New(),
		true,
	)
	resp, err := r.Route(context.Background(), &provider.Request{Model: "auto"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("expected ok, got %s", resp.Content)
	}
}

func TestRoute_AllFail_ReturnsNoCapacityError(t *testing.T) {
	local := &mockProvider{id: "local-1", completeErr: errors.New("down")}
	remote := &mockProvider{id: "remote-1", completeErr: &provider.HTTPError{StatusCode: 503}}
	m := metrics.New()
	r := router.New(
		[]provider.Provider{local},
		[]provider.Provider{remote},
		cache.New(),
		&alwaysReadyMonitor{},
		m,
		true,
	)
	_, err := r.Route(context.Background(), &provider.Request{Model: "auto"})
	if err == nil {
		t.Fatal("expected error when all providers fail")
	}
	if m.Snapshot().NoCapacity != 1 {
		t.Fatalf("expected NoCapacity=1, got %d", m.Snapshot().NoCapacity)
	}
}

func TestRoute_FallbackDisabled_NoRemoteAttempt(t *testing.T) {
	local := &mockProvider{id: "local-1", completeErr: errors.New("down")}
	remote := &mockProvider{id: "remote-1", completeResp: &provider.Response{Content: "from-remote"}}
	m := metrics.New()
	r := router.New(
		[]provider.Provider{local},
		[]provider.Provider{remote},
		cache.New(),
		&alwaysReadyMonitor{},
		m,
		false,
	)
	_, err := r.Route(context.Background(), &provider.Request{Model: "auto"})
	if !errors.Is(err, router.ErrAllProvidersFailed) {
		t.Fatalf("expected ErrAllProvidersFailed, got %v", err)
	}
	if m.Snapshot().RemoteRequests != 0 {
		t.Fatal("remote must not be attempted when fallback disabled")
	}
}

func TestRoute_429_BlocksProviderTierA(t *testing.T) {
	local := &mockProvider{id: "local-1", completeErr: errors.New("down")}
	remote := &mockProvider{id: "remote-1", completeErr: &provider.HTTPError{StatusCode: 429}}
	c := cache.New()
	r := router.New(
		[]provider.Provider{local}, []provider.Provider{remote}, c,
		&alwaysReadyMonitor{}, metrics.New(), true,
	)
	r.Route(context.Background(), &provider.Request{Model: "auto"})
	if !c.IsBlocked("remote-1") {
		t.Fatal("remote-1 must be blocked after 429")
	}
}
