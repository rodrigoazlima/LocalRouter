# LocalRouter

```
  _                    _ ____             _
 | |    ___   ___ __ _| |  _ \ ___  _   _| |_ ___ _ __
 | |   / _ \ / __/ _` | | |_) / _ \| | | | __/ _ \ '__|
 | |__| (_) | (_| (_| | |  _ < (_) | |_| | ||  __/ |
 |_____\___/ \___\__,_|_|_| \_\___/ \__,_|\__\___|_|
```

Local-first LLM routing proxy with intelligent failover and error-aware provider blocking.

[![Build Status](https://img.shields.io/github/actions/workflow/status/rodrigoazlima/localrouter/ci.yml?branch=master)](https://github.com/rodrigoazlima/localrouter/actions)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/rodrigoazlima/localrouter)](https://github.com/rodrigoazlima/localrouter/releases)

## Overview

LocalRouter sits in front of your local inference servers and remote LLM providers, exposing a single OpenAI-compatible endpoint. Requests flow to local nodes first; remote providers are only reached when local capacity is unavailable or unhealthy.

Core problem: running Ollama, LM Studio, or vLLM locally reduces cost and latency, but those servers are not always reliable. Without a router, any application has to implement fallback logic itself, and re-implement it every time the provider list changes.

Key design ideas:

- **Two-tier routing**: Tier 1 exhausts all healthy local nodes before promoting to Tier 2 remote providers.
- **Error-aware blocking**: provider errors are classified as transient (1-hour block) or persistent (24-hour block), so a broken API key does not keep generating billable failed calls.
- **Hysteresis health checks**: local nodes cycle through READY / DEGRADED / UNAVAILABLE states with success/failure thresholds to prevent flapping on transient latency spikes.

## Features

- OpenAI-compatible `/v1/chat/completions` endpoint (streaming and non-streaming)
- Local provider support: Ollama, LM Studio, vLLM, any OpenAI-compatible server
- Remote provider support: OpenRouter, Groq, Mistral, DeepSeek, NVIDIA, OpenAI (openai-compatible); Anthropic, Google Gemini, Cohere (native adapters)
- Per-request structured logging: client IP, request ID, destination provider, endpoint, model, and tier
- Background per-node health monitoring with exponential backoff
- TTL-based provider blocking: transient errors (HTTP 429/529, rate-limit body) block for 1 hour; auth/config errors (HTTP 401/403, 3+ consecutive 4xx) block for 24 hours
- Hot-reload of `config.yaml` via `fsnotify` with 100ms debounce; in-flight requests complete on the previous config
- Environment variable expansion in config (`${OPENAI_KEY}`)
- `GET /health` — per-node state and provider block status
- `GET /metrics` — request counts, failure counts, stream counts, per-node latency
- Single static binary; no external runtime dependencies
- SSE streaming with 15-second heartbeat to prevent proxy timeouts

## Use Cases

**Cost-optimized inference**: run a quantized model on a workstation for most requests; fall back to OpenAI only when the local server is down or overloaded.

**Multi-GPU lab**: route across several Ollama or vLLM instances; the health monitor balances by availability, not explicit load balancing.

**CI / offline-first environments**: configure remote providers as last-resort fallback; local inference handles the common case with zero egress.

**Provider resilience**: API key rotation or provider outages are handled automatically through TTL blocking without restarting the proxy.

**Free-tier chaining**: configure multiple free-tier providers (OpenRouter, Groq, Gemini, Mistral, Cohere, NVIDIA) as the fallback chain so paid providers are only reached when all free tiers are exhausted or blocked.

## Requirements

- **OS**: Linux, macOS, Windows
- **Go**: 1.22 or later (build from source)
- **Docker**: any recent version (container deployment)
- At least one configured local node or remote provider

No database, no message broker, no sidecar required. State is in-memory only.

## Installation

### Option 1: Download Release

Pre-built binaries for Linux (amd64/arm64), macOS (amd64/arm64), and Windows (amd64) are available on the [releases page](https://github.com/rodrigoazlima/localrouter/releases).

```bash
# Linux amd64 example
curl -L https://github.com/rodrigoazlima/localrouter/releases/latest/download/localrouter-linux-amd64 \
  -o localrouter
chmod +x localrouter
```

### Option 2: Build from Source

```bash
# Clone
git clone https://github.com/rodrigoazlima/localrouter.git
cd localrouter

# Install dependencies
go mod download

# Build
go build -o localrouter ./cmd/localrouter
```

### Option 3: Docker

```bash
docker build -t localrouter .

docker run -p 8080:8080 \
  -v "$(pwd)/config.yaml:/config.yaml" \
  -e OPENROUTER_KEY=sk-or-v1-... \
  -e GROQ_KEY=gsk_... \
  -e GOOGLE_KEY=AIza... \
  -e MISTRAL_KEY=... \
  -e COHERE_KEY=... \
  -e ANTHROPIC_KEY=sk-ant-... \
  -e OPENAI_KEY=sk-... \
  localrouter -config /config.yaml
```

## Running

```bash
./localrouter                            # uses ./config.yaml
./localrouter -config /etc/localrouter/config.yaml
```

LocalRouter listens on `:8080` by default. Send requests to it using any OpenAI-compatible client:

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "auto",
    "messages": [{"role": "user", "content": "What is the capital of France?"}],
    "stream": false
  }'
```

Streaming:

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "auto",
    "messages": [{"role": "user", "content": "Count to five."}],
    "stream": true
  }'
```

Health and metrics:

```bash
curl http://localhost:8080/health
curl http://localhost:8080/metrics
```

Shut down with `SIGTERM` or `Ctrl+C`. The server drains in-flight requests for up to 30 seconds before exiting.

## Configuration

### Environment Variables

Environment variables are expanded in `config.yaml` using `${VAR_NAME}` syntax. Only set variables for providers you enable. Providers with an empty or missing key will fail with HTTP 401 and be blocked automatically.

| Variable | Provider | Free tier | Get key |
|---|---|---|---|
| `OPENROUTER_KEY` | OpenRouter (500+ models) | ✓ `:free` model pool | console.openrouter.ai |
| `GROQ_KEY` | Groq | ✓ Llama 3.x, Kimi K2 | console.groq.com |
| `NVIDIA_KEY` | NVIDIA NIM | ✓ free credits | build.nvidia.com |
| `DEEPSEEK_KEY` | DeepSeek | ✓ | platform.deepseek.com |
| `GOOGLE_KEY` | Google Gemini | ✓ Flash / Flash-lite | aistudio.google.com/apikey |
| `COHERE_KEY` | Cohere | ✓ Command R/R+ | dashboard.cohere.com |
| `MISTRAL_KEY` | Mistral AI | ✓ dev quota | console.mistral.ai |
| `ANTHROPIC_KEY` | Anthropic (Claude) | — paid | console.anthropic.com |
| `OPENAI_KEY` | OpenAI | — paid | platform.openai.com |
| `VLLM_KEY` | vLLM (local, if auth enabled) | n/a local | — |

### Flags

| Flag | Description | Default |
|---|---|---|
| `-config` | Path to YAML config file | `config.yaml` |

### Config File

```yaml
local:
  nodes:
    - id: ollama-1                       # unique identifier
      type: ollama                       # ollama | openai-compatible
      endpoint: http://localhost:11434
      timeout_ms: 3000
    - id: lmstudio-1
      type: openai-compatible
      endpoint: http://localhost:1234
      timeout_ms: 3000
    - id: vllm-1
      type: openai-compatible
      endpoint: http://localhost:8000
      api_key: ${VLLM_KEY}              # optional; supports env expansion
      timeout_ms: 3000

remote:
  providers:
    - id: openrouter-1
      type: openai-compatible
      endpoint: https://openrouter.ai/api
      api_key: ${OPENROUTER_KEY}
    - id: groq-1
      type: openai-compatible
      endpoint: https://api.groq.com/openai
      api_key: ${GROQ_KEY}
    - id: nvidia-1
      type: openai-compatible
      endpoint: https://integrate.api.nvidia.com
      api_key: ${NVIDIA_KEY}
    - id: deepseek-1
      type: openai-compatible
      endpoint: https://api.deepseek.com
      api_key: ${DEEPSEEK_KEY}
    - id: mistral-1
      type: openai-compatible
      endpoint: https://api.mistral.ai
      api_key: ${MISTRAL_KEY}
    - id: openai-1
      type: openai-compatible
      endpoint: https://api.openai.com
      api_key: ${OPENAI_KEY}
    - id: google-1
      type: google                       # openai-compatible | anthropic | google | cohere
      api_key: ${GOOGLE_KEY}
    - id: cohere-1
      type: cohere
      api_key: ${COHERE_KEY}
    - id: anthropic-1
      type: anthropic
      api_key: ${ANTHROPIC_KEY}

routing:
  latency_threshold_ms: 2000            # latency above this marks a node DEGRADED
  fallback_enabled: true                # set false to disable remote fallback entirely
```

Nodes and providers are tried in the order listed. First successful response wins. Hot-reload on save (~100ms, no restart).

### Minimal Example

Single local Ollama node, no remote fallback:

```yaml
local:
  nodes:
    - id: ollama-local
      type: ollama
      endpoint: http://localhost:11434
      timeout_ms: 5000

routing:
  latency_threshold_ms: 3000
  fallback_enabled: false
```

## Project Structure

```
cmd/
  localrouter/
    main.go              # entry point; wires all components, handles shutdown
internal/
  config/
    config.go            # YAML parsing, env expansion, validation
    watcher.go           # fsnotify hot-reload with debounce
  cache/
    cache.go             # TTL-based provider blocking, 4xx tracking
  health/
    monitor.go           # background health checks, hysteresis state machine
  metrics/
    metrics.go           # atomic counters, snapshot export
  provider/
    provider.go          # Provider interface and shared request/response types
    factory/
      factory.go         # provider instantiation from config
    openaicompat/        # OpenAI-compatible HTTP adapter (used by most providers)
    ollama/              # Ollama adapter (wraps openaicompat, custom health check)
    anthropic/           # Anthropic Messages API adapter
    google/              # Google Gemini API adapter
    cohere/              # Cohere chat API adapter
  router/
    router.go            # Tier 1 → Tier 2 routing logic, error classification
  server/
    server.go            # HTTP server setup (chi router)
    handlers.go          # GET /health, GET /metrics
    sse.go               # POST /v1/chat/completions, SSE streaming
Dockerfile               # multi-stage build; final image is Alpine + binary
config.yaml              # example configuration
```

## Releases

Releases are published at [github.com/rodrigoazlima/localrouter/releases](https://github.com/rodrigoazlima/localrouter/releases).

Versioning follows [Semantic Versioning](https://semver.org/). Each release includes:

- Pre-built binaries for Linux, macOS, and Windows
- Docker image pushed to the GitHub Container Registry (`ghcr.io/rodrigoazlima/localrouter`)
- Changelog describing breaking changes, new features, and fixes

## Contributing

1. Fork the repository and create a branch from `master`.
2. Run existing tests before making changes: `go test ./...`
3. Write tests for new behavior. Each package under `internal/` has a corresponding `*_test.go` file.
4. Keep commits focused; one logical change per commit.
5. Open a pull request against `master` with a clear description of what changes and why.

Code conventions:

- Standard Go formatting (`gofmt`). No linter exceptions without a comment explaining why.
- Errors are returned, not logged at the call site. Logging happens at the boundary (server handlers, main).
- Config validation fails fast at startup. Do not silently ignore invalid config fields.
- No external dependencies beyond those already in `go.mod` without prior discussion.

## License

MIT License

Copyright (c) 2026 rodrigoazlima

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
