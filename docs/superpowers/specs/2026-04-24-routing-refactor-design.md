# Routing Refactor: Priority + Limits System

**Date:** 2026-04-24  
**Status:** Approved

## Context

LocalRouter currently routes requests via a two-tier system (local → remote) using YAML ordering as the only selection mechanism. There is no model awareness — the model name from a request is passed through blindly to the first available provider. Provider state is split across two systems: `health.Monitor` (locals) and `cache.Cache` (remotes), with TierA/TierB blocking logic.

This refactor replaces all of the above with:
- A unified `providers:` config list (no local/remote split)
- Priority-based, model-aware routing
- A single state machine for all providers
- Per-provider request limits with automatic window reset
- A `/models` endpoint listing available (model, provider) pairs

---

## 1. Config Schema (version: 2)

Single `providers:` list replaces `local.nodes` + `remote.providers`. `fallback_enabled` removed — priority ordering is the fallback mechanism.

```yaml
version: 2

routing:
  default_model: llama3.2:latest
  latency_threshold_ms: 2000

providers:
  - id: ollama-local
    type: ollama
    endpoint: http://localhost:11434
    timeout_ms: 3000
    api_key: ${OLLAMA_KEY}        # optional; if set but resolves empty → skip provider
    recovery_window: 5m           # how long BLOCKED state lasts; default 1h
    models:
      - id: llama3.2:latest
        priority: 1
        is_free: true
      - id: qwen2.5:7b
        priority: 2
        is_free: true

  - id: groq
    type: openai-compatible
    endpoint: https://api.groq.com/openai
    api_key: ${GROQ_KEY}
    limits:
      requests: 100
      window: 1m
    recovery_window: 5m
    models:
      - id: llama-3.1-8b-instant
        priority: 5
        is_free: true
```

**Valid types:** `ollama`, `openai-compatible`, `anthropic`, `google`, `cohere`, `mistral`

**Validation rules:**
- All provider IDs must be unique
- Every model must define `priority`
- `routing.default_model` must match a model ID present in at least one provider
- If `api_key` is set and resolves to empty string → skip provider (warn, don't error)

---

## 2. Provider State Machine

Four states, unified for all providers:

```
AVAILABLE ──[HTTP error]──► BLOCKED   ──[recovery_window expires]──► AVAILABLE
AVAILABLE ──[limit hit]───► EXHAUSTED ──[window resets]────────────► AVAILABLE
AVAILABLE ──[health fail]─► UNHEALTHY ──[health recovers]──────────► AVAILABLE
```

**Rules:**
- Any HTTP error (any status code) during a request → `BLOCKED` for `recovery_window`
- `recovery_window` default: `1h` if not configured
- Limit counter reached → `EXHAUSTED` until window resets (lazy reset on next access)
- Health check fails 3× consecutively → `UNHEALTHY`; recovers after 2× consecutive success
- Health check success does **not** clear `BLOCKED` or `EXHAUSTED`
- Precedence (highest wins): `BLOCKED > EXHAUSTED > UNHEALTHY > AVAILABLE`

**Deleted:** `internal/cache/cache.go` (TierA/TierB), `internal/startup/probe.go`

---

## 3. Limits Tracker

Fixed-window counter per provider. No background goroutines — lazy reset on access.

**New file:** `internal/limits/tracker.go`

```go
type Config struct {
    Requests int
    Window   time.Duration
}

// Record increments counter for id.
// Returns (exhausted bool, resetAt time.Time).
// If no limits configured for id, always returns (false, zero).
func (t *Tracker) Record(id string) (bool, time.Time)

// ResetAt returns when the current window expires (zero if no limits).
func (t *Tracker) ResetAt(id string) time.Time
```

Window resets when `time.Now() > resetAt`, checked on each `Record` call.

---

## 4. Model Registry

Builds a global ordered list of `(provider, model)` entries at init. Rebuilt on config reload.

**New file:** `internal/registry/registry.go`

```go
type Entry struct {
    ProviderID string
    ModelID    string
    Priority   int
    IsFree     bool
    IsDefault  bool
}

func Build(providers []config.ProviderConfig, defaultModel string) *Registry

// GlobalList: all entries sorted — used for model=auto routing.
func (r *Registry) GlobalList() []Entry

// ForModel: entries for a specific model id — used for explicit model routing.
func (r *Registry) ForModel(id string) []Entry
```

**Sort key:** `(Priority ASC, ProviderID ASC, ModelID ASC)` — deterministic, no YAML order dependency.

`IsDefault = true` on entries where `ModelID == routing.default_model`.

---

## 5. State Manager

Single source of truth for all provider states.

**New file:** `internal/state/manager.go`

```go
type State int

const (
    StateAvailable State = iota
    StateUnhealthy
    StateExhausted
    StateBlocked
)

func (m *Manager) GetState(id string) State
func (m *Manager) Block(id string, d time.Duration)           // called on any HTTP error
func (m *Manager) SetExhausted(id string, resetAt time.Time)  // called when limit hit
func (m *Manager) OnHealthResult(id string, ok bool, latencyMs int64)
```

Wraps `health.Monitor` results via `OnHealthResult`. All routing queries go through `GetState`.

---

## 6. Router

Full rewrite. Single code path for all providers — no local/remote distinction.

**File:** `internal/router/router.go`

```go
type Router struct {
    providers map[string]provider.Provider
    registry  *registry.Registry
    state     *state.Manager
    limits    *limits.Tracker
    cfg       RouterConfig
    mu        sync.RWMutex
}

type RouterConfig struct {
    DefaultModel    string
    RecoveryWindows map[string]time.Duration // provider id → recovery_window
}
```

**Routing algorithm:**

```go
func (r *Router) resolve(model string) []registry.Entry {
    switch model {
    case "":
        return r.registry.ForModel(r.cfg.DefaultModel)
    case "auto":
        return r.registry.GlobalList()
    default:
        return r.registry.ForModel(model)
    }
}

func (r *Router) route(ctx, req) (provider.Provider, string, error) {
    entries := r.resolve(req.Model)
    if len(entries) == 0 {
        return nil, "", ErrModelNotFound
    }
    for _, e := range entries {
        if r.state.GetState(e.ProviderID) != state.StateAvailable {
            continue
        }
        exhausted, resetAt := r.limits.Record(e.ProviderID)
        if exhausted {
            r.state.SetExhausted(e.ProviderID, resetAt)
            continue
        }
        return r.providers[e.ProviderID], e.ModelID, nil
    }
    return nil, "", ErrAllProvidersFailed
}
```

On HTTP error from provider: `r.state.Block(providerID, r.cfg.RecoveryWindows[providerID])`  
Router does not retry the same provider within a single request.

`UpdateProviders()` called on config reload — rebuilds registry, resets limits tracker, updates state manager roster.

---

## 7. `/models` Endpoint

**Routes:** `GET /models` and `GET /v1/models`

Returns only entries where `state.GetState(providerID) == StateAvailable`. Includes `auto` as first entry.

```json
{
  "object": "list",
  "data": [
    {
      "id": "auto",
      "object": "model",
      "is_auto": true
    },
    {
      "id": "llama3.2:latest",
      "object": "model",
      "provider_id": "ollama-local",
      "priority": 1,
      "is_free": true,
      "is_default": true,
      "state": "available",
      "limits": null
    },
    {
      "id": "llama-3.1-8b-instant",
      "object": "model",
      "provider_id": "groq",
      "priority": 5,
      "is_free": true,
      "is_default": false,
      "state": "available",
      "limits": { "requests": 100, "window": "1m" }
    }
  ]
}
```

---

## 8. Initialization Flow

```
1. Parse flags (config path, port)
2. Load config — fail fast if version != 2
3. Validate config (unique IDs, all models have priority, default_model exists)
4. For each provider: if api_key set but resolves empty → skip (log warning)
5. Build provider.Provider instances via factory (type → adapter)
6. Build registry.Registry from valid providers + routing.default_model
7. Build limits.Tracker from provider limits configs
8. Build state.Manager — start health check goroutines for all valid providers
9. Run concurrent startup health probes (10s timeout) → set initial UNHEALTHY on fail
10. Build router
11. Register routes: POST /v1/chat/completions, GET /models, GET /v1/models, GET /health, GET /metrics
12. Log available providers (skipped providers only in debug mode)
13. Start HTTP server
```

**Startup log format (normal mode — skipped providers omitted):**
```
[INIT] ollama-local: available — llama3.2:latest(p=1) qwen2.5:7b(p=2)
[INIT] groq: available — llama-3.1-8b-instant(p=5) [limits: 100/1m]
[INIT] default model: llama3.2:latest
[INIT] listening on :8080
```

**Debug mode adds:**
```
[DEBUG] openai: skipped (api_key set but resolves empty)
```

---

## 9. Free Model Inventory (config.yaml)

At least one `is_free: true` model per provider:

| Provider | Type | Free Model ID |
|----------|------|---------------|
| ollama-local | ollama | (user-defined) |
| openrouter | openai-compatible | `meta-llama/llama-3.2-3b-instruct:free` |
| groq | openai-compatible | `llama-3.1-8b-instant` |
| nvidia | openai-compatible | `meta/llama-3.1-8b-instruct` |
| deepseek | openai-compatible | `deepseek-chat` |
| google | google | `gemini-1.5-flash` |
| cohere | cohere | `command-r` |
| mistral | mistral | `mistral-small-latest` |

`is_free` is metadata only — does not affect routing.

---

## 10. File Impact

| Action | File |
|--------|------|
| **New** | `internal/state/manager.go` |
| **New** | `internal/limits/tracker.go` |
| **New** | `internal/registry/registry.go` |
| **Rewrite** | `internal/config/config.go` |
| **Rewrite** | `internal/router/router.go` |
| **Rewrite** | `cmd/localrouter/main.go` |
| **Extend** | `internal/server/server.go` — add `/models`, `/v1/models` routes |
| **Extend** | `internal/server/handlers.go` — add `handleModels()` |
| **Extend** | `internal/health/monitor.go` — support all providers, not just locals |
| **Extend** | `internal/metrics/metrics.go` — new counters (exhausted events, blocked events) |
| **Delete** | `internal/cache/cache.go` |
| **Delete** | `internal/startup/probe.go` |
| **Migrate** | `config.yaml` — version 2 schema |

**Untouched:** provider adapters (`anthropic/`, `google/`, `cohere/`, `openaicompat/`, `ollama/`), SSE streaming logic in `server/sse.go`.

---

## 11. Compatibility

Maintained:
- `POST /v1/chat/completions` — same request/response shape
- SSE streaming
- `GET /health` — shape preserved, new states added
- `GET /metrics` — shape preserved, new counters added
- No new external dependencies

Breaking:
- `config.yaml` schema — version 2, migration required (rename sections, add models/priority)

---

## 12. Verification

```bash
# Start with new config
go run ./cmd/localrouter -config config.yaml

# Check models endpoint
curl http://localhost:8080/models | jq .

# Route with explicit model
curl -X POST http://localhost:8080/v1/chat/completions \
  -d '{"model":"llama3.2:latest","messages":[{"role":"user","content":"hi"}]}'

# Route with auto
curl -X POST http://localhost:8080/v1/chat/completions \
  -d '{"model":"auto","messages":[{"role":"user","content":"hi"}]}'

# Route with no model (uses default)
curl -X POST http://localhost:8080/v1/chat/completions \
  -d '{"messages":[{"role":"user","content":"hi"}]}'

# Verify health reflects new states
curl http://localhost:8080/health | jq .

# Verify metrics
curl http://localhost:8080/metrics | jq .
```
