package startup_test

import (
	"context"
	"errors"
	"testing"

	"github.com/rodrigoazlima/localrouter/internal/cache"
	"github.com/rodrigoazlima/localrouter/internal/health"
	"github.com/rodrigoazlima/localrouter/internal/metrics"
	"github.com/rodrigoazlima/localrouter/internal/provider"
	"github.com/rodrigoazlima/localrouter/internal/startup"
)

type stubProvider struct {
	id  string
	err error
}

func (s *stubProvider) ID() string   { return s.id }
func (s *stubProvider) Type() string { return "stub" }
func (s *stubProvider) Complete(_ context.Context, _ *provider.Request) (*provider.Response, error) {
	return nil, errors.New("not implemented")
}
func (s *stubProvider) Stream(_ context.Context, _ *provider.Request) (<-chan provider.Chunk, error) {
	return nil, errors.New("not implemented")
}
func (s *stubProvider) HealthCheck(_ context.Context) error { return s.err }

func newMon() *health.Monitor {
	return health.New(metrics.New(), 2000)
}

func TestRun_LocalPass_SetsMonitorReady(t *testing.T) {
	mon := newMon()
	c := cache.New()
	local := &stubProvider{id: "local-1", err: nil}
	mon.AddNode("local-1", local, 100, 60000)

	startup.Run(context.Background(), []provider.Provider{local}, nil, mon, c, 5000)

	if !mon.IsReady("local-1") {
		t.Fatal("expected local-1 to be StateReady after passing probe")
	}
	mon.Stop()
}

func TestRun_LocalFail_DoesNotSetReady(t *testing.T) {
	mon := newMon()
	c := cache.New()
	local := &stubProvider{id: "local-2", err: errors.New("connection refused")}
	mon.AddNode("local-2", local, 100, 60000)

	startup.Run(context.Background(), []provider.Provider{local}, nil, mon, c, 5000)

	if mon.IsReady("local-2") {
		t.Fatal("expected local-2 to remain StateUnavailable after failing probe")
	}
	mon.Stop()
}

func TestRun_RemotePass_Unblocks(t *testing.T) {
	mon := newMon()
	c := cache.New()
	c.Block("remote-1", cache.TierA)
	remote := &stubProvider{id: "remote-1", err: nil}

	startup.Run(context.Background(), nil, []provider.Provider{remote}, mon, c, 5000)

	if c.IsBlocked("remote-1") {
		t.Fatal("expected remote-1 unblocked after passing probe")
	}
}

func TestRun_RemoteFail_AuthError_BlocksTierB(t *testing.T) {
	mon := newMon()
	c := cache.New()
	remote := &stubProvider{id: "remote-2", err: &provider.HTTPError{StatusCode: 401}}

	startup.Run(context.Background(), nil, []provider.Provider{remote}, mon, c, 5000)

	if !c.IsBlocked("remote-2") {
		t.Fatal("expected remote-2 blocked after 401 probe")
	}
	e := c.Get("remote-2")
	if e.Reason != cache.TierB {
		t.Fatalf("expected TierB block for 401, got %v", e.Reason)
	}
}

func TestRun_RemoteFail_OtherError_BlocksTierA(t *testing.T) {
	mon := newMon()
	c := cache.New()
	remote := &stubProvider{id: "remote-3", err: errors.New("timeout")}

	startup.Run(context.Background(), nil, []provider.Provider{remote}, mon, c, 5000)

	if !c.IsBlocked("remote-3") {
		t.Fatal("expected remote-3 blocked after timeout probe")
	}
	e := c.Get("remote-3")
	if e.Reason != cache.TierA {
		t.Fatalf("expected TierA block for timeout, got %v", e.Reason)
	}
}

func TestRun_LocalFail_DoesNotTouchCache(t *testing.T) {
	mon := newMon()
	c := cache.New()
	local := &stubProvider{id: "local-3", err: errors.New("down")}
	mon.AddNode("local-3", local, 100, 60000)

	startup.Run(context.Background(), []provider.Provider{local}, nil, mon, c, 5000)

	if c.IsBlocked("local-3") {
		t.Fatal("expected local node not to be added to cache on failure")
	}
	mon.Stop()
}
