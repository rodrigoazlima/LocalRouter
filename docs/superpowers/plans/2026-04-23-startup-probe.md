# Startup Probe Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** On startup, concurrently probe every configured provider via its existing `HealthCheck()` method and write results into monitor state (locals) and block cache (remotes) before any real traffic arrives.

**Architecture:** New `internal/startup` package exposes a single `Run` function called as a goroutine from `main.go`. Locals that pass have their monitor state immediately set to `StateReady` (skipping the ~20s cold-start). Remotes that fail are blocked in the cache; remotes that pass have any stale block cleared. All probes run concurrently with a 10s timeout per provider. No LLM traffic — all `HealthCheck()` implementations use GET `/models`-style endpoints.

**Tech Stack:** Go 1.22, `sync.WaitGroup`, `context.WithTimeout`, existing `health.Monitor`, `cache.Cache`, `provider.Provider` interfaces.

---

## File Map

| Action | Path | Responsibility |
|---|---|---|
| Modify | `internal/cache/cache.go` | Add `Unblock(id)` method |
| Modify | `internal/cache/cache_test.go` | Test `Unblock` |
| Modify | `internal/health/monitor.go` | Add `SetReady(id)` method |
| Modify | `internal/health/monitor_test.go` | Test `SetReady` |
| Create | `internal/startup/probe.go` | `Run` function — concurrent probe logic |
| Create | `internal/startup/probe_test.go` | Tests for all probe outcomes |
| Modify | `cmd/localrouter/main.go` | Wire up `go startup.Run(...)` |

---

## Task 1: Add `cache.Unblock`

**Files:**
- Modify: `internal/cache/cache.go`
- Modify: `internal/cache/cache_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/cache/cache_test.go`:

```go
func TestCache_Unblock_ClearsBlock(t *testing.T) {
	c := cache.New()
	c.Block("p1", cache.TierA)
	if !c.IsBlocked("p1") {
		t.Fatal("expected p1 blocked before Unblock")
	}
	c.Unblock("p1")
	if c.IsBlocked("p1") {
		t.Fatal("expected p1 unblocked after Unblock")
	}
}

func TestCache_Unblock_NoopOnMissing(t *testing.T) {
	c := cache.New()
	c.Unblock("nonexistent") // must not panic
}
```

- [ ] **Step 2: Run test to verify it fails**

```
go test ./internal/cache/... -run TestCache_Unblock -v
```

Expected: `FAIL` — `c.Unblock undefined`

- [ ] **Step 3: Implement `Unblock`**

Add to `internal/cache/cache.go` after `BlockUntil`:

```go
func (c *Cache) Unblock(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, id)
}
```

- [ ] **Step 4: Run test to verify it passes**

```
go test ./internal/cache/... -run TestCache_Unblock -v
```

Expected: `PASS`

- [ ] **Step 5: Run full cache suite**

```
go test ./internal/cache/... -v
```

Expected: all PASS

- [ ] **Step 6: Commit**

```
git add internal/cache/cache.go internal/cache/cache_test.go
git commit -m "feat(cache): add Unblock method"
```

---

## Task 2: Add `health.Monitor.SetReady`

**Files:**
- Modify: `internal/health/monitor.go`
- Modify: `internal/health/monitor_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/health/monitor_test.go`:

```go
func TestMonitor_SetReady_ImmediatelyReady(t *testing.T) {
	m := health.New(metrics.New(), 2000)
	n := &stubNode{id: "n1", err: nil}
	m.AddNode("n1", n, 100, 60000) // 60s interval: background won't fire during test

	if m.IsReady("n1") {
		t.Fatal("expected StateUnavailable before SetReady")
	}
	m.SetReady("n1")
	if !m.IsReady("n1") {
		t.Fatal("expected StateReady after SetReady")
	}
	m.Stop()
}

func TestMonitor_SetReady_NoopOnUnknownID(t *testing.T) {
	m := health.New(metrics.New(), 2000)
	m.SetReady("does-not-exist") // must not panic
}
```

- [ ] **Step 2: Run test to verify it fails**

```
go test ./internal/health/... -run TestMonitor_SetReady -v
```

Expected: `FAIL` — `m.SetReady undefined`

- [ ] **Step 3: Implement `SetReady`**

Add to `internal/health/monitor.go` after `Stop`:

```go
func (mon *Monitor) SetReady(id string) {
	mon.mu.Lock()
	defer mon.mu.Unlock()
	s, ok := mon.states[id]
	if !ok {
		return
	}
	s.State = StateReady
	s.successRun = successThreshold
	s.failureRun = 0
	s.latencyBreaches = 0
	mon.states[id] = s
}
```

- [ ] **Step 4: Run test to verify it passes**

```
go test ./internal/health/... -run TestMonitor_SetReady -v
```

Expected: `PASS`

- [ ] **Step 5: Run full health suite**

```
go test ./internal/health/... -v
```

Expected: all PASS

- [ ] **Step 6: Commit**

```
git add internal/health/monitor.go internal/health/monitor_test.go
git commit -m "feat(health): add SetReady method for startup probe"
```

---

## Task 3: Implement startup probe

**Files:**
- Create: `internal/startup/probe.go`
- Create: `internal/startup/probe_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/startup/probe_test.go`:

```go
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

// stubProvider implements provider.Provider for testing.
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

// TestRun_LocalPass_SetsMonitorReady: passing local node → StateReady.
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

// TestRun_LocalFail_DoesNotSetReady: failing local node → stays StateUnavailable.
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

// TestRun_RemotePass_Unblocks: passing remote → clears any existing block.
func TestRun_RemotePass_Unblocks(t *testing.T) {
	mon := newMon()
	c := cache.New()
	c.Block("remote-1", cache.TierA) // pre-existing block
	remote := &stubProvider{id: "remote-1", err: nil}

	startup.Run(context.Background(), nil, []provider.Provider{remote}, mon, c, 5000)

	if c.IsBlocked("remote-1") {
		t.Fatal("expected remote-1 unblocked after passing probe")
	}
}

// TestRun_RemoteFail_AuthError_BlocksTierB: 401/403 → 24h TierB block.
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

// TestRun_RemoteFail_OtherError_BlocksTierA: non-auth error → 1h TierA block.
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

// TestRun_LocalFail_DoesNotTouchCache: failing local → cache untouched.
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
```

- [ ] **Step 2: Run tests to verify they fail**

```
go test ./internal/startup/... -v
```

Expected: `FAIL` — `startup package not found`

- [ ] **Step 3: Implement `internal/startup/probe.go`**

Create `internal/startup/probe.go`:

```go
package startup

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/rodrigoazlima/localrouter/internal/cache"
	"github.com/rodrigoazlima/localrouter/internal/health"
	"github.com/rodrigoazlima/localrouter/internal/provider"
)

// Run concurrently probes all locals and remotes via HealthCheck.
// Designed to be called as a goroutine: go startup.Run(...).
// Does not check cache or monitor state before probing.
// Does not make LLM requests — all HealthCheck implementations use GET /models-style endpoints.
func Run(ctx context.Context, locals []provider.Provider, remotes []provider.Provider, mon *health.Monitor, c *cache.Cache, timeoutMs int) {
	timeout := time.Duration(timeoutMs) * time.Millisecond
	var wg sync.WaitGroup

	for _, p := range locals {
		wg.Add(1)
		p := p
		go func() {
			defer wg.Done()
			probeCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			start := time.Now()
			err := p.HealthCheck(probeCtx)
			ms := time.Since(start).Milliseconds()
			if err != nil {
				log.Printf("startup probe: local %s: FAIL: %v", p.ID(), err)
				return
			}
			log.Printf("startup probe: local %s: OK (%dms)", p.ID(), ms)
			mon.SetReady(p.ID())
		}()
	}

	for _, p := range remotes {
		wg.Add(1)
		p := p
		go func() {
			defer wg.Done()
			probeCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			start := time.Now()
			err := p.HealthCheck(probeCtx)
			ms := time.Since(start).Milliseconds()
			if err != nil {
				log.Printf("startup probe: remote %s: FAIL: %v", p.ID(), err)
				var httpErr *provider.HTTPError
				if errors.As(err, &httpErr) && (httpErr.StatusCode == 401 || httpErr.StatusCode == 403) {
					c.Block(p.ID(), cache.TierB)
				} else {
					c.Block(p.ID(), cache.TierA)
				}
				return
			}
			log.Printf("startup probe: remote %s: OK (%dms)", p.ID(), ms)
			c.Unblock(p.ID())
		}()
	}

	wg.Wait()
}
```

- [ ] **Step 4: Run tests to verify they pass**

```
go test ./internal/startup/... -v
```

Expected: all PASS

- [ ] **Step 5: Run full suite**

```
go test ./... -race
```

Expected: all PASS

- [ ] **Step 6: Commit**

```
git add internal/startup/probe.go internal/startup/probe_test.go
git commit -m "feat(startup): add concurrent startup probe for all providers"
```

---

## Task 4: Wire up in `main.go`

**Files:**
- Modify: `cmd/localrouter/main.go`

- [ ] **Step 1: Add import**

In `cmd/localrouter/main.go`, add to the import block:

```go
"github.com/rodrigoazlima/localrouter/internal/startup"
```

- [ ] **Step 2: Launch probe goroutine**

In `main()`, after the `mon.AddNode` loop and before `r := router.New(...)`:

```go
	go startup.Run(context.Background(), locals, remotes, mon, c, 10000)

	r := router.New(locals, remotes, c, mon, m, cfg.Routing.FallbackEnabled)
```

Full updated block for context (lines ~47–57 in current `main.go`):

```go
	latency := int64(cfg.Routing.LatencyThresholdMs)
	if latency == 0 {
		latency = 2000
	}
	mon := health.New(m, latency)
	for _, n := range cfg.Local.Nodes {
		p, err := factory.NewFromNode(n)
		if err != nil {
			log.Fatalf("build health checker for %s: %v", n.ID, err)
		}
		mon.AddNode(n.ID, p, n.TimeoutMs, 10000)
	}

	go startup.Run(context.Background(), locals, remotes, mon, c, 10000)

	r := router.New(locals, remotes, c, mon, m, cfg.Routing.FallbackEnabled)
```

- [ ] **Step 3: Build to verify no compile errors**

```
go build ./cmd/localrouter
```

Expected: exits 0, produces `localrouter` binary

- [ ] **Step 4: Run full suite**

```
go test ./... -race
```

Expected: all PASS

- [ ] **Step 5: Commit**

```
git add cmd/localrouter/main.go
git commit -m "feat: wire startup probe into main"
```
