# Hybrid Model Orchestrator and Routing System

**Technical Specification**

---

## 1. System Overview

A routing service designed to prioritize local inference resources while maintaining seamless fallback to remote providers. The system enforces deterministic routing, minimal latency overhead, and resilient failure handling through cached provider state and simple decision rules.

Core behavior:

* Always attempt local execution first
* Escalate to remote providers only on failure conditions
* Persist failure states to avoid repeated degraded calls

---

## 2. High-Level Architecture

### Components

**1. API Gateway**

* Handles incoming HTTP requests
* Enforces schema compatibility
* Manages streaming and response normalization

**2. Router Engine**

* Implements Tier 1 → Tier 2 decision flow
* Evaluates availability, latency, and cached provider state

**3. Local Execution Layer**

* Interfaces with local runtimes (single-node or distributed)
* Performs readiness checks and execution

**4. Remote Provider Adapter Layer**

* Normalizes communication with external APIs
* Extracts headers, status codes, and error signals

**5. State Cache (In-Memory)**

* Tracks provider health and block states
* TTL-based expiration

**6. Health Monitor**

* Periodic checks for local nodes
* Passive monitoring of remote provider responses

**7. Metrics Collector**

* Aggregates routing decisions and error rates

---

## 3. Request Lifecycle

1. Request received at `/v1/chat/completions`
2. Input normalized
3. Router evaluates providers in priority order (lowest priority value = highest preference)
4. If provider succeeds → return response
5. If provider fails → update state, skip, and try next provider
6. Execute request on available provider
7. Update state based on response outcome
8. Return response

---

## 4. Endpoint Definitions

### POST /v1/chat/completions

**Purpose:** Primary inference endpoint

**Compatibility:**

* OpenAI-style request/response schema
* Supports streaming (SSE)

**Request Example:**

```json
{
  "model": "auto",
  "messages": [
    { "role": "user", "content": "..." }
  ],
  "stream": true
}
```

**Behavior:**

* `"model": "auto"` triggers routing through global priority list
* Explicit model routes to providers that support it
* Empty model uses configured `default_model`

---

### GET /health

**Purpose:** System health visibility

**Response Structure:**

```json
{
  "local": {
    "status": "healthy | degraded | unavailable",
    "nodes": [
      { "id": "node-1", "status": "ready", "latency_ms": 12 }
    ]
  },
  "remote": [
    { "id": "provider-1", "status": "available | blocked", "ttl_remaining": 1200 }
  ]
}
```

---

### GET /metrics

**Purpose:** Operational insight

**Response Structure:**

```json
{
  "routing": {
    "local_requests": 12450,
    "remote_requests": 3200
  },
  "errors": {
    "tier1_failures": 210,
    "tier2_failures": 45
  },
  "cache": {
    "blocked_providers": 2,
    "average_ttl_seconds": 1800
  }
}
```

---

## 5. Routing Strategy

Provider routing follows a priority-based approach:

1. **Priority Order:** Providers with lower `priority` values are tried first
2. **State Check:** Skip providers in BLOCKED, EXHAUSTED, or UNHEALTHY state
3. **Rate Limiting:** Respect per-provider rate limits (configured via `limits`)
4. **Failover:** On failure, provider enters BLOCKED state for configured `recovery_window`, next provider tried

**Recovery Windows:**
* Each provider has a configurable `recovery_window` (default: 1h)
* Auth failures (HTTP 401/403) use the same recovery window as other errors
* Blocked providers automatically return to AVAILABLE after recovery window expires

---

## 6. Error-Aware State Management

### Provider State Machine

Providers transition between states based on health checks and request outcomes:

**States:**
* **AVAILABLE:** Ready to receive requests
* **UNHEALTHY:** Health check failed (local providers only)
* **EXHAUSTED:** Rate limit reached, resets when window expires
* **BLOCKED:** Request failure, blocked for configured `recovery_window`

---

### Recovery Configuration

Each provider can configure its recovery behavior via `recovery_window`:

```yaml
providers:
  - id: my-provider
    type: ollama
    endpoint: http://localhost:11434
    recovery_window: 5m  # how long BLOCKED state lasts (default: 1h)
```

**Behavior:**
* On failure, provider is blocked for the configured duration
* After `recovery_window` expires, provider returns to AVAILABLE state
* No manual intervention required for recovery
### State Management

The system uses a state manager that:
* Tracks per-provider blocked/unhealthy/exhausted states with timestamps
* Evaluates state transitions based on time-based expiry
* Provides quick state lookups during routing decisions

---

## 7. Rate Limit Tracking

### Inputs

* HTTP status codes
* Response headers (e.g., remaining quota)
* Error payload inspection

### Behavior

* Detect threshold breaches
* Update provider exhaustion state

---

## 7. Rate Limit Tracking

### Inputs

* HTTP status codes
* Response headers (e.g., remaining quota)
* Error payload inspection

### Behavior

* Detect threshold breaches
* Immediately classify into Tier A or Tier B
* Update cache

No predictive throttling. Reactive only.

---

## 7. Local Node Management

### Node States

* **READY:** Healthy and responding normally
* **DEGRADED:** High latency above configured threshold
* **UNAVAILABLE:** Health check failed or connection error

### Health Signals

* Periodic background health checks
* Passive latency observation from requests
* Timeout tracking per request

### Selection Rule

* Local nodes are prioritized first (lowest priority values)
* If all local nodes unavailable, remote providers are tried

---

## 8. Streaming Handling

* Pass-through streaming from selected backend
* Normalize chunk format
* Abort propagation on upstream failure
* Maintain consistent client interface regardless of backend

---

## 9. Configuration Model

Example:

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
    recovery_window: 5m
    models:
      - id: llama3.2:latest
        priority: 1

  - id: groq-1
    type: openai-compatible
    endpoint: https://api.groq.com/openai/v1
    api_key: ${GROQ_KEY}
    limits:
      requests: 100
      window: 1m
    recovery_window: 10m
    models:
      - id: llama-3.1-8b-instant
        priority: 10
```

---

## 10. Performance Targets

* Routing overhead: < 10ms
* Provider selection time: < 5ms
* Failover decision time: < 5ms
* Memory footprint: minimal (in-memory state only)

---

## 11. Failure Modes

| Scenario                   | Behavior                                     |
| -------------------------- | -------------------------------------------- |
| Local node timeout         | Immediate fallback to remote providers       |
| Provider rate limit        | Blocked for configured recovery_window       |
| Auth failure (401/403)     | Blocked for configured recovery_window       |
| All providers unavailable  | Return structured error                      |

---

## 12. Design Constraints

* No complex load balancing algorithms
* Deterministic priority-based routing order
* In-memory state only (no external DB)
* Prioritize availability over optimal distribution
* Avoid retry storms through provider blocking

---

## 13. End State

A deterministic routing layer that:

* Maximizes use of preferred providers based on priority
* Minimizes failed external calls through smart failover
* Adapts to provider instability via time-based blocking
* Uses per-provider `recovery_window` for consistent state management
* Maintains a single, stable API surface for all inference paths
