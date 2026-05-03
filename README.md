<a id="readme-top"></a>

[![Build Status][build-shield]][build-url]
[![Release][release-shield]][release-url]
[![Go Version][go-shield]][go-url]
[![License: MIT][license-shield]][license-url]

<br />
  <pre>
  _                    _ ____             _            
 | |    ___   ___ __ _| |  _ \ ___  _   _| |_ ___ _ __
 | |   / _ \ / __/ _` | | |_) / _ \| | | | __/ _ \ '__|
 | |__| (_) | (_| (_| | |  _ &lt; (_) | |_| | ||  __/ |
 |_____\___/ \___\__,_|_|_| \_\___/ \__,_|\__\___|_|   
  </pre>

<div align="center">
  <h3 align="center">LocalRouter</h3>

  <p align="center">
    One OpenAI-compatible endpoint. Every LLM provider. Automatic failover.
    <br />
    <a href="#configuration-reference"><strong>Explore the docs »</strong></a>
    <br />
    <br />
    <a href="#quick-setup">Quick Start</a>
    &middot;
    <a href="https://github.com/rodrigoazlima/localrouter/issues/new?labels=bug">Report Bug</a>
    &middot;
    <a href="https://github.com/rodrigoazlima/localrouter/issues/new?labels=enhancement">Request Feature</a>
  </p>
</div>

---

<!-- TABLE OF CONTENTS -->
<details>
  <summary>Table of Contents</summary>
  <ol>
    <li><a href="#about-the-project">About The Project</a>
      <ul>
        <li><a href="#built-with">Built With</a></li>
      </ul>
    </li>
    <li><a href="#quick-setup">Quick Setup</a>
      <ul>
        <li><a href="#prerequisites">Prerequisites</a></li>
        <li><a href="#installation">Installation</a></li>
      </ul>
    </li>
    <li><a href="#usage">Usage</a></li>
    <li><a href="#configuration-reference">Configuration Reference</a></li>
    <li><a href="#api-reference">API Reference</a></li>
    <li><a href="#project-structure">Project Structure</a></li>
    <li><a href="#roadmap">Roadmap</a></li>
    <li><a href="#contributing">Contributing</a></li>
    <li><a href="#license">License</a></li>
    <li><a href="#contact">Contact</a></li>
  </ol>
</details>

---

## About The Project

LocalRouter is a single OpenAI-compatible endpoint that routes every request to the best available LLM provider — local or remote — based on priority, health, and rate limits. Your app talks to one URL. LocalRouter handles the rest.

**Why this exists:** running Ollama or LM Studio locally is fast and free, but those servers go down. Remote APIs have rate limits and cost money. Without a router, every app reimplements the same fallback logic — badly. LocalRouter does it once, correctly, and stays out of the way.

**How routing works:**

1. Each model has a `priority` (lower = preferred). Providers are tried in priority order.
2. If a provider is **unhealthy** (health check failures), **exhausted** (rate limit hit), or **blocked** (HTTP error), it is skipped automatically without restarts.
3. `model=auto` tries the globally highest-priority model first, then falls through the full list until something succeeds.
4. `model=<name>` routes to the lowest-priority provider that has that model and is available.
5. Empty model uses the configured `default_model`.

**Provider state machine:** `AVAILABLE` → `BLOCKED` (on HTTP error, clears after `recovery_window`) / `EXHAUSTED` (rate limit hit, clears when window resets) / `UNHEALTHY` (consecutive health check failures, clears on recovery).

**Features at a glance:**

- OpenAI-compatible `/v1/chat/completions` — streaming (SSE) and non-streaming
- Priority-based routing — deterministic, no YAML ordering dependency
- Automatic failover — failed provider blocked for `recovery_window`, next tried immediately
- Per-provider rate limits — fixed-window counters, multiple windows (RPM + RPD), lazy reset
- `GET /health` — per-provider state with latency
- `GET /metrics` — request counts, failures, stream stats, block/exhaustion events
- `GET /report` — HTML dashboard showing provider states and actionable error diagnostics
- `GET /models` / `GET /v1/models` — lists available models (available-state providers only)
- Hot-reload — `config.yaml` reloads on save (~100ms debounce, in-flight requests complete on old config)
- State persistence — provider states saved to `.settings/` for recovery across restarts
- SSE heartbeat — 15-second ping to prevent proxy timeouts
- Auto-discovery — scans environment variables and LAN for providers, generates config automatically
- Supported provider types: `ollama`, `openai-compatible`, `anthropic`, `google`, `cohere`, `mistral`
- Skipped providers — if `api_key` resolves to an empty env var, provider is silently skipped at startup
- Single static binary, no external runtime dependencies

<p align="right">(<a href="#readme-top">back to top</a>)</p>

### Built With

[![Go][Go-badge]][Go-url]
[![chi][chi-badge]][chi-url]
[![Docker][Docker-badge]][Docker-url]

<p align="right">(<a href="#readme-top">back to top</a>)</p>

---

## Quick Setup

### Prerequisites

- Go 1.22+ (build from source) **or** download a pre-built binary
- At least one LLM provider: [Ollama](https://ollama.com), [Groq](https://console.groq.com), [OpenAI](https://platform.openai.com), etc.

### Installation

**Download binary** (Linux, macOS, Windows):

```bash
# Linux amd64
curl -L https://github.com/rodrigoazlima/localrouter/releases/latest/download/localrouter-linux-amd64 \
  -o localrouter && chmod +x localrouter

# macOS arm64
curl -L https://github.com/rodrigoazlima/localrouter/releases/latest/download/localrouter-darwin-arm64 \
  -o localrouter && chmod +x localrouter
```

Pre-built binaries for Linux (amd64/arm64), macOS (amd64/arm64), and Windows (amd64) are on the [releases page](https://github.com/rodrigoazlima/localrouter/releases).

**Build from source:**

```bash
git clone https://github.com/rodrigoazlima/localrouter.git
cd localrouter
go build -o localrouter ./cmd/localrouter
```

**Docker:**

```bash
docker build -t localrouter .
docker run -p 8080:8080 -v $(pwd)/config.yaml:/config.yaml localrouter
```

Docker image also available on the GitHub Container Registry: `ghcr.io/rodrigoazlima/localrouter`

**Windows Service** (via NSSM, requires Administrator + PowerShell 7+):

```powershell
.\scripts\install.ps1 install
.\scripts\install.ps1 start
.\scripts\install.ps1 status
```

The installer builds the binary, registers it as a Windows service with auto-restart, and injects API keys from your environment.

<p align="right">(<a href="#readme-top">back to top</a>)</p>

---

## Usage

### 1. Configure

Create `config.yaml`. Minimal example — Ollama local with Groq as fallback:

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
    api_key: ${GROQ_API_KEY}
    limits:
      - requests: 100
        window: 1m
    recovery_window: 10m
    models:
      - id: llama-3.1-8b-instant
        priority: 10
```

**Auto-discover providers from environment variables** (skips manual config):

```bash
./localrouter -discover
```

Scans `GROQ_API_KEY`, `OPENAI_KEY`, `GOOGLE_API_KEY`, etc., fetches available models, and writes `config.yaml` automatically.

### 2. Run

```bash
GROQ_API_KEY=gsk_... ./localrouter -config config.yaml
```

### 3. Send requests

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

# Check provider health
curl http://localhost:8080/health
```

Works with any OpenAI-compatible client — set `base_url` to `http://localhost:8080` or `http://localhost:8080/v1` (both work).

**Python example:**

```python
from openai import OpenAI

client = OpenAI(base_url="http://localhost:8080/v1", api_key="unused")
response = client.chat.completions.create(
    model="auto",
    messages=[{"role": "user", "content": "Hello!"}]
)
print(response.choices[0].message.content)
```

<p align="right">(<a href="#readme-top">back to top</a>)</p>

---

## Configuration Reference

### CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-config` | `config.yaml` | Path to config file |
| `-port` | `8080` | HTTP listen port |
| `-discover` | `false` | Auto-discover providers from environment |
| `-network-discover` | `false` | Scan LAN for other LocalRouter instances |

### Config Schema (version: 2)

```yaml
version: 2

logging:
  level: INFO                          # DEBUG | INFO

routing:
  default_model: llama3.2:latest       # used when request omits model field
  latency_threshold_ms: 2000           # health monitor: above this → DEGRADED
  fallback_enabled: true               # enable automatic failover

providers:
  - id: my-provider                    # unique; used in logs and /health
    type: ollama                       # ollama | openai-compatible | anthropic | google | cohere | mistral
    endpoint: http://localhost:11434   # required for ollama, openai-compatible, mistral
    api_key: ${MY_KEY}                 # env var expanded; if set+empty → provider skipped
    timeout_ms: 30000                  # per-request HTTP timeout
    stream_timeout_ms: 3600000         # streaming request timeout
    chat_path: /v1/chat/completions    # override default chat path
    health_check_path: /v1/models      # override health check endpoint
    recovery_window: 5m                # how long BLOCKED state lasts (default: 1h)

    limits:
      - requests: 100                  # max requests per window
        window: 1m                     # window duration (30s, 1m, 1h, 24h)
        concurrent_requests: 10        # max in-flight requests (0 = unlimited)
      - requests: 1000
        window: 24h                    # multiple limits per provider supported

    models:
      - id: llama3.2:latest            # model ID passed to this provider
        priority: 1                    # lower = preferred; must be > 0
        is_free: true                  # metadata only; does not affect routing
        api_key: ${MODEL_KEY}          # per-model API key override
        temperature: 0.7               # model parameter overrides
        top_p: 0.95
        max_tokens: 4096
        seed: -1
        limits:                        # per-model rate limits
          - requests: 20
            window: 1m
```

**Rules:**
- All provider IDs must be unique
- Every model must have `priority > 0`
- `routing.default_model` must exist in at least one non-skipped provider
- Providers with `api_key` pointing to an unset env var are silently skipped at startup

### Supported Providers & Environment Variables

| Variable | Provider | Free Tier |
|----------|----------|-----------|
| `OPENROUTER_API_KEY` | OpenRouter | 500+ `:free` models |
| `GROQ_API_KEY` | Groq | Llama 3.x, Kimi K2 |
| `NVIDIA_API_KEY` | NVIDIA NIM | free credits |
| `GITHUB_TOKEN` | GitHub Models | GPT-4o, Llama |
| `GOOGLE_API_KEY` | Google Gemini | Flash / Flash-lite |
| `COHERE_API_KEY` | Cohere | Command R/R+ |
| `MISTRAL_API_KEY` | Mistral AI | dev quota |
| `ZHIPU_API_KEY` | Zhipu AI | GLM-4-Flash |
| `ANTHROPIC_KEY` | Anthropic | paid |
| `OPENAI_KEY` | OpenAI | paid |
| `VLLM_KEY` | vLLM (local) | local |

<p align="right">(<a href="#readme-top">back to top</a>)</p>

---

## API Reference

### `POST /v1/chat/completions`

Standard OpenAI chat completions. `model` field controls routing:

| Value | Behavior |
|-------|----------|
| `auto` | Try models in global priority order until one succeeds |
| `<model-id>` | Route to lowest-priority available provider with that model |
| _(omitted)_ | Use `routing.default_model` |

Supports both streaming (`"stream": true`, SSE) and non-streaming responses.

### `GET /models` · `GET /v1/models`

Lists models from available providers only. Always includes `auto` as the first entry.

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

### `GET /report`

Returns an HTML dashboard showing:
- Providers working successfully
- Providers with issues (rate limited, transient errors)
- Providers failing health checks (unreachable/misconfigured)

Each provider card includes the error type (`rate_limit`, `deprecated_model`, `timeout`, `invalid_endpoint`) and actionable suggestions.

<p align="right">(<a href="#readme-top">back to top</a>)</p>

---

## Project Structure

```
cmd/localrouter/
  main.go              # entry point — wires components, startup probes, shutdown
internal/
  config/
    config.go          # v2 schema parsing, env expansion, validation
    watcher.go         # fsnotify hot-reload, 100ms debounce
  discovery/           # env var scanning, model fetching, LAN discovery
  health/
    monitor.go         # background health checks, READY/DEGRADED/UNAVAILABLE hysteresis
  limits/
    tracker.go         # fixed-window request counters per provider
    concurrency.go     # concurrent request tracking
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
    handlers.go        # /health, /metrics, /models, /report
    sse.go             # /v1/chat/completions, SSE streaming
scripts/
  install.ps1          # Windows service installer (NSSM)
  run.ps1 / run.sh     # local dev runner
config.yaml            # example configuration (v2 schema)
Dockerfile             # multi-stage build; Alpine + static binary
.github/workflows/
  ci.yml               # go test -race + E2E Playwright tests
  release.yml          # GoReleaser cross-platform builds
test/e2e/              # Playwright end-to-end tests
```

<p align="right">(<a href="#readme-top">back to top</a>)</p>

---

## Roadmap

- [x] Priority-based routing with automatic failover
- [x] Per-provider and per-model rate limiting (multiple windows)
- [x] `model=auto` global fallback chain
- [x] Hot-reload configuration without downtime
- [x] State persistence across restarts
- [x] Enpoint `/report` with error diagnostics
- [x] Auto-discovery from environment variables
- [x] LAN discovery for LocalRouter instances
- [x] Windows service installer
- [ ] `model=small` global fallback chain focused in fast and low complexity tasks
- [ ] `model=medium` global fallback chain focused in average tasks
- [ ] `model=big` global fallback chain focused in high complexity tasks
- [ ] Limits for context size acording to prompt
- [ ] Convert /report into json format
- [ ] Prompt compressions compatibility
- [ ] Strategy for model compatibility
- [ ] Web UI for real-time provider dashboard
- [ ] Test integration with with Cline
- [ ] Test integration with with Code
- [ ] Test integration with with Open Code
- [ ] Test integration with with Kilo
- [ ] Prometheus `/metrics` endpoint
- [ ] Request cost tracking and budget limits
- [ ] System for custom routing strategies

See [open issues](https://github.com/rodrigoazlima/localrouter/issues) for proposed features and known issues.

<p align="right">(<a href="#readme-top">back to top</a>)</p>

---

## Contributing

1. Fork, branch from `master`
2. `go test ./...` before and after changes
3. Write tests for new behavior
4. One logical change per commit
5. PR against `master` with a clear description

**Code conventions:** `gofmt`, return errors don't log at call site, fail-fast config validation, no new external dependencies without discussion.

<a href="https://github.com/rodrigoazlima/localrouter/graphs/contributors">
  <img src="https://contrib.rocks/image?repo=rodrigoazlima/localrouter" alt="contributors" />
</a>

<p align="right">(<a href="#readme-top">back to top</a>)</p>

---

## License

Distributed under the MIT License. See `LICENSE` for more information.

<p align="right">(<a href="#readme-top">back to top</a>)</p>

---

## Contact

Rodrigo Lima — rodrigoazlima@gmail.com

Project Link: [https://github.com/rodrigoazlima/localrouter](https://github.com/rodrigoazlima/localrouter)

<p align="right">(<a href="#readme-top">back to top</a>)</p>

---

<!-- MARKDOWN LINKS & BADGES -->
[build-shield]: https://img.shields.io/github/actions/workflow/status/rodrigoazlima/localrouter/ci.yml?branch=master&style=for-the-badge
[build-url]: https://github.com/rodrigoazlima/localrouter/actions
[release-shield]: https://img.shields.io/github/v/release/rodrigoazlima/localrouter?style=for-the-badge
[release-url]: https://github.com/rodrigoazlima/localrouter/releases
[go-shield]: https://img.shields.io/badge/Go-1.22+-00ADD8?style=for-the-badge&logo=go&logoColor=white
[go-url]: https://go.dev
[license-shield]: https://img.shields.io/badge/License-MIT-blue?style=for-the-badge
[license-url]: https://github.com/rodrigoazlima/localrouter/blob/master/LICENSE
[Go-badge]: https://img.shields.io/badge/Go-00ADD8?style=for-the-badge&logo=go&logoColor=white
[Go-url]: https://go.dev
[chi-badge]: https://img.shields.io/badge/chi-router-black?style=for-the-badge
[chi-url]: https://github.com/go-chi/chi
[Docker-badge]: https://img.shields.io/badge/Docker-2496ED?style=for-the-badge&logo=docker&logoColor=white
[Docker-url]: https://docker.com
