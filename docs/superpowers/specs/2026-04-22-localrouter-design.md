# LocalRouter — Implementation Design

**Date:** 2026-04-22
**Status:** Approved
**Source spec:** `doc/project.md`

---

## 1. Language & Runtime

Go. Single binary. Fits <10ms routing overhead target, native SSE streaming, strong concurrency primitives.

---

## 2. Package Structure

```
localrouter/
├── cmd/localrouter/
│   └── main.go
├── internal/
│   ├── config/
│   ├── cache/
│   ├── health/
│   ├── metrics/
│   ├── provider/
│   │   ├── provider.go         # interface + shared types
│   │   ├── local/
│   │   │   ├── base/           # shared OpenAI-compat HTTP logic
│   │   │   ├── ollama/         # base + Ollama quirks
│   │   │   ├── lmstudio/       # base + LM Studio quirks
│   │   │   └── vllm/           # base + vLLM quirks
│   │   └── remote/
│   │       ├── openai/
│   │       ├── anthropic/
│   │       ├── google/
│   │       └── cohere/
│   ├── router/
│   └── server/
├── config.yaml
└── Dockerfile
```

**Adapter type = protocol, not service identity.** Config drives instances. LM Studio, vLLM, and hosted OpenAI-compatible endpoints all use `type: openai-compatible`. Ollama gets its own adapter only for health check and model listing quirks.

---

## 3. Provider Interface & Core Types

```go
type Provider interface {
    ID() string
    Type() string
    Complete(ctx context.Context, req *Request) (*Response, error)
    Stream(ctx context.Context, req *Request) (<-chan Chunk, error)
    HealthCheck(ctx context.Context) error
}

type Request struct {
    Model    string
    Messages []Message
    Stream   bool
    Raw      map[string]any // passthrough extra fields
}

type Message struct {
    Role    string
    Content string
}

type Response struct {
    ID      string
    Model   string
    Content string
    Usage   Usage
}

type Chunk struct {
    Delta string
    Done  bool
    Err   error
}

type Usage struct {
    PromptTokens     int
    CompletionTokens int
}
```

Each adapter translates `Request` → provider wire format and maps response back to `Response`/`Chunk`. Router never touches wire formats.

`HealthCheck(ctx)` contract:
- Returns error on any non-200
- Does not block beyond ctx deadline
- Does not retry internally
- Idempotent and lightweight

---

## 4. Cache & Error Classification

```go
type State string

const (
    StateAvailable State = "available"
    StateBlocked   State = "blocked"
)

type Entry struct {
    State     State
    Reason    string    // "tier_a" | "tier_b"
    ExpiresAt time.Time
}

type Cache struct {
    mu      sync.RWMutex
    entries map[string]Entry  // key: provider_id
}
```

**Tier A errors** (transient) → block 1 hour:
- HTTP 429, 529
- Body contains "overloaded" or "rate limit"

**Tier B errors** (persistent) → block 24 hours:
- HTTP 401, 403
- 3 consecutive non-rate-limit 4xx

Cache check on every remote selection: skip provider if `state=blocked && now < expiresAt`. Lazy expiry on read — no active cleanup goroutine required.

Hot reload preserves all cache state. Blocked providers remain blocked across config reloads.

---

## 5. Router Logic

```go
type Router struct {
    mu         sync.RWMutex
    localNodes []NodeState
    remotes    []provider.Provider
    cache      *cache.Cache
    health     *health.Monitor
    metrics    *metrics.Collector
}

func (r *Router) Route(ctx context.Context, req *provider.Request) (*provider.Response, error) {
    r.mu.RLock()
    locals := r.localNodes
    remotes := r.remotes
    r.mu.RUnlock()

    // Tier 1: local nodes in configured order
    for _, node := range locals {
        if r.health.IsReady(node.ID) {
            resp, err := node.Provider.Complete(ctx, req)
            if err == nil {
                r.metrics.LocalRequests.Add(1)
                return resp, nil
            }
            r.metrics.Tier1Failures.Add(1)
        }
    }

    // Tier 2: remote providers in configured order
    for _, p := range remotes {
        if r.cache.IsBlocked(p.ID()) {
            continue
        }
        resp, err := p.Complete(ctx, req)
        if err == nil {
            r.metrics.RemoteRequests.Add(1)
            return resp, nil
        }
        r.metrics.Tier2Failures.Add(1)
        r.cache.Block(p.ID(), classifyError(err))
    }

    r.metrics.NoCapacity.Add(1)
    return nil, ErrAllProvidersFailed
}
```

Streaming follows identical routing path, calls `Stream()` instead of `Complete()`, passes `<-chan Chunk` to SSE handler. No duplicated routing logic.

Config hot reload: router swaps `localNodes`/`remotes` under `sync.RWMutex`. Providers removed from config are reference-counted — not destroyed until in-flight requests complete.

**DEGRADED nodes are not eligible for Tier 1** (deterministic, Option A). Only READY nodes receive local traffic.

---

## 6. HTTP Server & SSE Streaming

Framework: stdlib `net/http` + `chi` router.

### Endpoints

```
POST /v1/chat/completions
GET  /health
GET  /metrics
```

### SSE Handler

```go
func (s *Server) streamHandler(w http.ResponseWriter, r *http.Request) {
    flusher, ok := w.(http.Flusher)
    if !ok {
        http.Error(w, "streaming unsupported", http.StatusInternalServerError)
        return
    }

    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-cache, no-transform")
    w.Header().Set("Connection", "keep-alive")
    w.Header().Set("X-Accel-Buffering", "no")
    flusher.Flush() // early flush — prevents proxy buffering on connect

    s.metrics.StreamsStarted.Add(1)
    start := time.Now()
    disconnected := false
    defer func() {
        if disconnected {
            s.metrics.StreamsDisconnected.Add(1)
        } else {
            s.metrics.StreamsCompleted.Add(1)
        }
        s.metrics.StreamDuration.Add(time.Since(start).Milliseconds())
    }()

    chunks, err := s.router.Stream(r.Context(), req)
    if err != nil {
        writeSSEError(w, err)
        flusher.Flush()
        return
    }

    ticker := time.NewTicker(15 * time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-r.Context().Done():
            disconnected = true
            return
        case <-ticker.C:
            fmt.Fprintf(w, ": ping\n\n")
            flusher.Flush()
        case chunk, ok := <-chunks:
            if !ok {
                fmt.Fprintf(w, "data: [DONE]\n\n")
                flusher.Flush()
                return
            }
            if chunk.Err != nil {
                writeSSEError(w, chunk.Err)
                flusher.Flush()
                return
            }
            fmt.Fprintf(w, "data: %s\n\n", marshalChunk(chunk))
            flusher.Flush()
        }
    }
}
```

**SSE invariants:**
- Stream terminates exactly once: `[DONE]` on success, error event on failure, never both
- Error event format: `{"error": {"message": "...", "type": "stream_error"}}`
- `marshalChunk()` never panics, always produces valid JSON
- 15s heartbeat prevents idle timeout through load balancers and proxies
- `r.Context()` cancellation propagates upstream — adapter closes channel, goroutine exits

---

## 7. Health Monitor

```go
type Monitor struct {
    mu      sync.RWMutex
    states  map[string]NodeStatus
    workers map[string]*nodeWorker
}

type NodeStatus struct {
    State        State   // READY | DEGRADED | UNAVAILABLE
    LatencyMs    int64
    LastCheck    time.Time
    SuccessRun   int     // consecutive successes
    FailureRun   int     // consecutive failures
    LatencyBreaches int
}

type nodeWorker struct {
    cancel context.CancelFunc
}
```

**State transitions (hysteresis):**
- UNAVAILABLE: after 3 consecutive failures
- READY: after 2 consecutive successes
- DEGRADED: latency threshold breached 3 times consecutively

**Default state:** UNAVAILABLE until first successful check (cold start safety).

**Check loop per node:**
```go
ctx, cancel := context.WithTimeout(parent, node.TimeoutMs)
defer cancel()
start := time.Now()
err := provider.HealthCheck(ctx)
latency := time.Since(start) // measured at call site, not adapter-reported
```

**Scheduling:**
- Interval: configurable (default 10s) + `rand(0, jitter)` — prevents thundering herd
- Exponential backoff when UNAVAILABLE, up to max interval; resets on success

**Lifecycle:**
- New node on reload: spawn worker goroutine
- Removed node on reload: call `worker.cancel()`, no orphaned goroutines
- Config change to non-endpoint fields (e.g. `timeout_ms`): preserve counters
- Config change to endpoint: reset counters, restart worker

**RWMutex discipline:**
- Writes: only inside monitor goroutines
- Reads: router uses `RLock()` only
- Compute new state outside lock, assign inside lock

**Snapshot:** `Monitor.Snapshot()` returns deep copy — no external mutation.

**Per-node metrics:** `health_check_success_total`, `health_check_failure_total`, `current_state` gauge, `latency_ms` last observed.

---

## 8. Config & Hot Reload

```go
type Config struct {
    Local   LocalConfig   `yaml:"local"`
    Remote  RemoteConfig  `yaml:"remote"`
    Routing RoutingConfig `yaml:"routing"`
}

type NodeConfig struct {
    ID        string `yaml:"id"`
    Type      string `yaml:"type"`      // "ollama" | "openai-compatible" | "anthropic" | "google" | "cohere"
    Endpoint  string `yaml:"endpoint"`
    APIKey    string `yaml:"api_key"`   // supports ${ENV_VAR}
    TimeoutMs int    `yaml:"timeout_ms"`
}
```

**Env expansion:** Walk all string fields at load time, replace `${VAR}` via `os.Getenv`. Expanded values never written to logs or `/health`/`/metrics` responses. `Config.String()` and `MarshalJSON()` redact all `api_key` fields.

**Startup order** (strict, each step waits for previous):
1. Load & validate config
2. Init cache
3. Init health monitor
4. Init providers
5. Init router
6. Start server

**Shutdown order** (reverse):
1. Stop accepting requests
2. Drain in-flight requests
3. Stop router
4. Stop health monitor
5. Stop providers

**Hot reload pipeline:**
1. fsnotify detects file change → debounce 100ms
2. Single reload worker goroutine (serialized — no concurrent reloads)
3. Parse and validate new config; on failure: log error, keep current, no state change
4. Assign monotonic version number
5. **Atomic swap:** all components transition to new config version simultaneously under single lock
6. Health monitor reconciles workers (add new, cancel removed; preserve counters for endpoint-unchanged nodes)
7. Router swaps node/provider slices
8. Cache untouched

**Request-scoped config snapshot:** Each request captures config version at intake and uses it for its full lifecycle. In-flight requests on old config version complete against old providers.

**Provider reference counting:** Providers removed by reload are not destroyed until all requests holding a reference complete (`sync.WaitGroup` per provider instance).

**Active streams:** Hold reference to provider at stream start. Provider removal does not terminate live streams — they complete on old instance.

**Reload churn:** fsnotify events debounced + reload worker serializes processing. Reload N+1 queued, not run concurrently with N.

**fsnotify reliability:** Periodic re-read fallback every 60s to catch missed events.

---

## 9. Metrics

```go
type Collector struct {
    // Counters (monotonically increasing)
    LocalRequests       atomic.Int64
    RemoteRequests      atomic.Int64
    Tier1Failures       atomic.Int64
    Tier2Failures       atomic.Int64
    NoCapacity          atomic.Int64  // ErrAllProvidersFailed
    StreamsStarted      atomic.Int64
    StreamsCompleted    atomic.Int64
    StreamsDisconnected atomic.Int64

    nodeChecksOK   map[string]*atomic.Int64  // protected by RWMutex
    nodeChecksFail map[string]*atomic.Int64

    // Gauges (current state)
    BlockedProviders atomic.Int64              // updated on cache state change
    nodeLatencyMs    map[string]*atomic.Int64  // last observed

    // Durations
    StreamDuration atomic.Int64  // cumulative ms, counter semantics
}
```

`NoCapacity` increments when all providers are unavailable or blocked at routing time — distinct from per-provider failure counters.

Gauges are never summed over time. `Snapshot()` tags each field with semantic type in JSON response.

No external dependencies (no Prometheus). Percentile histograms deferred — current last-observed latency sufficient for operational visibility.

---

## 10. Configuration Example

```yaml
local:
  nodes:
    - id: ollama-1
      type: ollama
      endpoint: http://localhost:11434
      timeout_ms: 3000
    - id: lmstudio-1
      type: openai-compatible
      endpoint: http://localhost:1234
      timeout_ms: 3000
    - id: vllm-1
      type: openai-compatible
      endpoint: http://localhost:8000
      api_key: ${VLLM_KEY}
      timeout_ms: 3000

remote:
  providers:
    - id: openai-1
      type: openai-compatible
      endpoint: https://api.openai.com
      api_key: ${OPENAI_KEY}
    - id: anthropic-1
      type: anthropic
      api_key: ${ANTHROPIC_KEY}
    - id: google-1
      type: google
      api_key: ${GOOGLE_API_KEY}
    - id: cohere-1
      type: cohere
      api_key: ${COHERE_KEY}

routing:
  latency_threshold_ms: 2000
  fallback_enabled: true
```

---

## 11. Deployment

**Binary:** `go build ./cmd/localrouter` — single static binary, no runtime deps.

**Docker:**
```dockerfile
FROM golang:1.23-alpine AS builder
WORKDIR /app
COPY . .
RUN go build -o localrouter ./cmd/localrouter

FROM alpine:3.19
COPY --from=builder /app/localrouter /usr/local/bin/
EXPOSE 8080
ENTRYPOINT ["localrouter"]
```

---

## 12. Testing Strategy

Unit tests only. Each package tested in isolation via interface mocks.

Key test surfaces:
- `cache`: block/unblock TTL logic, expiry, tier A/B classification
- `router`: tier1→tier2 fallback, blocked provider skipping, all-fail path
- `health`: state transitions, hysteresis thresholds, backoff
- `config`: env expansion, validation, reload logic, secret redaction
- `server`: SSE handler termination invariants, context cancellation
- `provider/*/`: request/response translation per adapter

---

## 13. Performance Targets

Per `doc/project.md`:
- Routing overhead: < 10ms
- Failover decision: < 5ms
- Memory: in-memory cache only, no external DB
