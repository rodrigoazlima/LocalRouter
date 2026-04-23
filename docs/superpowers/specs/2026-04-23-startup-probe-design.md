# Startup Probe Design

Date: 2026-04-23
Project: LocalRouter (`github.com/rodrigoazlima/localrouter`)

## Problem

On startup, all local nodes begin in `StateUnavailable`. The background health monitor needs two consecutive successful checks (each 10 seconds apart) before promoting a node to `StateReady` — a ~20 second cold-start window during which the router rejects all local traffic and falls through to remote providers unnecessarily.

Remote providers have no startup state at all: they are assumed available until the router encounters an error in a real request.

## Solution

A startup probe that fires immediately at launch, concurrently checks every configured provider via its existing `HealthCheck()` method, and writes results into the monitor state (locals) and block cache (remotes) — before any real requests arrive.

## Constraints

- Non-blocking: runs in a goroutine, does not delay server startup
- No LLM traffic: uses only `HealthCheck()` — all implementations use GET `/models`-style endpoints, not completions
- No pre-check: does not inspect cache or monitor state before probing; always probes unconditionally
- No new config: timeout hardcoded to 10 seconds per provider

## New File

`internal/startup/probe.go`

```go
func Run(ctx context.Context,
    locals []provider.Provider,
    remotes []provider.Provider,
    mon *health.Monitor,
    c *cache.Cache,
    timeoutMs int)
```

### Behavior

1. Create one goroutine per provider (locals + remotes), all concurrent
2. Each goroutine: `context.WithTimeout(ctx, timeoutMs)` → `p.HealthCheck(probeCtx)`
3. Log result with timing: `startup probe: [local|remote] <id>: OK (Xms)` or `FAIL: <err>`
4. Write result:

**Local node — pass:** `mon.SetReady(id)` → immediately `StateReady`, skips cold-start  
**Local node — fail:** log only; background monitor continues  
**Remote — pass:** `c.Unblock(id)` → clears any stale block  
**Remote — fail (HTTP 401/403):** `c.Block(id, cache.TierB)` → 24-hour block  
**Remote — fail (other):** `c.Block(id, cache.TierA)` → 1-hour block  

Uses `sync.WaitGroup`; function returns after all probes complete (the goroutine in main exits cleanly).

## New Methods on Existing Types

### `cache.Unblock(id string)`
Deletes the cache entry for `id`. No entry = not blocked. Symmetric with `Block`.

### `health.Monitor.SetReady(id string)`
Acquires write lock, sets `State = StateReady`, resets `successRun = successThreshold`, `failureRun = 0`, `latencyBreaches = 0`. Only acts if node exists. Background worker continues running and will update state on subsequent checks.

## Wire-up in `main.go`

After the `mon.AddNode` loop, before `srv := server.New(...)`:

```go
go startup.Run(context.Background(), locals, remotes, mon, c, 10000)
```

## Error Classification

```go
var httpErr *provider.HTTPError
if errors.As(err, &httpErr) && (httpErr.StatusCode == 401 || httpErr.StatusCode == 403) {
    c.Block(id, cache.TierB)
} else {
    c.Block(id, cache.TierA)
}
```

Consistent with error classification used in the router.

## Testing

- `internal/startup/probe_test.go`
- Mock providers returning pass/fail/auth-error
- Assert `mon.SetReady` called for passing locals (verify via `mon.IsReady`)
- Assert `c.IsBlocked` state after probe for failing remotes
- Assert `c.Unblock` clears a pre-existing block for passing remotes
- Assert no blocking side effects for failing locals
