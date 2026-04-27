# LocalRouter

```
  _                    _ ____             _
 | |    ___   ___ __ _| |  _ \ ___  _   _| |_ ___ _ __
 | |   / _ \ / __/ _` | | |_) / _ \| | | | __/ _ \ '__|
 | |__| (_) | (_| (_| | |  _ < (_) | |_| | ||  __/ |
 |_____\___/ \___\__,_|_|_| \_\___/ \__,_|\__\___|_|
```

[![Build Status](https://img.shields.io/github/actions/workflow/status/rodrigoazlima/localrouter/ci.yml?branch=master)](https://github.com/rodrigoazlima/localrouter/actions)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/rodrigoazlima/localrouter)](https://github.com/rodrigoazlima/localrouter/releases)

---

## What It Is

LocalRouter is a single OpenAI-compatible endpoint that routes every request to the best available LLM provider — local or remote — based on priority, health, and rate limits. Your app talks to one URL. LocalRouter handles the rest.

**Why this exists:** running Ollama or LM Studio locally is fast and free, but those servers go down. Remote APIs have rate limits and cost money. Without a router, every app reimplements the same fallback logic — badly. LocalRouter does it once, correctly, and stays out of the way.

**How routing works:**

1. Each model has a `priority` (lower = preferred). Providers are tried in priority order.
2. If a provider is **unhealthy** (health check failures), **exhausted** (rate limit hit), or **blocked** (HTTP error during a request), it is skipped — automatically, without restarts.
3. `model=auto` tries the globally highest-priority model first, then falls through the full list until something succeeds.
4. `model=<name>` routes to the lowest-priority provider that has that model and is available.
5. Empty model uses the configured `default_model`.

**State machine:** AVAILABLE → BLOCKED (on any HTTP error, clears after `recovery_window`) / EXHAUSTED (on limit hit, clears when window resets) / UNHEALTHY (on consecutive health check failures, clears on recovery).

---

## Quick Setup

### 1. Install

**Download binary** (Linux, macOS, Windows):
```bash
# Linux amd64
curl -L https://github.com/rodrigoazlima/localrouter/releases/latest/download/localrouter-linux-amd64 \
  -o localrouter && chmod +x localrouter
```

**Build from source** (Go 1.22+):
```bash
git clone https://github.com/rodrigoazlima/localrouter.git
cd localrouter
go build -o localrouter ./cmd/localrouter
```

**Docker:**
```bash
docker build -t localrouter .
```

### 2. Configure

Create `config.yaml`. Minimal example — one local Ollama, one free remote fallback:

```yaml
version: 2

routing:
  default_model: llama3.2:latest

providers:
  - id: ollama-local
    type: ollama
    endpoint: http://localhost:11434
    timeout_ms: 3000
    recovery_window: 2m
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

### 3. Run

```bash
GROQ_KEY=gsk_... ./localrouter -config config.yaml
```

### 4. Send requests

```bash
# Auto-select best available model
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"auto","messages":[{"role":"user","content":"hi"}]}'

# Explicit model
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"llama3.2:latest","messages":[{"role":"user","content":"hi"}]}'

# List available models
curl http://localhost:8080/models
```

Works with any OpenAI-compatible client — set `base_url` to `http://localhost:8080` or `http://localhost:8080/v1` (both work).

---

## Features

- **OpenAI-compatible** `/v1/chat/completions` — streaming (SSE) and non-streaming
- **Priority-based routing** — deterministic, no YAML ordering dependency
- **Automatic failover** — failed provider blocked for `recovery_window`, next in priority tried immediately
- **Per-provider rate limits** — fixed-window counter, lazy reset, no background goroutines
- **`model=auto`** — routes through the global priority list until a provider succeeds
- **`GET /models`** and **`GET /v1/models`** — lists available (available-state only) providers and models
- **`GET /health`** — per-provider state (`available` / `unhealthy` / `exhausted` / `blocked`) with latency
- **`GET /metrics`** — request counts, failures, stream counts, block/exhaustion events, per-provider latency
- **Hot-reload** — `config.yaml` reloads on save (~100ms debounce, in-flight requests complete on old config)
- **Provider types**: `ollama`, `openai-compatible`, `anthropic`, `google`, `cohere`, `mistral`
- **Skipped providers** — if `api_key` is set but resolves to empty (unset env var), provider is silently skipped at startup
- **SSE heartbeat** — 15-second ping to prevent proxy timeouts
- Single static binary, no external runtime dependencies

---

## Configuration Reference

### Flags

| Flag | Default | Description |
|---|---|---|
| `-config` | `config.yaml` | Path to config file |
| `-port` | `8080` | HTTP listen port |

### Config Schema (version: 2)

```yaml
version: 2

routing:
  default_model: llama3.2:latest       # used when request omits model field
  latency_threshold_ms: 2000           # health monitor: above this → DEGRADED

providers:
  - id: my-provider                    # unique; used in logs and /health
    type: ollama                       # ollama | openai-compatible | anthropic | google | cohere | mistral
    endpoint: http://localhost:11434   # required for ollama, openai-compatible, mistral
    api_key: ${MY_KEY}                 # optional; env var expanded; if set+empty → provider skipped
    timeout_ms: 3000                   # per-request HTTP timeout (default: 30000)
    recovery_window: 5m                # how long BLOCKED state lasts (default: 1h)
    limits:
      requests: 100                    # max requests per window
      window: 1m                       # window duration (e.g. 30s, 1m, 1h)
    models:
      - id: llama3.2:latest            # model ID passed to this provider
        priority: 1                    # lower = preferred; must be > 0
        is_free: true                  # metadata only; does not affect routing
```

**Rules:**
- All provider IDs must be unique
- Every model must have `priority > 0`
- `routing.default_model` must exist in at least one non-skipped provider
- Providers with `api_key` set to an env var that is unset or empty are skipped at startup (warn in debug, no error)

### Environment Variables

Expanded in `config.yaml` using `${VAR_NAME}`. Only set variables for providers you enable.

| Variable | Provider | Free tier |
|---|---|---|
| `OPENROUTER_KEY` | OpenRouter | ✓ 500+ `:free` models |
| `GROQ_KEY` | Groq | ✓ Llama 3.x, Kimi K2 |
| `NVIDIA_KEY` | NVIDIA NIM | ✓ free credits |
| `GITHUB_TOKEN` | GitHub Models | ✓ GPT-4o, Llama |
| `GOOGLE_API_KEY` | Google Gemini | ✓ Flash / Flash-lite |
| `COHERE_KEY` | Cohere | ✓ Command R/R+ |
| `MISTRAL_KEY` | Mistral AI | ✓ dev quota |
| `ZHIPU_KEY` | Zhipu AI | ✓ GLM-4-Flash |
| `ANTHROPIC_KEY` | Anthropic | — paid |
| `OPENAI_KEY` | OpenAI | — paid |
| `VLLM_KEY` | vLLM (local, if auth enabled) | local |

---

## API

### `POST /v1/chat/completions`

Standard OpenAI chat completions. `model` field controls routing:

| Value | Behavior |
|---|---|
| `auto` | Try models in global priority order until one succeeds |
| `<model-id>` | Route to lowest-priority available provider with that model |
| _(omitted)_ | Use `routing.default_model` |

### `GET /models` / `GET /v1/models`

Lists models from available providers only (state = `available`). Always includes `auto` as the first entry.

```json
{
  "object": "list",
  "data": [
    { "id": "auto", "object": "model", "is_auto": true },
    {
      "id": "llama3.2:latest",
      "object": "model",
      "provider_id": "ollama-local",
      "priority": 1,
      "is_default": true,
      "state": "available"
    }
  ]
}
```

### `GET /health`

Per-provider state. `blocked_until` included when state is `blocked`.

```json
{
  "providers": [
    { "id": "ollama-local", "state": "available", "latency_ms": 34 },
    { "id": "groq-1", "state": "blocked", "latency_ms": 0, "blocked_until": "2026-04-24T12:00:00Z" }
  ]
}
```

### `GET /metrics`

```json
{
  "requests": 142,
  "failures": 3,
  "no_capacity": 0,
  "streams_started": 18,
  "streams_completed": 17,
  "streams_disconnected": 1,
  "stream_duration_ms": 94200,
  "provider_block_events": 3,
  "provider_exhausted_events": 1,
  "providers": {
    "ollama-local": { "checks_ok": 88, "checks_fail": 0, "latency_ms": 34 },
    "groq-1": { "checks_ok": 12, "checks_fail": 1, "latency_ms": 210 }
  }
}
```

---

## Project Structure

```
cmd/localrouter/
  main.go              # entry point — wires components, startup probes, shutdown
internal/
  config/
    config.go          # v2 schema parsing, env expansion, validation
    watcher.go         # fsnotify hot-reload, 100ms debounce
  health/
    monitor.go         # background health checks, READY/DEGRADED/UNAVAILABLE hysteresis
  limits/
    tracker.go         # fixed-window request counter per provider
  metrics/
    metrics.go         # atomic counters, snapshot export
  registry/
    registry.go        # sorted (provider, model) index — priority-order routing table
  state/
    manager.go         # AVAILABLE/UNHEALTHY/EXHAUSTED/BLOCKED state machine
  provider/
    provider.go        # Provider interface, shared types
    factory/           # instantiates provider adapters from config
    openaicompat/      # OpenAI-compatible HTTP adapter
    ollama/            # Ollama adapter
    anthropic/         # Anthropic Messages API adapter
    google/            # Google Gemini adapter
    cohere/            # Cohere chat adapter
  router/
    router.go          # model-aware routing loop — registry → state → limits → provider
  server/
    server.go          # HTTP server (chi)
    handlers.go        # /health, /metrics, /models
    sse.go             # /v1/chat/completions, SSE streaming
config.yaml            # example configuration (v2 schema)
Dockerfile             # multi-stage build; Alpine + static binary
```

---

## Releases

Pre-built binaries for Linux (amd64/arm64), macOS (amd64/arm64), and Windows (amd64) on the [releases page](https://github.com/rodrigoazlima/localrouter/releases). Docker image on the GitHub Container Registry (`ghcr.io/rodrigoazlima/localrouter`).

---

## Contributing

1. Fork, branch from `master`.
2. `go test ./...` before and after changes.
3. Write tests for new behavior.
4. One logical change per commit.
5. PR against `master` with a clear description.

**Code conventions:** `gofmt`, return errors don't log at call site, fail-fast config validation, no new external dependencies without discussion.

---

## License

MIT — Copyright (c) 2026 rodrigoazlima
