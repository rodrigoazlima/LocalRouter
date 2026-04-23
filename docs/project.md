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
3. Router evaluates Tier 1 (local)
4. If local succeeds → return response
5. If local fails → evaluate Tier 2
6. Select first non-blocked remote provider
7. Execute request
8. Update cache based on response outcome
9. Return response

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

* `"model": "auto"` triggers routing logic
* Explicit model bypasses routing (direct execution if mapped)

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

### Tier 1: Local Execution (Primary)

**Evaluation Criteria:**

* Node reachable
* Status = READY
* Latency below threshold (configurable)

**Execution Modes:**

* Single-node direct routing
* Multi-node: first-available or round-robin (optional, simple)

**Failure Conditions:**

* Connection failure
* Timeout exceeded
* HTTP 5xx
* Explicit “not ready” signal

If any condition triggers → escalate to Tier 2

---

### Tier 2: Remote Providers (Fallback)

**Selection Rules:**

1. Iterate provider list in configured order
2. Skip providers marked as BLOCKED
3. Select first AVAILABLE provider
4. Execute request

**Failure Handling:**

* On failure, update cache state
* Continue to next provider
* If all providers fail → return error to client

---

## 6. Error-Aware State Management

### Cache Design

* In-memory key-value store
* Key: `provider_id`
* Value:

```json
{
  "state": "available | blocked",
  "reason": "tier_a | tier_b",
  "expires_at": timestamp
}
```

---

### Error Classification

#### Tier A Errors (Transient)

* Rate limit exceeded
* Overloaded / busy responses
* 429 or equivalent signals

**Action:**

* Mark provider as BLOCKED
* TTL: 1 hour

---

#### Tier B Errors (Persistent)

* Authentication failure
* Invalid request format
* Misconfiguration
* Repeated 4xx (non-rate limit)

**Action:**

* Mark provider as BLOCKED
* TTL: 24 hours

---

### Cache Behavior

* On each request:

  * Check provider state before selection
  * Skip if `state = blocked` and `now < expires_at`
* On TTL expiration:

  * Automatically return to AVAILABLE state
* No active probing required; passive recovery

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

## 8. Local Node Management

### Node States

* READY
* DEGRADED (high latency)
* UNAVAILABLE

### Health Signals

* Heartbeat endpoint
* Passive latency observation
* Timeout tracking

### Selection Rule

* Prefer lowest-latency READY node
* If none available → Tier 2

---

## 9. Streaming Handling

* Pass-through streaming from selected backend
* Normalize chunk format
* Abort propagation on upstream failure
* Maintain consistent client interface regardless of backend

---

## 10. Configuration Model

Example:

```yaml
local:
  nodes:
    - id: node-1
      endpoint: http://localhost:11434
      timeout_ms: 3000

remote:
  providers:
    - id: provider-1
      endpoint: https://api.provider1.com
      api_key: ${API_KEY_1}
    - id: provider-2
      endpoint: https://api.provider2.com
      api_key: ${API_KEY_2}

routing:
  latency_threshold_ms: 2000
  fallback_enabled: true
```

---

## 11. Performance Targets

* Routing overhead: < 10ms
* Local-first success rate: maximized
* Failover decision time: < 5ms
* Memory footprint: minimal (in-memory cache only)

---

## 12. Failure Modes

| Scenario                   | Behavior                          |
| -------------------------- | --------------------------------- |
| Local node timeout         | Immediate fallback                |
| Local node partial failure | Retry next local node or fallback |
| Remote rate limit          | Cache block (1h)                  |
| Remote auth failure        | Cache block (24h)                 |
| All providers unavailable  | Return structured error           |

---

## 13. Design Constraints

* No complex load balancing algorithms
* Deterministic routing order
* In-memory state only (no external DB)
* Prioritize availability over optimal distribution
* Avoid retry storms through caching

---

## 14. End State

A deterministic routing layer that:

* Maximizes use of local compute
* Minimizes failed external calls
* Adapts to provider instability via memory-cached state
* Maintains a single, stable API surface for all inference paths
