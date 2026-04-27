# LocalRouter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a deterministic local-first LLM routing proxy that tries local inference nodes first, falls back to remote providers on failure, with in-memory TTL-based provider state caching and hot-reloadable YAML config.

**Architecture:** Layered Go packages wired in `main.go`: config loads YAML and watches for hot-reload; cache tracks blocked providers with TTL; health monitor checks local nodes on background goroutines with hysteresis; router implements tier1→tier2 selection; HTTP server exposes an OpenAI-compatible API with SSE streaming. All packages communicate through explicit interfaces enabling unit testing via mocks.

**Tech Stack:** Go 1.22, `github.com/go-chi/chi/v5`, `gopkg.in/yaml.v3`, `github.com/fsnotify/fsnotify`

---

## File Map

| File | Responsibility |
|------|----------------|
| `go.mod` | module + deps |
| `cmd/localrouter/main.go` | wire all components, startup/shutdown |
| `internal/config/config.go` | Config structs, YAML load, env expansion, validation |
| `internal/config/config_test.go` | config tests |
| `internal/config/watcher.go` | fsnotify hot-reload, debounce, reload worker |
| `internal/config/watcher_test.go` | watcher tests |
| `internal/cache/cache.go` | block state + TTL, 4xx counter |
| `internal/cache/cache_test.go` | cache tests |
| `internal/metrics/metrics.go` | atomic counters/gauges, Snapshot |
| `internal/metrics/metrics_test.go` | metrics tests |
| `internal/provider/provider.go` | Provider interface, Request/Response/Chunk/Usage/Message, HTTPError |
| `internal/provider/factory.go` | NewFromNode / NewFromProvider |
| `internal/provider/openaicompat/adapter.go` | OpenAI-compat HTTP adapter |
| `internal/provider/openaicompat/adapter_test.go` | openaicompat tests |
| `internal/provider/ollama/adapter.go` | wraps openaicompat, overrides HealthCheck |
| `internal/provider/ollama/adapter_test.go` | ollama tests |
| `internal/provider/anthropic/adapter.go` | Anthropic Messages API + SSE |
| `internal/provider/anthropic/adapter_test.go` | anthropic tests |
| `internal/provider/google/adapter.go` | Gemini generateContent + SSE |
| `internal/provider/google/adapter_test.go` | google tests |
| `internal/provider/cohere/adapter.go` | Cohere v2 chat + SSE |
| `internal/provider/cohere/adapter_test.go` | cohere tests |
| `internal/health/monitor.go` | background node health checks, hysteresis, lifecycle |
| `internal/health/monitor_test.go` | health tests |
| `internal/router/router.go` | tier1→tier2 routing, error classification |
| `internal/router/router_test.go` | router tests |
| `internal/server/server.go` | chi setup, Server struct |
| `internal/server/handlers.go` | GET /health, GET /metrics |
| `internal/server/sse.go` | POST /v1/chat/completions, SSE streaming |
| `internal/server/handlers_test.go` | handler tests |
| `internal/server/sse_test.go` | SSE tests |
| `Dockerfile` | multi-stage build |
| `config.yaml` | example config |

---

## Task 1: Module Init + Directory Scaffold

**Files:**
- Create: `go.mod`

- [ ] **Step 1: Init module and create directories**

```bash
cd /c/opt/GitHub/LocalRouter
go mod init github.com/rodrigoazlima/localrouter
mkdir -p cmd/localrouter
mkdir -p internal/config
mkdir -p internal/cache
mkdir -p internal/metrics
mkdir -p internal/provider/openaicompat
mkdir -p internal/provider/ollama
mkdir -p internal/provider/anthropic
mkdir -p internal/provider/google
mkdir -p internal/provider/cohere
mkdir -p internal/health
mkdir -p internal/router
mkdir -p internal/server
```

- [ ] **Step 2: Add dependencies**

```bash
go get github.com/go-chi/chi/v5
go get gopkg.in/yaml.v3
go get github.com/fsnotify/fsnotify
```

- [ ] **Step 3: Verify go.mod**

Run: `cat go.mod`
Expected: module line `github.com/rodrigoazlima/localrouter`, go 1.22, three require entries.

- [ ] **Step 4: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: init go module with chi, yaml, fsnotify"
```

---

## Task 2: Provider Interface + Core Types

**Files:**
- Create: `internal/provider/provider.go`

No tests — pure type definitions.

- [ ] **Step 1: Write provider.go**

```go
package provider

import (
	"context"
	"fmt"
	"io"
	"strings"
)

type Provider interface {
	ID() string
	Type() string
	Complete(ctx context.Context, req *Request) (*Response, error)
	Stream(ctx context.Context, req *Request) (<-chan Chunk, error)
	HealthCheck(ctx context.Context) error
}

type Request struct {
	Model    string         `json:"model"`
	Messages []Message      `json:"messages"`
	Stream   bool           `json:"stream,omitempty"`
	Raw      map[string]any `json:"-"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Response struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Content string `json:"content"`
	Usage   Usage  `json:"usage"`
}

type Chunk struct {
	Delta string
	Done  bool
	Err   error
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Body)
}

func NewHTTPError(code int, body io.Reader) *HTTPError {
	var sb strings.Builder
	io.Copy(&sb, io.LimitReader(body, 512))
	return &HTTPError{StatusCode: code, Body: sb.String()}
}
```

- [ ] **Step 2: Verify it compiles**

```bash
go build ./internal/provider/...
```

Expected: no output (success).

- [ ] **Step 3: Commit**

```bash
git add internal/provider/provider.go
git commit -m "feat: add provider interface and core types"
```

---

## Task 3: Cache

**Files:**
- Create: `internal/cache/cache.go`
- Create: `internal/cache/cache_test.go`

- [ ] **Step 1: Write failing tests**

```go
// internal/cache/cache_test.go
package cache_test

import (
	"testing"
	"time"

	"github.com/rodrigoazlima/localrouter/internal/cache"
)

func TestCache_NewProviderAvailable(t *testing.T) {
	c := cache.New()
	if c.IsBlocked("p1") {
		t.Fatal("new provider must not be blocked")
	}
}

func TestCache_BlockTierA_OneHour(t *testing.T) {
	c := cache.New()
	c.Block("p1", cache.TierA)
	if !c.IsBlocked("p1") {
		t.Fatal("must be blocked after TierA block")
	}
	e := c.Get("p1")
	diff := e.ExpiresAt.Sub(time.Now().Add(time.Hour))
	if diff > time.Second || diff < -time.Second {
		t.Fatalf("TierA TTL must be ~1h, diff=%v", diff)
	}
}

func TestCache_BlockTierB_24Hours(t *testing.T) {
	c := cache.New()
	c.Block("p1", cache.TierB)
	e := c.Get("p1")
	diff := e.ExpiresAt.Sub(time.Now().Add(24 * time.Hour))
	if diff > time.Second || diff < -time.Second {
		t.Fatalf("TierB TTL must be ~24h, diff=%v", diff)
	}
}

func TestCache_ExpiredBlock_Available(t *testing.T) {
	c := cache.New()
	c.BlockUntil("p1", cache.TierA, time.Now().Add(-time.Second))
	if c.IsBlocked("p1") {
		t.Fatal("expired block must be available")
	}
}

func TestCache_Snapshot_ImmutableCopy(t *testing.T) {
	c := cache.New()
	c.Block("p1", cache.TierA)
	snap := c.Snapshot()
	delete(snap, "p1")
	if len(c.Snapshot()) != 1 {
		t.Fatal("mutating snapshot must not affect cache")
	}
}

func TestCache_Track4xx_TierAUntilThreshold(t *testing.T) {
	c := cache.New()
	if tier := c.Track4xxAndGetTier("p1"); tier != cache.TierA {
		t.Fatalf("1st 4xx must be TierA, got %v", tier)
	}
	if tier := c.Track4xxAndGetTier("p1"); tier != cache.TierA {
		t.Fatalf("2nd 4xx must be TierA, got %v", tier)
	}
	if tier := c.Track4xxAndGetTier("p1"); tier != cache.TierB {
		t.Fatalf("3rd 4xx must be TierB, got %v", tier)
	}
}

func TestCache_Reset4xx_ResetsCounter(t *testing.T) {
	c := cache.New()
	c.Track4xxAndGetTier("p1")
	c.Track4xxAndGetTier("p1")
	c.Reset4xx("p1")
	if tier := c.Track4xxAndGetTier("p1"); tier != cache.TierA {
		t.Fatalf("after reset, 1st 4xx must be TierA, got %v", tier)
	}
}
```

- [ ] **Step 2: Run tests — expect failure**

```bash
go test ./internal/cache/... -v
```

Expected: compilation error — `cache` package does not exist yet.

- [ ] **Step 3: Write implementation**

```go
// internal/cache/cache.go
package cache

import (
	"sync"
	"time"
)

type Tier string

const (
	TierA Tier = "tier_a"
	TierB Tier = "tier_b"
)

type State string

const (
	StateAvailable State = "available"
	StateBlocked   State = "blocked"
)

type Entry struct {
	State     State
	Reason    Tier
	ExpiresAt time.Time
}

type Cache struct {
	mu             sync.RWMutex
	entries        map[string]Entry
	consecutive4xx map[string]int
}

func New() *Cache {
	return &Cache{
		entries:        make(map[string]Entry),
		consecutive4xx: make(map[string]int),
	}
}

func (c *Cache) IsBlocked(id string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[id]
	if !ok {
		return false
	}
	return e.State == StateBlocked && time.Now().Before(e.ExpiresAt)
}

func (c *Cache) Block(id string, tier Tier) {
	var ttl time.Duration
	if tier == TierA {
		ttl = time.Hour
	} else {
		ttl = 24 * time.Hour
	}
	c.BlockUntil(id, tier, time.Now().Add(ttl))
}

func (c *Cache) BlockUntil(id string, tier Tier, expiresAt time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[id] = Entry{State: StateBlocked, Reason: tier, ExpiresAt: expiresAt}
}

func (c *Cache) Get(id string) Entry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.entries[id]
}

func (c *Cache) Snapshot() map[string]Entry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]Entry, len(c.entries))
	for k, v := range c.entries {
		out[k] = v
	}
	return out
}

func (c *Cache) Track4xxAndGetTier(id string) Tier {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.consecutive4xx[id]++
	if c.consecutive4xx[id] >= 3 {
		return TierB
	}
	return TierA
}

func (c *Cache) Reset4xx(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.consecutive4xx, id)
}
```

- [ ] **Step 4: Run tests — expect pass**

```bash
go test ./internal/cache/... -v
```

Expected: all tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cache/
git commit -m "feat: add in-memory cache with TTL blocking and 4xx tracking"
```

---

## Task 4: Metrics

**Files:**
- Create: `internal/metrics/metrics.go`
- Create: `internal/metrics/metrics_test.go`

- [ ] **Step 1: Write failing tests**

```go
// internal/metrics/metrics_test.go
package metrics_test

import (
	"testing"

	"github.com/rodrigoazlima/localrouter/internal/metrics"
)

func TestCollector_ZeroOnNew(t *testing.T) {
	c := metrics.New()
	s := c.Snapshot()
	if s.LocalRequests != 0 || s.RemoteRequests != 0 || s.NoCapacity != 0 {
		t.Fatal("all counters must start at zero")
	}
}

func TestCollector_CounterIncrement(t *testing.T) {
	c := metrics.New()
	c.LocalRequests.Add(3)
	c.Tier2Failures.Add(1)
	c.NoCapacity.Add(2)
	s := c.Snapshot()
	if s.LocalRequests != 3 {
		t.Fatalf("expected 3, got %d", s.LocalRequests)
	}
	if s.Tier2Failures != 1 {
		t.Fatalf("expected 1, got %d", s.Tier2Failures)
	}
	if s.NoCapacity != 2 {
		t.Fatalf("expected 2, got %d", s.NoCapacity)
	}
}

func TestCollector_SnapshotIsImmutable(t *testing.T) {
	c := metrics.New()
	c.LocalRequests.Add(5)
	s1 := c.Snapshot()
	c.LocalRequests.Add(3)
	s2 := c.Snapshot()
	if s1.LocalRequests != 5 {
		t.Fatalf("s1 must be 5, got %d", s1.LocalRequests)
	}
	if s2.LocalRequests != 8 {
		t.Fatalf("s2 must be 8, got %d", s2.LocalRequests)
	}
}

func TestCollector_NodeOK_RecordsLatency(t *testing.T) {
	c := metrics.New()
	c.NodeOK("node-1", 42)
	s := c.Snapshot()
	n, ok := s.Nodes["node-1"]
	if !ok {
		t.Fatal("node-1 missing from snapshot")
	}
	if n.ChecksOK != 1 {
		t.Fatalf("expected 1 ok, got %d", n.ChecksOK)
	}
	if n.LatencyMs != 42 {
		t.Fatalf("expected 42ms, got %d", n.LatencyMs)
	}
}

func TestCollector_NodeFail_IncrementsFail(t *testing.T) {
	c := metrics.New()
	c.NodeFail("node-1")
	c.NodeFail("node-1")
	s := c.Snapshot()
	if s.Nodes["node-1"].ChecksFail != 2 {
		t.Fatalf("expected 2 fails, got %d", s.Nodes["node-1"].ChecksFail)
	}
}
```

- [ ] **Step 2: Run tests — expect failure**

```bash
go test ./internal/metrics/... -v
```

Expected: compilation error.

- [ ] **Step 3: Write implementation**

```go
// internal/metrics/metrics.go
package metrics

import (
	"sync"
	"sync/atomic"
)

type Collector struct {
	LocalRequests       atomic.Int64
	RemoteRequests      atomic.Int64
	Tier1Failures       atomic.Int64
	Tier2Failures       atomic.Int64
	NoCapacity          atomic.Int64
	StreamsStarted      atomic.Int64
	StreamsCompleted    atomic.Int64
	StreamsDisconnected atomic.Int64
	StreamDuration      atomic.Int64
	BlockedProviders    atomic.Int64

	mu             sync.RWMutex
	nodeChecksOK   map[string]*atomic.Int64
	nodeChecksFail map[string]*atomic.Int64
	nodeLatencyMs  map[string]*atomic.Int64
}

type Snapshot struct {
	LocalRequests       int64                   `json:"local_requests"`
	RemoteRequests      int64                   `json:"remote_requests"`
	Tier1Failures       int64                   `json:"tier1_failures"`
	Tier2Failures       int64                   `json:"tier2_failures"`
	NoCapacity          int64                   `json:"no_capacity"`
	StreamsStarted      int64                   `json:"streams_started"`
	StreamsCompleted    int64                   `json:"streams_completed"`
	StreamsDisconnected int64                   `json:"streams_disconnected"`
	StreamDurationMs    int64                   `json:"stream_duration_ms"`
	BlockedProviders    int64                   `json:"blocked_providers"`
	Nodes               map[string]NodeSnapshot `json:"nodes"`
}

type NodeSnapshot struct {
	ChecksOK   int64 `json:"checks_ok"`
	ChecksFail int64 `json:"checks_fail"`
	LatencyMs  int64 `json:"latency_ms"`
}

func New() *Collector {
	return &Collector{
		nodeChecksOK:   make(map[string]*atomic.Int64),
		nodeChecksFail: make(map[string]*atomic.Int64),
		nodeLatencyMs:  make(map[string]*atomic.Int64),
	}
}

func (c *Collector) ensureNode(id string) {
	if _, ok := c.nodeChecksOK[id]; !ok {
		c.nodeChecksOK[id] = &atomic.Int64{}
		c.nodeChecksFail[id] = &atomic.Int64{}
		c.nodeLatencyMs[id] = &atomic.Int64{}
	}
}

func (c *Collector) NodeOK(id string, latencyMs int64) {
	c.mu.Lock()
	c.ensureNode(id)
	ok := c.nodeChecksOK[id]
	lat := c.nodeLatencyMs[id]
	c.mu.Unlock()
	ok.Add(1)
	lat.Store(latencyMs)
}

func (c *Collector) NodeFail(id string) {
	c.mu.Lock()
	c.ensureNode(id)
	fail := c.nodeChecksFail[id]
	c.mu.Unlock()
	fail.Add(1)
}

func (c *Collector) Snapshot() Snapshot {
	c.mu.RLock()
	nodes := make(map[string]NodeSnapshot, len(c.nodeChecksOK))
	for id := range c.nodeChecksOK {
		nodes[id] = NodeSnapshot{
			ChecksOK:   c.nodeChecksOK[id].Load(),
			ChecksFail: c.nodeChecksFail[id].Load(),
			LatencyMs:  c.nodeLatencyMs[id].Load(),
		}
	}
	c.mu.RUnlock()
	return Snapshot{
		LocalRequests:       c.LocalRequests.Load(),
		RemoteRequests:      c.RemoteRequests.Load(),
		Tier1Failures:       c.Tier1Failures.Load(),
		Tier2Failures:       c.Tier2Failures.Load(),
		NoCapacity:          c.NoCapacity.Load(),
		StreamsStarted:      c.StreamsStarted.Load(),
		StreamsCompleted:    c.StreamsCompleted.Load(),
		StreamsDisconnected: c.StreamsDisconnected.Load(),
		StreamDurationMs:    c.StreamDuration.Load(),
		BlockedProviders:    c.BlockedProviders.Load(),
		Nodes:               nodes,
	}
}
```

- [ ] **Step 4: Run tests — expect pass**

```bash
go test ./internal/metrics/... -v
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/metrics/
git commit -m "feat: add metrics collector with atomic counters and node gauges"
```

---

## Task 5: Config Load

**Files:**
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`

- [ ] **Step 1: Write failing tests**

```go
// internal/config/config_test.go
package config_test

import (
	"os"
	"testing"

	"github.com/rodrigoazlima/localrouter/internal/config"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "config*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(content)
	f.Close()
	return f.Name()
}

func TestLoad_ValidConfig(t *testing.T) {
	path := writeConfig(t, `
local:
  nodes:
    - id: ollama-1
      type: ollama
      endpoint: http://localhost:11434
      timeout_ms: 3000
remote:
  providers:
    - id: openai-1
      type: openai-compatible
      endpoint: https://api.openai.com
      api_key: sk-test
routing:
  latency_threshold_ms: 2000
  fallback_enabled: true
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Local.Nodes) != 1 || cfg.Local.Nodes[0].ID != "ollama-1" {
		t.Fatalf("unexpected nodes: %+v", cfg.Local.Nodes)
	}
	if len(cfg.Remote.Providers) != 1 || cfg.Remote.Providers[0].ID != "openai-1" {
		t.Fatalf("unexpected providers: %+v", cfg.Remote.Providers)
	}
}

func TestLoad_EnvExpansion(t *testing.T) {
	t.Setenv("TEST_KEY", "sk-expanded")
	path := writeConfig(t, `
local:
  nodes: []
remote:
  providers:
    - id: openai-1
      type: openai-compatible
      endpoint: https://api.openai.com
      api_key: ${TEST_KEY}
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Remote.Providers[0].APIKey != "sk-expanded" {
		t.Fatalf("env not expanded: %s", cfg.Remote.Providers[0].APIKey)
	}
}

func TestLoad_DuplicateID_Error(t *testing.T) {
	path := writeConfig(t, `
local:
  nodes:
    - id: dup
      type: ollama
      endpoint: http://localhost:11434
remote:
  providers:
    - id: dup
      type: openai-compatible
      endpoint: https://api.openai.com
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for duplicate id")
	}
}

func TestLoad_UnknownType_Error(t *testing.T) {
	path := writeConfig(t, `
local:
  nodes:
    - id: n1
      type: unknown
      endpoint: http://localhost:11434
remote:
  providers: []
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for unknown type")
	}
}

func TestLoad_MissingEndpoint_Error(t *testing.T) {
	path := writeConfig(t, `
local:
  nodes:
    - id: n1
      type: ollama
remote:
  providers: []
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for missing endpoint")
	}
}

func TestNodeConfig_Redacted_DoesNotMutate(t *testing.T) {
	n := config.NodeConfig{ID: "n1", APIKey: "secret"}
	r := n.Redacted()
	if r.APIKey != "[REDACTED]" {
		t.Fatalf("expected [REDACTED], got %s", r.APIKey)
	}
	if n.APIKey != "secret" {
		t.Fatal("Redacted must not modify original")
	}
}
```

- [ ] **Step 2: Run tests — expect failure**

```bash
go test ./internal/config/... -v
```

Expected: compilation error.

- [ ] **Step 3: Write implementation**

```go
// internal/config/config.go
package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Version int           `yaml:"-"`
	Local   LocalConfig   `yaml:"local"`
	Remote  RemoteConfig  `yaml:"remote"`
	Routing RoutingConfig `yaml:"routing"`
}

type LocalConfig struct {
	Nodes []NodeConfig `yaml:"nodes"`
}

type RemoteConfig struct {
	Providers []ProviderConfig `yaml:"providers"`
}

type NodeConfig struct {
	ID        string `yaml:"id"`
	Type      string `yaml:"type"`
	Endpoint  string `yaml:"endpoint"`
	APIKey    string `yaml:"api_key"`
	TimeoutMs int    `yaml:"timeout_ms"`
}

type ProviderConfig struct {
	ID       string `yaml:"id"`
	Type     string `yaml:"type"`
	Endpoint string `yaml:"endpoint"`
	APIKey   string `yaml:"api_key"`
}

type RoutingConfig struct {
	LatencyThresholdMs int  `yaml:"latency_threshold_ms"`
	FallbackEnabled    bool `yaml:"fallback_enabled"`
}

var validNodeTypes = map[string]bool{
	"ollama": true, "openai-compatible": true,
}
var validProviderTypes = map[string]bool{
	"openai-compatible": true, "anthropic": true, "google": true, "cohere": true,
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	expandEnv(&cfg)
	if err := validate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func expandEnv(cfg *Config) {
	for i := range cfg.Local.Nodes {
		cfg.Local.Nodes[i].APIKey = expand(cfg.Local.Nodes[i].APIKey)
		cfg.Local.Nodes[i].Endpoint = expand(cfg.Local.Nodes[i].Endpoint)
	}
	for i := range cfg.Remote.Providers {
		cfg.Remote.Providers[i].APIKey = expand(cfg.Remote.Providers[i].APIKey)
		cfg.Remote.Providers[i].Endpoint = expand(cfg.Remote.Providers[i].Endpoint)
	}
}

func expand(s string) string {
	if !strings.Contains(s, "${") {
		return s
	}
	return os.ExpandEnv(s)
}

func validate(cfg *Config) error {
	ids := make(map[string]bool)
	for _, n := range cfg.Local.Nodes {
		if n.ID == "" {
			return fmt.Errorf("node missing id")
		}
		if ids[n.ID] {
			return fmt.Errorf("duplicate id: %s", n.ID)
		}
		ids[n.ID] = true
		if !validNodeTypes[n.Type] {
			return fmt.Errorf("unknown node type %q for %s", n.Type, n.ID)
		}
		if n.Endpoint == "" {
			return fmt.Errorf("node %s missing endpoint", n.ID)
		}
	}
	for _, p := range cfg.Remote.Providers {
		if p.ID == "" {
			return fmt.Errorf("provider missing id")
		}
		if ids[p.ID] {
			return fmt.Errorf("duplicate id: %s", p.ID)
		}
		ids[p.ID] = true
		if !validProviderTypes[p.Type] {
			return fmt.Errorf("unknown provider type %q for %s", p.Type, p.ID)
		}
	}
	return nil
}

func (n NodeConfig) Redacted() NodeConfig {
	if n.APIKey != "" {
		n.APIKey = "[REDACTED]"
	}
	return n
}

func (p ProviderConfig) Redacted() ProviderConfig {
	if p.APIKey != "" {
		p.APIKey = "[REDACTED]"
	}
	return p
}
```

- [ ] **Step 4: Run tests — expect pass**

```bash
go test ./internal/config/... -v
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat: add config load with YAML parsing, env expansion, and validation"
```

---

## Task 6: Config Watcher

**Files:**
- Create: `internal/config/watcher.go`
- Create: `internal/config/watcher_test.go`

- [ ] **Step 1: Write failing tests**

```go
// internal/config/watcher_test.go
package config_test

import (
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rodrigoazlima/localrouter/internal/config"
)

func TestWatcher_CallsOnChangeAfterFileWrite(t *testing.T) {
	path := writeConfig(t, `
local:
  nodes:
    - id: node-1
      type: ollama
      endpoint: http://localhost:11434
remote:
  providers: []
`)
	initial, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	var callCount atomic.Int32
	w, err := config.NewWatcher(path, initial, func(cfg *config.Config) {
		callCount.Add(1)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()

	os.WriteFile(path, []byte(`
local:
  nodes:
    - id: node-1
      type: ollama
      endpoint: http://localhost:11434
    - id: node-2
      type: openai-compatible
      endpoint: http://localhost:1234
remote:
  providers: []
`), 0644)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if callCount.Load() > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("onChange not called within 2s")
}

func TestWatcher_InvalidConfig_DoesNotCallOnChange(t *testing.T) {
	path := writeConfig(t, `
local:
  nodes:
    - id: node-1
      type: ollama
      endpoint: http://localhost:11434
remote:
  providers: []
`)
	initial, _ := config.Load(path)
	var callCount atomic.Int32
	w, _ := config.NewWatcher(path, initial, func(_ *config.Config) { callCount.Add(1) })
	defer w.Stop()

	os.WriteFile(path, []byte(`this: is: invalid: yaml: ::::`), 0644)
	time.Sleep(300 * time.Millisecond)
	if callCount.Load() > 0 {
		t.Fatal("onChange must not fire on invalid config")
	}
}
```

- [ ] **Step 2: Run — expect failure**

```bash
go test ./internal/config/... -run TestWatcher -v
```

Expected: compilation error.

- [ ] **Step 3: Write implementation**

```go
// internal/config/watcher.go
package config

import (
	"log"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

type Watcher struct {
	path     string
	mu       sync.Mutex
	version  int
	current  *Config
	onChange func(*Config)
	stop     chan struct{}
}

func NewWatcher(path string, initial *Config, onChange func(*Config)) (*Watcher, error) {
	w := &Watcher{
		path:     path,
		version:  1,
		current:  initial,
		onChange: onChange,
		stop:     make(chan struct{}),
	}
	go w.watchLoop()
	go w.periodicRefresh()
	return w, nil
}

func (w *Watcher) Stop() { close(w.stop) }

func (w *Watcher) Current() *Config {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.current
}

func (w *Watcher) watchLoop() {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("config watcher: fsnotify init: %v", err)
		return
	}
	defer fw.Close()
	fw.Add(w.path)

	reload := make(chan struct{}, 1)
	var debounce *time.Timer

	for {
		select {
		case <-w.stop:
			return
		case _, ok := <-fw.Events:
			if !ok {
				return
			}
			if debounce != nil {
				debounce.Stop()
			}
			debounce = time.AfterFunc(100*time.Millisecond, func() {
				select {
				case reload <- struct{}{}:
				default:
				}
			})
		case <-reload:
			w.reload()
		case err := <-fw.Errors:
			log.Printf("config watcher: %v", err)
		}
	}
}

func (w *Watcher) periodicRefresh() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
			w.reload()
		}
	}
}

func (w *Watcher) reload() {
	cfg, err := Load(w.path)
	if err != nil {
		log.Printf("config reload failed (keeping current): %v", err)
		return
	}
	w.mu.Lock()
	w.version++
	cfg.Version = w.version
	w.current = cfg
	cb := w.onChange
	w.mu.Unlock()
	cb(cfg)
}
```

- [ ] **Step 4: Run tests — expect pass**

```bash
go test ./internal/config/... -v
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/watcher.go internal/config/watcher_test.go
git commit -m "feat: add config watcher with fsnotify hot-reload and debounce"
```

---

## Task 7: OpenAI-Compatible Adapter

**Files:**
- Create: `internal/provider/openaicompat/adapter.go`
- Create: `internal/provider/openaicompat/adapter_test.go`

- [ ] **Step 1: Write failing tests**

```go
// internal/provider/openaicompat/adapter_test.go
package openaicompat_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rodrigoazlima/localrouter/internal/provider"
	"github.com/rodrigoazlima/localrouter/internal/provider/openaicompat"
)

func TestComplete_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("missing auth header")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":    "chatcmpl-1",
			"model": "gpt-4o",
			"choices": []map[string]any{
				{"message": map[string]any{"content": "Hello!"}},
			},
			"usage": map[string]any{"prompt_tokens": 5, "completion_tokens": 3},
		})
	}))
	defer srv.Close()

	a := openaicompat.New("test", srv.URL, "test-key", 3000)
	resp, err := a.Complete(context.Background(), &provider.Request{
		Model:    "gpt-4o",
		Messages: []provider.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "Hello!" {
		t.Fatalf("expected Hello!, got %s", resp.Content)
	}
	if resp.Usage.PromptTokens != 5 {
		t.Fatalf("expected 5 prompt tokens, got %d", resp.Usage.PromptTokens)
	}
}

func TestComplete_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	a := openaicompat.New("test", srv.URL, "", 3000)
	_, err := a.Complete(context.Background(), &provider.Request{
		Model:    "gpt-4o",
		Messages: []provider.Message{{Role: "user", Content: "Hi"}},
	})
	var httpErr *provider.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected HTTPError, got %T: %v", err, err)
	}
	if httpErr.StatusCode != 429 {
		t.Fatalf("expected 429, got %d", httpErr.StatusCode)
	}
}

func TestStream_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"He\"}}]}\n\n")
		w.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"llo\"}}]}\n\n")
		w.WriteString("data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	a := openaicompat.New("test", srv.URL, "", 3000)
	ch, err := a.Stream(context.Background(), &provider.Request{
		Model:    "gpt-4o",
		Messages: []provider.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result strings.Builder
	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
		result.WriteString(chunk.Delta)
	}
	if result.String() != "Hello" {
		t.Fatalf("expected Hello, got %s", result.String())
	}
}

func TestHealthCheck_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a := openaicompat.New("test", srv.URL, "", 3000)
	if err := a.HealthCheck(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
```

- [ ] **Step 2: Run — expect failure**

```bash
go test ./internal/provider/openaicompat/... -v
```

Expected: compilation error.

- [ ] **Step 3: Write implementation**

```go
// internal/provider/openaicompat/adapter.go
package openaicompat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rodrigoazlima/localrouter/internal/provider"
)

type Adapter struct {
	id       string
	endpoint string
	apiKey   string
	client   *http.Client
}

func New(id, endpoint, apiKey string, timeoutMs int) *Adapter {
	if timeoutMs <= 0 {
		timeoutMs = 30000
	}
	return &Adapter{
		id:       id,
		endpoint: strings.TrimRight(endpoint, "/"),
		apiKey:   apiKey,
		client:   &http.Client{Timeout: time.Duration(timeoutMs) * time.Millisecond},
	}
}

func (a *Adapter) ID() string      { return a.id }
func (a *Adapter) Type() string    { return "openai-compatible" }
func (a *Adapter) Endpoint() string { return a.endpoint }

type oaiRequest struct {
	Model    string       `json:"model"`
	Messages []oaiMessage `json:"messages"`
	Stream   bool         `json:"stream,omitempty"`
}
type oaiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type oaiResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct{ Content string `json:"content"` } `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}
type streamChunk struct {
	Choices []struct {
		Delta struct{ Content string `json:"content"` } `json:"delta"`
	} `json:"choices"`
}

func (a *Adapter) Complete(ctx context.Context, req *provider.Request) (*provider.Response, error) {
	body, _ := json.Marshal(oaiRequest{Model: req.Model, Messages: toOAI(req.Messages)})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.endpoint+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if a.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+a.apiKey)
	}
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, provider.NewHTTPError(resp.StatusCode, resp.Body)
	}
	var r oaiResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	content := ""
	if len(r.Choices) > 0 {
		content = r.Choices[0].Message.Content
	}
	return &provider.Response{
		ID: r.ID, Model: r.Model, Content: content,
		Usage: provider.Usage{PromptTokens: r.Usage.PromptTokens, CompletionTokens: r.Usage.CompletionTokens},
	}, nil
}

func (a *Adapter) Stream(ctx context.Context, req *provider.Request) (<-chan provider.Chunk, error) {
	body, _ := json.Marshal(oaiRequest{Model: req.Model, Messages: toOAI(req.Messages), Stream: true})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.endpoint+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if a.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+a.apiKey)
	}
	resp, err := (&http.Client{}).Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, provider.NewHTTPError(resp.StatusCode, resp.Body)
	}
	ch := make(chan provider.Chunk, 10)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return
			default:
			}
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				return
			}
			var sc streamChunk
			if err := json.Unmarshal([]byte(data), &sc); err != nil {
				ch <- provider.Chunk{Err: fmt.Errorf("parse stream: %w", err)}
				return
			}
			if len(sc.Choices) > 0 && sc.Choices[0].Delta.Content != "" {
				ch <- provider.Chunk{Delta: sc.Choices[0].Delta.Content}
			}
		}
		if err := scanner.Err(); err != nil {
			ch <- provider.Chunk{Err: err}
		}
	}()
	return ch, nil
}

func (a *Adapter) HealthCheck(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.endpoint+"/v1/models", nil)
	if err != nil {
		return err
	}
	if a.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+a.apiKey)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check: HTTP %d", resp.StatusCode)
	}
	return nil
}

func toOAI(msgs []provider.Message) []oaiMessage {
	out := make([]oaiMessage, len(msgs))
	for i, m := range msgs {
		out[i] = oaiMessage{Role: m.Role, Content: m.Content}
	}
	return out
}

var _ provider.Provider = (*Adapter)(nil)
```

- [ ] **Step 4: Run tests — expect pass**

```bash
go test ./internal/provider/openaicompat/... -v
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/provider/openaicompat/
git commit -m "feat: add openai-compatible provider adapter"
```

---

## Task 8: Ollama Adapter

**Files:**
- Create: `internal/provider/ollama/adapter.go`
- Create: `internal/provider/ollama/adapter_test.go`

- [ ] **Step 1: Write failing tests**

```go
// internal/provider/ollama/adapter_test.go
package ollama_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rodrigoazlima/localrouter/internal/provider"
	"github.com/rodrigoazlima/localrouter/internal/provider/ollama"
)

func TestOllamaHealthCheck_UsesApiTags(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			called = true
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	a := ollama.New("ollama-1", srv.URL, "", 3000)
	if err := a.HealthCheck(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("must call /api/tags")
	}
}

func TestOllamaHealthCheck_Non200_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	a := ollama.New("ollama-1", srv.URL, "", 3000)
	if err := a.HealthCheck(context.Background()); err == nil {
		t.Fatal("expected error for non-200")
	}
}

func TestOllamaType(t *testing.T) {
	a := ollama.New("x", "http://localhost", "", 3000)
	if a.Type() != "ollama" {
		t.Fatalf("expected ollama, got %s", a.Type())
	}
}

func TestOllama_ImplementsProvider(t *testing.T) {
	var _ provider.Provider = ollama.New("x", "http://localhost", "", 3000)
}
```

- [ ] **Step 2: Run — expect failure**

```bash
go test ./internal/provider/ollama/... -v
```

Expected: compilation error.

- [ ] **Step 3: Write implementation**

```go
// internal/provider/ollama/adapter.go
package ollama

import (
	"context"
	"fmt"
	"net/http"

	"github.com/rodrigoazlima/localrouter/internal/provider"
	"github.com/rodrigoazlima/localrouter/internal/provider/openaicompat"
)

type Adapter struct {
	*openaicompat.Adapter
	httpClient *http.Client
}

func New(id, endpoint, apiKey string, timeoutMs int) *Adapter {
	return &Adapter{
		Adapter:    openaicompat.New(id, endpoint, apiKey, timeoutMs),
		httpClient: &http.Client{},
	}
}

func (a *Adapter) Type() string { return "ollama" }

func (a *Adapter) HealthCheck(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.Endpoint()+"/api/tags", nil)
	if err != nil {
		return err
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama health: HTTP %d", resp.StatusCode)
	}
	return nil
}

var _ provider.Provider = (*Adapter)(nil)
```

- [ ] **Step 4: Run tests — expect pass**

```bash
go test ./internal/provider/ollama/... -v
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/provider/ollama/
git commit -m "feat: add ollama adapter with /api/tags health check"
```

---

## Task 9: Anthropic Adapter

**Files:**
- Create: `internal/provider/anthropic/adapter.go`
- Create: `internal/provider/anthropic/adapter_test.go`

Wire format: `POST /v1/messages`, headers `x-api-key` + `anthropic-version: 2023-06-01`, body `{model, messages, max_tokens}`, response `{id, content:[{type,text}], usage:{input_tokens,output_tokens}}`. Stream events: `event: content_block_delta` + `data: {delta:{type:"text_delta",text:"..."}}`, then `event: message_stop`.

- [ ] **Step 1: Write failing tests**

```go
// internal/provider/anthropic/adapter_test.go
package anthropic_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rodrigoazlima/localrouter/internal/provider"
	"github.com/rodrigoazlima/localrouter/internal/provider/anthropic"
)

func TestComplete_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("missing x-api-key")
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("missing anthropic-version")
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_1", "model": "claude-3-5-sonnet-20241022",
			"content": []map[string]any{{"type": "text", "text": "Hello!"}},
			"usage":   map[string]any{"input_tokens": 5, "output_tokens": 3},
		})
	}))
	defer srv.Close()

	a := anthropic.New("ant-1", "test-key", srv.URL)
	resp, err := a.Complete(context.Background(), &provider.Request{
		Model:    "claude-3-5-sonnet-20241022",
		Messages: []provider.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "Hello!" {
		t.Fatalf("expected Hello!, got %s", resp.Content)
	}
	if resp.Usage.PromptTokens != 5 {
		t.Fatalf("expected 5, got %d", resp.Usage.PromptTokens)
	}
}

func TestComplete_401_ReturnsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	a := anthropic.New("ant-1", "bad", srv.URL)
	_, err := a.Complete(context.Background(), &provider.Request{
		Model: "claude-3-5-sonnet-20241022", Messages: []provider.Message{{Role: "user", Content: "Hi"}},
	})
	var httpErr *provider.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != 401 {
		t.Fatalf("expected 401 HTTPError, got %v", err)
	}
}

func TestStream_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteString("event: content_block_delta\ndata: {\"delta\":{\"type\":\"text_delta\",\"text\":\"He\"}}\n\n")
		w.WriteString("event: content_block_delta\ndata: {\"delta\":{\"type\":\"text_delta\",\"text\":\"llo\"}}\n\n")
		w.WriteString("event: message_stop\ndata: {}\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	a := anthropic.New("ant-1", "key", srv.URL)
	ch, err := a.Stream(context.Background(), &provider.Request{
		Model: "claude-3-5-sonnet-20241022", Messages: []provider.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result strings.Builder
	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
		result.WriteString(chunk.Delta)
	}
	if result.String() != "Hello" {
		t.Fatalf("expected Hello, got %s", result.String())
	}
}
```

- [ ] **Step 2: Run — expect failure**

```bash
go test ./internal/provider/anthropic/... -v
```

Expected: compilation error.

- [ ] **Step 3: Write implementation**

```go
// internal/provider/anthropic/adapter.go
package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/rodrigoazlima/localrouter/internal/provider"
)

const antVersion = "2023-06-01"
const defaultMaxTokens = 1024

type Adapter struct {
	id       string
	apiKey   string
	endpoint string
	client   *http.Client
}

func New(id, apiKey, endpoint string) *Adapter {
	if endpoint == "" {
		endpoint = "https://api.anthropic.com"
	}
	return &Adapter{id: id, apiKey: apiKey,
		endpoint: strings.TrimRight(endpoint, "/"), client: &http.Client{}}
}

func (a *Adapter) ID() string   { return a.id }
func (a *Adapter) Type() string { return "anthropic" }

type antReq struct {
	Model     string  `json:"model"`
	Messages  []antMsg `json:"messages"`
	MaxTokens int     `json:"max_tokens"`
	Stream    bool    `json:"stream,omitempty"`
}
type antMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type antResp struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func (a *Adapter) Complete(ctx context.Context, req *provider.Request) (*provider.Response, error) {
	body, _ := json.Marshal(antReq{Model: req.Model, Messages: toAnt(req.Messages), MaxTokens: defaultMaxTokens})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", a.apiKey)
	httpReq.Header.Set("anthropic-version", antVersion)
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, provider.NewHTTPError(resp.StatusCode, resp.Body)
	}
	var r antResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	var content string
	for _, c := range r.Content {
		if c.Type == "text" {
			content += c.Text
		}
	}
	return &provider.Response{
		ID: r.ID, Model: r.Model, Content: content,
		Usage: provider.Usage{PromptTokens: r.Usage.InputTokens, CompletionTokens: r.Usage.OutputTokens},
	}, nil
}

type antDelta struct {
	Delta struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"delta"`
}

func (a *Adapter) Stream(ctx context.Context, req *provider.Request) (<-chan provider.Chunk, error) {
	body, _ := json.Marshal(antReq{Model: req.Model, Messages: toAnt(req.Messages), MaxTokens: defaultMaxTokens, Stream: true})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", a.apiKey)
	httpReq.Header.Set("anthropic-version", antVersion)
	resp, err := (&http.Client{}).Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, provider.NewHTTPError(resp.StatusCode, resp.Body)
	}
	ch := make(chan provider.Chunk, 10)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		var lastEvent string
		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return
			default:
			}
			line := scanner.Text()
			if strings.HasPrefix(line, "event: ") {
				lastEvent = strings.TrimPrefix(line, "event: ")
				if lastEvent == "message_stop" {
					return
				}
				continue
			}
			if lastEvent != "content_block_delta" || !strings.HasPrefix(line, "data: ") {
				continue
			}
			var d antDelta
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &d); err != nil {
				ch <- provider.Chunk{Err: fmt.Errorf("parse stream: %w", err)}
				return
			}
			if d.Delta.Type == "text_delta" && d.Delta.Text != "" {
				ch <- provider.Chunk{Delta: d.Delta.Text}
			}
		}
	}()
	return ch, nil
}

func (a *Adapter) HealthCheck(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.endpoint+"/v1/models", nil)
	if err != nil {
		return err
	}
	req.Header.Set("x-api-key", a.apiKey)
	req.Header.Set("anthropic-version", antVersion)
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return provider.NewHTTPError(resp.StatusCode, nil)
	}
	return nil
}

func toAnt(msgs []provider.Message) []antMsg {
	out := make([]antMsg, len(msgs))
	for i, m := range msgs {
		out[i] = antMsg{Role: m.Role, Content: m.Content}
	}
	return out
}

var _ provider.Provider = (*Adapter)(nil)
```

- [ ] **Step 4: Run tests — expect pass**

```bash
go test ./internal/provider/anthropic/... -v
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/provider/anthropic/
git commit -m "feat: add anthropic provider adapter"
```

---

## Task 10: Google Adapter

**Files:**
- Create: `internal/provider/google/adapter.go`
- Create: `internal/provider/google/adapter_test.go`

Wire format: `POST /v1beta/models/{model}:generateContent?key={apiKey}`, body `{contents:[{role,parts:[{text}]}]}`, response `{candidates:[{content:{parts:[{text}]}}],usageMetadata:{promptTokenCount,candidatesTokenCount}}`. Stream: same path with `:streamGenerateContent`, SSE `data: {candidates:[...]}`.

- [ ] **Step 1: Write failing tests**

```go
// internal/provider/google/adapter_test.go
package google_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rodrigoazlima/localrouter/internal/provider"
	"github.com/rodrigoazlima/localrouter/internal/provider/google"
)

func TestComplete_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "key=test-key") {
			t.Errorf("missing key query param: %s", r.URL.RawQuery)
		}
		if !strings.Contains(r.URL.Path, ":generateContent") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"candidates": []map[string]any{
				{"content": map[string]any{"parts": []map[string]any{{"text": "Hello!"}}, "role": "model"}},
			},
			"usageMetadata": map[string]any{"promptTokenCount": 5, "candidatesTokenCount": 3},
		})
	}))
	defer srv.Close()

	a := google.New("g-1", "test-key", srv.URL)
	resp, err := a.Complete(context.Background(), &provider.Request{
		Model:    "gemini-1.5-flash",
		Messages: []provider.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "Hello!" {
		t.Fatalf("expected Hello!, got %s", resp.Content)
	}
}

func TestComplete_429_ReturnsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "quota exceeded", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	a := google.New("g-1", "key", srv.URL)
	_, err := a.Complete(context.Background(), &provider.Request{
		Model: "gemini-1.5-flash", Messages: []provider.Message{{Role: "user", Content: "Hi"}},
	})
	var httpErr *provider.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != 429 {
		t.Fatalf("expected 429 HTTPError, got %v", err)
	}
}

func TestStream_UsesStreamGenerateContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, ":streamGenerateContent") {
			t.Errorf("expected streamGenerateContent, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteString("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Hi\"}]}}]}\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	a := google.New("g-1", "key", srv.URL)
	ch, err := a.Stream(context.Background(), &provider.Request{
		Model: "gemini-1.5-flash", Messages: []provider.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got strings.Builder
	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
		got.WriteString(chunk.Delta)
	}
	if got.String() != "Hi" {
		t.Fatalf("expected Hi, got %s", got.String())
	}
}
```

- [ ] **Step 2: Run — expect failure**

```bash
go test ./internal/provider/google/... -v
```

Expected: compilation error.

- [ ] **Step 3: Write implementation**

```go
// internal/provider/google/adapter.go
package google

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/rodrigoazlima/localrouter/internal/provider"
)

type Adapter struct {
	id       string
	apiKey   string
	endpoint string
	client   *http.Client
}

func New(id, apiKey, endpoint string) *Adapter {
	if endpoint == "" {
		endpoint = "https://generativelanguage.googleapis.com"
	}
	return &Adapter{id: id, apiKey: apiKey,
		endpoint: strings.TrimRight(endpoint, "/"), client: &http.Client{}}
}

func (a *Adapter) ID() string   { return a.id }
func (a *Adapter) Type() string { return "google" }

type geminiReq struct {
	Contents []geminiContent `json:"contents"`
}
type geminiContent struct {
	Role  string       `json:"role"`
	Parts []geminiPart `json:"parts"`
}
type geminiPart struct{ Text string `json:"text"` }

type geminiResp struct {
	Candidates []struct {
		Content struct{ Parts []geminiPart `json:"parts"` } `json:"content"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
	} `json:"usageMetadata"`
}

func (a *Adapter) url(model, action string) string {
	return fmt.Sprintf("%s/v1beta/models/%s:%s?key=%s", a.endpoint, model, action, a.apiKey)
}

func (a *Adapter) Complete(ctx context.Context, req *provider.Request) (*provider.Response, error) {
	body, _ := json.Marshal(geminiReq{Contents: toGemini(req.Messages)})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.url(req.Model, "generateContent"), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, provider.NewHTTPError(resp.StatusCode, resp.Body)
	}
	var r geminiResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	var content string
	if len(r.Candidates) > 0 {
		for _, p := range r.Candidates[0].Content.Parts {
			content += p.Text
		}
	}
	return &provider.Response{
		Model: req.Model, Content: content,
		Usage: provider.Usage{PromptTokens: r.UsageMetadata.PromptTokenCount, CompletionTokens: r.UsageMetadata.CandidatesTokenCount},
	}, nil
}

func (a *Adapter) Stream(ctx context.Context, req *provider.Request) (<-chan provider.Chunk, error) {
	body, _ := json.Marshal(geminiReq{Contents: toGemini(req.Messages)})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.url(req.Model, "streamGenerateContent"), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{}).Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, provider.NewHTTPError(resp.StatusCode, resp.Body)
	}
	ch := make(chan provider.Chunk, 10)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return
			default:
			}
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var r geminiResp
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &r); err != nil {
				ch <- provider.Chunk{Err: fmt.Errorf("parse stream: %w", err)}
				return
			}
			if len(r.Candidates) > 0 {
				for _, p := range r.Candidates[0].Content.Parts {
					if p.Text != "" {
						ch <- provider.Chunk{Delta: p.Text}
					}
				}
			}
		}
	}()
	return ch, nil
}

func (a *Adapter) HealthCheck(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/v1beta/models?key=%s", a.endpoint, a.apiKey), nil)
	if err != nil {
		return err
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return provider.NewHTTPError(resp.StatusCode, nil)
	}
	return nil
}

func toGemini(msgs []provider.Message) []geminiContent {
	out := make([]geminiContent, len(msgs))
	for i, m := range msgs {
		role := m.Role
		if role == "assistant" {
			role = "model"
		}
		out[i] = geminiContent{Role: role, Parts: []geminiPart{{Text: m.Content}}}
	}
	return out
}

var _ provider.Provider = (*Adapter)(nil)
```

- [ ] **Step 4: Run tests — expect pass**

```bash
go test ./internal/provider/google/... -v
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/provider/google/
git commit -m "feat: add google gemini provider adapter"
```

---

## Task 11: Cohere Adapter

**Files:**
- Create: `internal/provider/cohere/adapter.go`
- Create: `internal/provider/cohere/adapter_test.go`

Wire format: `POST /v2/chat`, `Authorization: Bearer {key}`, body `{model, messages, stream?}`, response `{id, message:{content:[{type,text}]}, usage:{billed_units:{input_tokens,output_tokens}}}`. Stream events: `event: content-delta` + `data: {delta:{message:{content:{delta:{text}}}}}`, then `event: message-end`.

- [ ] **Step 1: Write failing tests**

```go
// internal/provider/cohere/adapter_test.go
package cohere_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rodrigoazlima/localrouter/internal/provider"
	"github.com/rodrigoazlima/localrouter/internal/provider/cohere"
)

func TestComplete_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("missing auth header")
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id":      "chat-1",
			"message": map[string]any{"content": []map[string]any{{"type": "text", "text": "Hello!"}}},
			"usage":   map[string]any{"billed_units": map[string]any{"input_tokens": 5, "output_tokens": 3}},
		})
	}))
	defer srv.Close()

	a := cohere.New("coh-1", "test-key", srv.URL)
	resp, err := a.Complete(context.Background(), &provider.Request{
		Model: "command-r", Messages: []provider.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "Hello!" {
		t.Fatalf("expected Hello!, got %s", resp.Content)
	}
}

func TestComplete_429_ReturnsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limit", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	a := cohere.New("coh-1", "key", srv.URL)
	_, err := a.Complete(context.Background(), &provider.Request{
		Model: "command-r", Messages: []provider.Message{{Role: "user", Content: "Hi"}},
	})
	var httpErr *provider.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != 429 {
		t.Fatalf("expected 429 HTTPError, got %v", err)
	}
}

func TestStream_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteString("event: content-delta\ndata: {\"delta\":{\"message\":{\"content\":{\"delta\":{\"text\":\"He\"}}}}}\n\n")
		w.WriteString("event: content-delta\ndata: {\"delta\":{\"message\":{\"content\":{\"delta\":{\"text\":\"llo\"}}}}}\n\n")
		w.WriteString("event: message-end\ndata: {\"finish_reason\":\"COMPLETE\"}\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	a := cohere.New("coh-1", "key", srv.URL)
	ch, err := a.Stream(context.Background(), &provider.Request{
		Model: "command-r", Messages: []provider.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got strings.Builder
	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
		got.WriteString(chunk.Delta)
	}
	if got.String() != "Hello" {
		t.Fatalf("expected Hello, got %s", got.String())
	}
}
```

- [ ] **Step 2: Run — expect failure**

```bash
go test ./internal/provider/cohere/... -v
```

Expected: compilation error.

- [ ] **Step 3: Write implementation**

```go
// internal/provider/cohere/adapter.go
package cohere

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/rodrigoazlima/localrouter/internal/provider"
)

type Adapter struct {
	id       string
	apiKey   string
	endpoint string
	client   *http.Client
}

func New(id, apiKey, endpoint string) *Adapter {
	if endpoint == "" {
		endpoint = "https://api.cohere.com"
	}
	return &Adapter{id: id, apiKey: apiKey,
		endpoint: strings.TrimRight(endpoint, "/"), client: &http.Client{}}
}

func (a *Adapter) ID() string   { return a.id }
func (a *Adapter) Type() string { return "cohere" }

type cohReq struct {
	Model    string    `json:"model"`
	Messages []cohMsg  `json:"messages"`
	Stream   bool      `json:"stream,omitempty"`
}
type cohMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type cohResp struct {
	ID      string `json:"id"`
	Message struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
	Usage struct {
		BilledUnits struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"billed_units"`
	} `json:"usage"`
}

func (a *Adapter) Complete(ctx context.Context, req *provider.Request) (*provider.Response, error) {
	body, _ := json.Marshal(cohReq{Model: req.Model, Messages: toCoh(req.Messages)})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint+"/v2/chat", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+a.apiKey)
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, provider.NewHTTPError(resp.StatusCode, resp.Body)
	}
	var r cohResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	var content string
	for _, c := range r.Message.Content {
		if c.Type == "text" {
			content += c.Text
		}
	}
	return &provider.Response{
		ID: r.ID, Model: req.Model, Content: content,
		Usage: provider.Usage{PromptTokens: r.Usage.BilledUnits.InputTokens, CompletionTokens: r.Usage.BilledUnits.OutputTokens},
	}, nil
}

type cohDelta struct {
	Delta struct {
		Message struct {
			Content struct {
				Delta struct{ Text string `json:"text"` } `json:"delta"`
			} `json:"content"`
		} `json:"message"`
	} `json:"delta"`
}

func (a *Adapter) Stream(ctx context.Context, req *provider.Request) (<-chan provider.Chunk, error) {
	body, _ := json.Marshal(cohReq{Model: req.Model, Messages: toCoh(req.Messages), Stream: true})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint+"/v2/chat", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+a.apiKey)
	resp, err := (&http.Client{}).Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, provider.NewHTTPError(resp.StatusCode, resp.Body)
	}
	ch := make(chan provider.Chunk, 10)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		var lastEvent string
		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return
			default:
			}
			line := scanner.Text()
			if strings.HasPrefix(line, "event: ") {
				lastEvent = strings.TrimPrefix(line, "event: ")
				if lastEvent == "message-end" {
					return
				}
				continue
			}
			if lastEvent != "content-delta" || !strings.HasPrefix(line, "data: ") {
				continue
			}
			var d cohDelta
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &d); err != nil {
				ch <- provider.Chunk{Err: fmt.Errorf("parse stream: %w", err)}
				return
			}
			if text := d.Delta.Message.Content.Delta.Text; text != "" {
				ch <- provider.Chunk{Delta: text}
			}
		}
	}()
	return ch, nil
}

func (a *Adapter) HealthCheck(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.endpoint+"/v2/models", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return provider.NewHTTPError(resp.StatusCode, nil)
	}
	return nil
}

func toCoh(msgs []provider.Message) []cohMsg {
	out := make([]cohMsg, len(msgs))
	for i, m := range msgs {
		out[i] = cohMsg{Role: m.Role, Content: m.Content}
	}
	return out
}

var _ provider.Provider = (*Adapter)(nil)
```

- [ ] **Step 4: Run tests — expect pass**

```bash
go test ./internal/provider/cohere/... -v
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/provider/cohere/
git commit -m "feat: add cohere v2 provider adapter"
```

---

## Task 12: Provider Factory

**Files:**
- Create: `internal/provider/factory.go`

No tests — thin dispatch; adapter constructors are tested in their own packages.

- [ ] **Step 1: Write factory**

```go
// internal/provider/factory.go
package provider

import (
	"fmt"

	"github.com/rodrigoazlima/localrouter/internal/config"
	"github.com/rodrigoazlima/localrouter/internal/provider/anthropic"
	"github.com/rodrigoazlima/localrouter/internal/provider/cohere"
	"github.com/rodrigoazlima/localrouter/internal/provider/google"
	"github.com/rodrigoazlima/localrouter/internal/provider/ollama"
	"github.com/rodrigoazlima/localrouter/internal/provider/openaicompat"
)

func NewFromNode(n config.NodeConfig) (Provider, error) {
	switch n.Type {
	case "ollama":
		return ollama.New(n.ID, n.Endpoint, n.APIKey, n.TimeoutMs), nil
	case "openai-compatible":
		return openaicompat.New(n.ID, n.Endpoint, n.APIKey, n.TimeoutMs), nil
	default:
		return nil, fmt.Errorf("unknown node type: %s", n.Type)
	}
}

func NewFromRemote(p config.ProviderConfig) (Provider, error) {
	switch p.Type {
	case "openai-compatible":
		return openaicompat.New(p.ID, p.Endpoint, p.APIKey, 30000), nil
	case "anthropic":
		return anthropic.New(p.ID, p.APIKey, ""), nil
	case "google":
		return google.New(p.ID, p.APIKey, ""), nil
	case "cohere":
		return cohere.New(p.ID, p.APIKey, ""), nil
	default:
		return nil, fmt.Errorf("unknown provider type: %s", p.Type)
	}
}
```

- [ ] **Step 2: Build to verify no import errors**

```bash
go build ./internal/provider/...
```

Expected: no output.

- [ ] **Step 3: Commit**

```bash
git add internal/provider/factory.go
git commit -m "feat: add provider factory dispatching on type string"
```

---

## Task 13: Health Monitor

**Files:**
- Create: `internal/health/monitor.go`
- Create: `internal/health/monitor_test.go`

- [ ] **Step 1: Write failing tests**

```go
// internal/health/monitor_test.go
package health_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rodrigoazlima/localrouter/internal/health"
	"github.com/rodrigoazlima/localrouter/internal/metrics"
)

type mockProvider struct {
	id      string
	failErr error
	calls   atomic.Int32
}

func (m *mockProvider) ID() string   { return m.id }
func (m *mockProvider) Type() string { return "mock" }
func (m *mockProvider) HealthCheck(_ context.Context) error {
	m.calls.Add(1)
	return m.failErr
}
func (m *mockProvider) Complete(_ context.Context, _ interface{}) (interface{}, error) { return nil, nil }
func (m *mockProvider) Stream(_ context.Context, _ interface{}) (interface{}, error)   { return nil, nil }

// health.Monitor requires provider.Provider — use a local interface to avoid import cycle in tests.
// The monitor_test uses the real provider interface via a minimal stub.

func TestMonitor_ColdStart_Unavailable(t *testing.T) {
	mp := &mockProvider{id: "n1"}
	m := health.New(metrics.New(), 2000)
	if m.IsReady("n1") {
		t.Fatal("node must be UNAVAILABLE before first check")
	}
	_ = mp
}

func TestMonitor_BecomesReadyAfterSuccesses(t *testing.T) {
	mp := &stubNode{id: "n1", err: nil}
	m := health.New(metrics.New(), 2000)
	m.AddNode("n1", mp, 3000, 50)

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if m.IsReady("n1") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("node did not become READY within 1s")
}

func TestMonitor_BecomesUnavailableAfterFailures(t *testing.T) {
	mp := &stubNode{id: "n1", err: errors.New("down")}
	m := health.New(metrics.New(), 2000)
	m.AddNode("n1", mp, 3000, 50)

	time.Sleep(400 * time.Millisecond)
	if m.IsReady("n1") {
		t.Fatal("node must not be READY after repeated failures")
	}
}

func TestMonitor_RemoveNode_StopsWorker(t *testing.T) {
	mp := &stubNode{id: "n1", err: nil}
	m := health.New(metrics.New(), 2000)
	m.AddNode("n1", mp, 3000, 50)
	time.Sleep(100 * time.Millisecond)
	countBefore := mp.calls.Load()
	m.RemoveNode("n1")
	time.Sleep(200 * time.Millisecond)
	countAfter := mp.calls.Load()
	if countAfter > countBefore+1 {
		t.Fatalf("worker still running after RemoveNode: calls went %d→%d", countBefore, countAfter)
	}
}

// stubNode implements health.HealthChecker (the minimal interface monitor needs).
type stubNode struct {
	id    string
	err   error
	calls atomic.Int32
}

func (s *stubNode) ID() string { return s.id }
func (s *stubNode) HealthCheck(_ context.Context) error {
	s.calls.Add(1)
	return s.err
}
```

- [ ] **Step 2: Run — expect failure**

```bash
go test ./internal/health/... -v
```

Expected: compilation error.

- [ ] **Step 3: Write implementation**

```go
// internal/health/monitor.go
package health

import (
	"context"
	"math/rand"
	"sync"
	"time"

	"github.com/rodrigoazlima/localrouter/internal/metrics"
)

type NodeState int

const (
	StateUnavailable NodeState = iota
	StateDegraded
	StateReady
)

const (
	failThreshold    = 3
	successThreshold = 2
	latencyBreachThreshold = 3
)

// HealthChecker is the subset of provider.Provider the monitor needs.
// Avoids an import cycle: health → provider would be fine, but this keeps it minimal.
type HealthChecker interface {
	ID() string
	HealthCheck(ctx context.Context) error
}

type nodeStatus struct {
	state           NodeState
	latencyMs       int64
	successRun      int
	failureRun      int
	latencyBreaches int
}

type nodeWorker struct {
	cancel context.CancelFunc
}

type Monitor struct {
	mu                 sync.RWMutex
	states             map[string]nodeStatus
	workers            map[string]*nodeWorker
	metrics            *metrics.Collector
	latencyThresholdMs int64
}

func New(m *metrics.Collector, latencyThresholdMs int64) *Monitor {
	return &Monitor{
		states:             make(map[string]nodeStatus),
		workers:            make(map[string]*nodeWorker),
		metrics:            m,
		latencyThresholdMs: latencyThresholdMs,
	}
}

func (mon *Monitor) AddNode(id string, hc HealthChecker, timeoutMs, intervalMs int) {
	ctx, cancel := context.WithCancel(context.Background())
	mon.mu.Lock()
	mon.states[id] = nodeStatus{state: StateUnavailable}
	mon.workers[id] = &nodeWorker{cancel: cancel}
	mon.mu.Unlock()
	go mon.runNode(ctx, id, hc, timeoutMs, intervalMs)
}

func (mon *Monitor) RemoveNode(id string) {
	mon.mu.Lock()
	if w, ok := mon.workers[id]; ok {
		w.cancel()
		delete(mon.workers, id)
	}
	delete(mon.states, id)
	mon.mu.Unlock()
}

func (mon *Monitor) IsReady(id string) bool {
	mon.mu.RLock()
	defer mon.mu.RUnlock()
	s, ok := mon.states[id]
	return ok && s.state == StateReady
}

func (mon *Monitor) Snapshot() map[string]nodeStatus {
	mon.mu.RLock()
	defer mon.mu.RUnlock()
	out := make(map[string]nodeStatus, len(mon.states))
	for k, v := range mon.states {
		out[k] = v
	}
	return out
}

func (mon *Monitor) runNode(ctx context.Context, id string, hc HealthChecker, timeoutMs, intervalMs int) {
	base := time.Duration(intervalMs) * time.Millisecond
	backoff := base

	for {
		jittered := base + time.Duration(rand.Int63n(int64(base/4+1)))
		select {
		case <-ctx.Done():
			return
		case <-time.After(jittered):
		}

		checkCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
		start := time.Now()
		err := hc.HealthCheck(checkCtx)
		latency := time.Since(start).Milliseconds()
		cancel()

		mon.mu.Lock()
		s := mon.states[id]
		if err != nil {
			s.failureRun++
			s.successRun = 0
			s.latencyBreaches = 0
			if s.failureRun >= failThreshold {
				s.state = StateUnavailable
			}
			mon.states[id] = s
			mon.mu.Unlock()
			mon.metrics.NodeFail(id)
			backoff = min(backoff*2, 5*time.Minute)
		} else {
			s.successRun++
			s.failureRun = 0
			s.latencyMs = latency
			if latency > mon.latencyThresholdMs {
				s.latencyBreaches++
				if s.latencyBreaches >= latencyBreachThreshold {
					s.state = StateDegraded
				}
			} else {
				s.latencyBreaches = 0
				if s.successRun >= successThreshold {
					s.state = StateReady
				}
			}
			mon.states[id] = s
			mon.mu.Unlock()
			mon.metrics.NodeOK(id, latency)
			backoff = base
		}
	}
}

func min(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
```

- [ ] **Step 4: Fix test stub — `stubNode` must satisfy `health.HealthChecker`**

The test's `stubNode` already satisfies `HealthChecker` (has `ID()` and `HealthCheck()`). The `TestMonitor_ColdStart_Unavailable` test doesn't add any node, so the unused `mp` warning is harmless — remove the `_ = mp` line and delete `mp` declaration entirely:

```go
func TestMonitor_ColdStart_Unavailable(t *testing.T) {
	m := health.New(metrics.New(), 2000)
	if m.IsReady("n1") {
		t.Fatal("node must be UNAVAILABLE before first check")
	}
}
```

Also remove the unused `mockProvider` struct — only `stubNode` is needed.

- [ ] **Step 5: Run tests — expect pass**

```bash
go test ./internal/health/... -v
```

Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/health/
git commit -m "feat: add health monitor with hysteresis, backoff, and lifecycle control"
```

---

## Task 14: Router

**Files:**
- Create: `internal/router/router.go`
- Create: `internal/router/router_test.go`

- [ ] **Step 1: Write failing tests**

```go
// internal/router/router_test.go
package router_test

import (
	"context"
	"errors"
	"testing"

	"github.com/rodrigoazlima/localrouter/internal/cache"
	"github.com/rodrigoazlima/localrouter/internal/health"
	"github.com/rodrigoazlima/localrouter/internal/metrics"
	"github.com/rodrigoazlima/localrouter/internal/provider"
	"github.com/rodrigoazlima/localrouter/internal/router"
)

type mockProvider struct {
	id          string
	completeErr error
	completeResp *provider.Response
}

func (m *mockProvider) ID() string   { return m.id }
func (m *mockProvider) Type() string { return "mock" }
func (m *mockProvider) Complete(_ context.Context, _ *provider.Request) (*provider.Response, error) {
	return m.completeResp, m.completeErr
}
func (m *mockProvider) Stream(_ context.Context, _ *provider.Request) (<-chan provider.Chunk, error) {
	return nil, nil
}
func (m *mockProvider) HealthCheck(_ context.Context) error { return nil }

type alwaysReadyMonitor struct{}

func (a *alwaysReadyMonitor) IsReady(id string) bool { return true }

type neverReadyMonitor struct{}

func (n *neverReadyMonitor) IsReady(id string) bool { return false }

func TestRoute_LocalSuccess_ReturnsLocalResponse(t *testing.T) {
	local := &mockProvider{id: "local-1", completeResp: &provider.Response{Content: "from-local"}}
	r := router.New(
		[]provider.Provider{local},
		nil,
		cache.New(),
		&alwaysReadyMonitor{},
		metrics.New(),
	)
	resp, err := r.Route(context.Background(), &provider.Request{Model: "auto"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "from-local" {
		t.Fatalf("expected from-local, got %s", resp.Content)
	}
}

func TestRoute_LocalFail_FallsBackToRemote(t *testing.T) {
	local := &mockProvider{id: "local-1", completeErr: errors.New("timeout")}
	remote := &mockProvider{id: "remote-1", completeResp: &provider.Response{Content: "from-remote"}}
	r := router.New(
		[]provider.Provider{local},
		[]provider.Provider{remote},
		cache.New(),
		&alwaysReadyMonitor{},
		metrics.New(),
	)
	resp, err := r.Route(context.Background(), &provider.Request{Model: "auto"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "from-remote" {
		t.Fatalf("expected from-remote, got %s", resp.Content)
	}
}

func TestRoute_LocalNotReady_SkipsToRemote(t *testing.T) {
	local := &mockProvider{id: "local-1", completeResp: &provider.Response{Content: "from-local"}}
	remote := &mockProvider{id: "remote-1", completeResp: &provider.Response{Content: "from-remote"}}
	r := router.New(
		[]provider.Provider{local},
		[]provider.Provider{remote},
		cache.New(),
		&neverReadyMonitor{},
		metrics.New(),
	)
	resp, err := r.Route(context.Background(), &provider.Request{Model: "auto"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "from-remote" {
		t.Fatalf("expected from-remote, got %s", resp.Content)
	}
}

func TestRoute_BlockedRemote_Skipped(t *testing.T) {
	local := &mockProvider{id: "local-1", completeErr: errors.New("down")}
	blocked := &mockProvider{id: "remote-blocked"}
	good := &mockProvider{id: "remote-good", completeResp: &provider.Response{Content: "ok"}}

	c := cache.New()
	c.Block("remote-blocked", cache.TierA)

	r := router.New(
		[]provider.Provider{local},
		[]provider.Provider{blocked, good},
		c,
		&alwaysReadyMonitor{},
		metrics.New(),
	)
	resp, err := r.Route(context.Background(), &provider.Request{Model: "auto"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("expected ok, got %s", resp.Content)
	}
}

func TestRoute_AllFail_ReturnsNoCapacityError(t *testing.T) {
	local := &mockProvider{id: "local-1", completeErr: errors.New("down")}
	remote := &mockProvider{id: "remote-1", completeErr: &provider.HTTPError{StatusCode: 503}}
	m := metrics.New()
	r := router.New(
		[]provider.Provider{local},
		[]provider.Provider{remote},
		cache.New(),
		&alwaysReadyMonitor{},
		m,
	)
	_, err := r.Route(context.Background(), &provider.Request{Model: "auto"})
	if err == nil {
		t.Fatal("expected error when all providers fail")
	}
	if m.Snapshot().NoCapacity != 1 {
		t.Fatalf("expected NoCapacity=1, got %d", m.Snapshot().NoCapacity)
	}
}

func TestRoute_429_BlocksProviderTierA(t *testing.T) {
	local := &mockProvider{id: "local-1", completeErr: errors.New("down")}
	remote := &mockProvider{id: "remote-1", completeErr: &provider.HTTPError{StatusCode: 429}}
	c := cache.New()
	r := router.New(
		[]provider.Provider{local}, []provider.Provider{remote}, c,
		&alwaysReadyMonitor{}, metrics.New(),
	)
	r.Route(context.Background(), &provider.Request{Model: "auto"})
	if !c.IsBlocked("remote-1") {
		t.Fatal("remote-1 must be blocked after 429")
	}
}
```

- [ ] **Step 2: Run — expect failure**

```bash
go test ./internal/router/... -v
```

Expected: compilation error.

- [ ] **Step 3: Write implementation**

```go
// internal/router/router.go
package router

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/rodrigoazlima/localrouter/internal/cache"
	"github.com/rodrigoazlima/localrouter/internal/metrics"
	"github.com/rodrigoazlima/localrouter/internal/provider"
)

var ErrAllProvidersFailed = errors.New("all providers failed or unavailable")

// ReadinessChecker is the subset of health.Monitor used by the router.
type ReadinessChecker interface {
	IsReady(id string) bool
}

type Router struct {
	mu      sync.RWMutex
	locals  []provider.Provider
	remotes []provider.Provider
	cache   *cache.Cache
	health  ReadinessChecker
	metrics *metrics.Collector
}

func New(locals, remotes []provider.Provider, c *cache.Cache, h ReadinessChecker, m *metrics.Collector) *Router {
	return &Router{locals: locals, remotes: remotes, cache: c, health: h, metrics: m}
}

func (r *Router) Update(locals, remotes []provider.Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.locals = locals
	r.remotes = remotes
}

func (r *Router) Route(ctx context.Context, req *provider.Request) (*provider.Response, error) {
	r.mu.RLock()
	locals := r.locals
	remotes := r.remotes
	r.mu.RUnlock()

	for _, p := range locals {
		if !r.health.IsReady(p.ID()) {
			continue
		}
		resp, err := p.Complete(ctx, req)
		if err == nil {
			r.metrics.LocalRequests.Add(1)
			r.cache.Reset4xx(p.ID())
			return resp, nil
		}
		r.metrics.Tier1Failures.Add(1)
	}

	for _, p := range remotes {
		if r.cache.IsBlocked(p.ID()) {
			continue
		}
		resp, err := p.Complete(ctx, req)
		if err == nil {
			r.metrics.RemoteRequests.Add(1)
			r.cache.Reset4xx(p.ID())
			return resp, nil
		}
		r.metrics.Tier2Failures.Add(1)
		tier := r.classifyError(p.ID(), err)
		r.cache.Block(p.ID(), tier)
		r.metrics.BlockedProviders.Add(1)
	}

	r.metrics.NoCapacity.Add(1)
	return nil, ErrAllProvidersFailed
}

func (r *Router) Stream(ctx context.Context, req *provider.Request) (<-chan provider.Chunk, error) {
	r.mu.RLock()
	locals := r.locals
	remotes := r.remotes
	r.mu.RUnlock()

	for _, p := range locals {
		if !r.health.IsReady(p.ID()) {
			continue
		}
		ch, err := p.Stream(ctx, req)
		if err == nil {
			r.metrics.LocalRequests.Add(1)
			return ch, nil
		}
		r.metrics.Tier1Failures.Add(1)
	}

	for _, p := range remotes {
		if r.cache.IsBlocked(p.ID()) {
			continue
		}
		ch, err := p.Stream(ctx, req)
		if err == nil {
			r.metrics.RemoteRequests.Add(1)
			return ch, nil
		}
		r.metrics.Tier2Failures.Add(1)
		tier := r.classifyError(p.ID(), err)
		r.cache.Block(p.ID(), tier)
		r.metrics.BlockedProviders.Add(1)
	}

	r.metrics.NoCapacity.Add(1)
	return nil, ErrAllProvidersFailed
}

func (r *Router) classifyError(providerID string, err error) cache.Tier {
	var httpErr *provider.HTTPError
	if !errors.As(err, &httpErr) {
		return cache.TierA
	}
	switch httpErr.StatusCode {
	case 429, 529:
		return cache.TierA
	case 401, 403:
		return cache.TierB
	}
	body := strings.ToLower(httpErr.Body)
	if strings.Contains(body, "rate limit") || strings.Contains(body, "overloaded") {
		return cache.TierA
	}
	if httpErr.StatusCode >= 400 && httpErr.StatusCode < 500 {
		return r.cache.Track4xxAndGetTier(providerID)
	}
	return cache.TierA
}
```

- [ ] **Step 4: Run tests — expect pass**

```bash
go test ./internal/router/... -v
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/router/
git commit -m "feat: add router with tier1→tier2 fallback and error classification"
```

---

## Task 15: Server + Handlers

**Files:**
- Create: `internal/server/server.go`
- Create: `internal/server/handlers.go`
- Create: `internal/server/handlers_test.go`

- [ ] **Step 1: Write failing tests**

```go
// internal/server/handlers_test.go
package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rodrigoazlima/localrouter/internal/cache"
	"github.com/rodrigoazlima/localrouter/internal/health"
	"github.com/rodrigoazlima/localrouter/internal/metrics"
	"github.com/rodrigoazlima/localrouter/internal/server"
)

func TestHealthEndpoint_ReturnsJSON(t *testing.T) {
	m := metrics.New()
	c := cache.New()
	mon := health.New(m, 2000)
	srv := server.New(nil, mon, c, m)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if _, ok := body["local"]; !ok {
		t.Fatal("missing 'local' key in health response")
	}
	if _, ok := body["remote"]; !ok {
		t.Fatal("missing 'remote' key in health response")
	}
}

func TestMetricsEndpoint_ReturnsJSON(t *testing.T) {
	m := metrics.New()
	m.LocalRequests.Add(5)
	srv := server.New(nil, health.New(m, 2000), cache.New(), m)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var snap metrics.Snapshot
	if err := json.NewDecoder(rr.Body).Decode(&snap); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if snap.LocalRequests != 5 {
		t.Fatalf("expected 5, got %d", snap.LocalRequests)
	}
}
```

- [ ] **Step 2: Run — expect failure**

```bash
go test ./internal/server/... -run TestHealth -v
go test ./internal/server/... -run TestMetrics -v
```

Expected: compilation error.

- [ ] **Step 3: Write server.go**

```go
// internal/server/server.go
package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/rodrigoazlima/localrouter/internal/cache"
	"github.com/rodrigoazlima/localrouter/internal/health"
	"github.com/rodrigoazlima/localrouter/internal/metrics"
	"github.com/rodrigoazlima/localrouter/internal/router"
)

type Server struct {
	*http.Server
	router  *router.Router
	monitor *health.Monitor
	cache   *cache.Cache
	metrics *metrics.Collector
}

func New(r *router.Router, mon *health.Monitor, c *cache.Cache, m *metrics.Collector) *Server {
	s := &Server{router: r, monitor: mon, cache: c, metrics: m}

	mux := chi.NewRouter()
	mux.Use(middleware.Recoverer)
	mux.Get("/health", s.handleHealth)
	mux.Get("/metrics", s.handleMetrics)
	mux.Post("/v1/chat/completions", s.handleCompletions)

	s.Server = &http.Server{Addr: ":8080", Handler: mux}
	return s
}
```

- [ ] **Step 4: Write handlers.go**

```go
// internal/server/handlers.go
package server

import (
	"encoding/json"
	"net/http"
	"time"
)

type healthResponse struct {
	Local  localHealth    `json:"local"`
	Remote []remoteHealth `json:"remote"`
}

type localHealth struct {
	Status string      `json:"status"`
	Nodes  []nodeInfo  `json:"nodes"`
}

type nodeInfo struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	LatencyMs int64  `json:"latency_ms"`
}

type remoteHealth struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	TTLRemaining int64  `json:"ttl_remaining,omitempty"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	snap := s.monitor.Snapshot()
	cacheSnap := s.cache.Snapshot()
	metricsSnap := s.metrics.Snapshot()

	nodes := make([]nodeInfo, 0, len(snap))
	overallStatus := "unavailable"
	for id, st := range snap {
		status := "unavailable"
		switch st.State {
		case 2: // StateReady
			status = "ready"
			overallStatus = "healthy"
		case 1: // StateDegraded
			status = "degraded"
			if overallStatus != "healthy" {
				overallStatus = "degraded"
			}
		}
		latency := metricsSnap.Nodes[id].LatencyMs
		nodes = append(nodes, nodeInfo{ID: id, Status: status, LatencyMs: latency})
	}

	remotes := make([]remoteHealth, 0)
	for id, entry := range cacheSnap {
		rh := remoteHealth{ID: id}
		if entry.State == "blocked" && time.Now().Before(entry.ExpiresAt) {
			rh.Status = "blocked"
			rh.TTLRemaining = int64(time.Until(entry.ExpiresAt).Seconds())
		} else {
			rh.Status = "available"
		}
		remotes = append(remotes, rh)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(healthResponse{
		Local:  localHealth{Status: overallStatus, Nodes: nodes},
		Remote: remotes,
	})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.metrics.Snapshot())
}
```

- [ ] **Step 5: Run tests — expect pass**

```bash
go test ./internal/server/... -run "TestHealth|TestMetrics" -v
```

Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/server/server.go internal/server/handlers.go internal/server/handlers_test.go
git commit -m "feat: add server with /health and /metrics handlers"
```

---

## Task 16: SSE Streaming Handler

**Files:**
- Create: `internal/server/sse.go`
- Create: `internal/server/sse_test.go`

- [ ] **Step 1: Write failing tests**

```go
// internal/server/sse_test.go
package server_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rodrigoazlima/localrouter/internal/cache"
	"github.com/rodrigoazlima/localrouter/internal/health"
	"github.com/rodrigoazlima/localrouter/internal/metrics"
	"github.com/rodrigoazlima/localrouter/internal/provider"
	"github.com/rodrigoazlima/localrouter/internal/router"
	"github.com/rodrigoazlima/localrouter/internal/server"
)

type succeedingProvider struct{}

func (s *succeedingProvider) ID() string   { return "ok" }
func (s *succeedingProvider) Type() string { return "mock" }
func (s *succeedingProvider) Complete(_ context.Context, _ *provider.Request) (*provider.Response, error) {
	return &provider.Response{Content: "Hello!"}, nil
}
func (s *succeedingProvider) Stream(_ context.Context, _ *provider.Request) (<-chan provider.Chunk, error) {
	ch := make(chan provider.Chunk, 3)
	ch <- provider.Chunk{Delta: "He"}
	ch <- provider.Chunk{Delta: "llo"}
	close(ch)
	return ch, nil
}
func (s *succeedingProvider) HealthCheck(_ context.Context) error { return nil }

type alwaysReady struct{}
func (a *alwaysReady) IsReady(_ string) bool { return true }

func buildTestServer(p provider.Provider) *server.Server {
	m := metrics.New()
	c := cache.New()
	mon := health.New(m, 2000)
	r := router.New([]provider.Provider{p}, nil, c, &alwaysReady{}, m)
	return server.New(r, mon, c, m)
}

func TestCompletions_NonStream_ReturnsJSON(t *testing.T) {
	srv := buildTestServer(&succeedingProvider{})
	body := `{"model":"auto","messages":[{"role":"user","content":"Hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	choices, _ := resp["choices"].([]any)
	if len(choices) == 0 {
		t.Fatal("expected at least one choice")
	}
}

func TestCompletions_Stream_SendsSSEEvents(t *testing.T) {
	srv := buildTestServer(&succeedingProvider{})
	body := `{"model":"auto","messages":[{"role":"user","content":"Hi"}],"stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("expected text/event-stream, got %s", ct)
	}

	scanner := bufio.NewScanner(bytes.NewReader(rr.Body.Bytes()))
	var dataLines []string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			dataLines = append(dataLines, strings.TrimPrefix(line, "data: "))
		}
	}
	if len(dataLines) == 0 {
		t.Fatal("no data lines in SSE response")
	}
	last := dataLines[len(dataLines)-1]
	if last != "[DONE]" {
		t.Fatalf("last data line must be [DONE], got %s", last)
	}
}

func TestCompletions_MissingBody_Returns400(t *testing.T) {
	srv := buildTestServer(&succeedingProvider{})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}
```

- [ ] **Step 2: Run — expect failure**

```bash
go test ./internal/server/... -run TestCompletions -v
```

Expected: compilation error (sse.go missing).

- [ ] **Step 3: Write sse.go**

```go
// internal/server/sse.go
package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/rodrigoazlima/localrouter/internal/provider"
)

type completionRequest struct {
	Model    string             `json:"model"`
	Messages []provider.Message `json:"messages"`
	Stream   bool               `json:"stream"`
}

type completionResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Model   string   `json:"model"`
	Choices []choice `json:"choices"`
	Usage   provider.Usage `json:"usage"`
}

type choice struct {
	Index   int             `json:"index"`
	Message provider.Message `json:"message"`
}

type streamChoice struct {
	Index int    `json:"index"`
	Delta struct {
		Content string `json:"content"`
	} `json:"delta"`
}

type streamEvent struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Choices []streamChoice `json:"choices"`
}

type sseError struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

func (s *Server) handleCompletions(w http.ResponseWriter, r *http.Request) {
	var req completionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Messages) == 0 {
		http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
		return
	}

	provReq := &provider.Request{
		Model:    req.Model,
		Messages: req.Messages,
		Stream:   req.Stream,
	}

	if !req.Stream {
		s.handleComplete(w, r, provReq)
		return
	}
	s.handleStream(w, r, provReq)
}

func (s *Server) handleComplete(w http.ResponseWriter, r *http.Request, req *provider.Request) {
	resp, err := s.router.Route(r.Context(), req)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q,"type":"router_error"}}`, err.Error()), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(completionResponse{
		ID:     resp.ID,
		Object: "chat.completion",
		Model:  resp.Model,
		Choices: []choice{{
			Index:   0,
			Message: provider.Message{Role: "assistant", Content: resp.Content},
		}},
		Usage: resp.Usage,
	})
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request, req *provider.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher.Flush()

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

	ch, err := s.router.Stream(r.Context(), req)
	if err != nil {
		writeSSEError(w, err.Error())
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
		case chunk, ok := <-ch:
			if !ok {
				fmt.Fprintf(w, "data: [DONE]\n\n")
				flusher.Flush()
				return
			}
			if chunk.Err != nil {
				writeSSEError(w, chunk.Err.Error())
				flusher.Flush()
				return
			}
			data, _ := json.Marshal(streamEvent{
				Object: "chat.completion.chunk",
				Choices: []streamChoice{{
					Delta: struct {
						Content string `json:"content"`
					}{Content: chunk.Delta},
				}},
			})
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

func writeSSEError(w http.ResponseWriter, msg string) {
	var e sseError
	e.Error.Message = msg
	e.Error.Type = "stream_error"
	data, _ := json.Marshal(e)
	fmt.Fprintf(w, "data: %s\n\n", data)
}
```

- [ ] **Step 4: Run tests — expect pass**

```bash
go test ./internal/server/... -v
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/server/sse.go internal/server/sse_test.go
git commit -m "feat: add SSE streaming handler with heartbeat and context cancellation"
```

---

## Task 17: main.go Wiring

**Files:**
- Create: `cmd/localrouter/main.go`

No unit tests — integration point. Build verification is the test.

- [ ] **Step 1: Write main.go**

```go
// cmd/localrouter/main.go
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rodrigoazlima/localrouter/internal/cache"
	"github.com/rodrigoazlima/localrouter/internal/config"
	"github.com/rodrigoazlima/localrouter/internal/health"
	"github.com/rodrigoazlima/localrouter/internal/metrics"
	"github.com/rodrigoazlima/localrouter/internal/provider"
	"github.com/rodrigoazlima/localrouter/internal/router"
	"github.com/rodrigoazlima/localrouter/internal/server"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	// 1. Load config
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	cfg.Version = 1

	// 2. Init cache and metrics
	c := cache.New()
	m := metrics.New()

	// 3. Build providers
	locals, err := buildLocals(cfg)
	if err != nil {
		log.Fatalf("build local providers: %v", err)
	}
	remotes, err := buildRemotes(cfg)
	if err != nil {
		log.Fatalf("build remote providers: %v", err)
	}

	// 4. Init health monitor
	latency := int64(cfg.Routing.LatencyThresholdMs)
	if latency == 0 {
		latency = 2000
	}
	mon := health.New(m, latency)
	for _, n := range cfg.Local.Nodes {
		p, _ := provider.NewFromNode(n)
		mon.AddNode(n.ID, p.(health.HealthChecker), n.TimeoutMs, 10000)
	}

	// 5. Init router
	r := router.New(locals, remotes, c, mon, m)

	// 6. Start server
	srv := server.New(r, mon, c, m)

	// 7. Config hot reload
	watcher, err := config.NewWatcher(*cfgPath, cfg, func(newCfg *config.Config) {
		newLocals, err := buildLocals(newCfg)
		if err != nil {
			log.Printf("reload: build locals: %v", err)
			return
		}
		newRemotes, err := buildRemotes(newCfg)
		if err != nil {
			log.Printf("reload: build remotes: %v", err)
			return
		}
		r.Update(newLocals, newRemotes)

		// reconcile health monitor
		oldNodes := make(map[string]bool)
		for _, n := range cfg.Local.Nodes {
			oldNodes[n.ID] = true
		}
		for _, n := range newCfg.Local.Nodes {
			if !oldNodes[n.ID] {
				p, _ := provider.NewFromNode(n)
				mon.AddNode(n.ID, p.(health.HealthChecker), n.TimeoutMs, 10000)
			}
		}
		newNodes := make(map[string]bool)
		for _, n := range newCfg.Local.Nodes {
			newNodes[n.ID] = true
		}
		for _, n := range cfg.Local.Nodes {
			if !newNodes[n.ID] {
				mon.RemoveNode(n.ID)
			}
		}
		cfg = newCfg
	})
	if err != nil {
		log.Fatalf("start config watcher: %v", err)
	}
	defer watcher.Stop()

	// 8. Graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("listening on %s", srv.Addr)
		if err := srv.ListenAndServe(); err != nil {
			log.Printf("server: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down...")
	shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	srv.Shutdown(shutCtx)
	mon.Stop()
}

func buildLocals(cfg *config.Config) ([]provider.Provider, error) {
	out := make([]provider.Provider, 0, len(cfg.Local.Nodes))
	for _, n := range cfg.Local.Nodes {
		p, err := provider.NewFromNode(n)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func buildRemotes(cfg *config.Config) ([]provider.Provider, error) {
	out := make([]provider.Provider, 0, len(cfg.Remote.Providers))
	for _, p := range cfg.Remote.Providers {
		prov, err := provider.NewFromRemote(p)
		if err != nil {
			return nil, err
		}
		out = append(out, prov)
	}
	return out, nil
}
```

The `Monitor` needs a `Stop()` method and must expose `HealthChecker` interface for the cast. Add `Stop()` to `internal/health/monitor.go`:

```go
func (mon *Monitor) Stop() {
	mon.mu.Lock()
	defer mon.mu.Unlock()
	for id, w := range mon.workers {
		w.cancel()
		delete(mon.workers, id)
	}
}
```

- [ ] **Step 2: Build to verify**

```bash
go build ./cmd/localrouter/...
```

Expected: binary created with no errors.

- [ ] **Step 3: Commit**

```bash
git add cmd/localrouter/main.go internal/health/monitor.go
git commit -m "feat: wire all components in main.go with graceful shutdown"
```

---

## Task 18: Dockerfile + Example Config

**Files:**
- Create: `Dockerfile`
- Create: `config.yaml`

- [ ] **Step 1: Write Dockerfile**

```dockerfile
FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o localrouter ./cmd/localrouter

FROM alpine:3.19
RUN apk --no-cache add ca-certificates
COPY --from=builder /app/localrouter /usr/local/bin/localrouter
EXPOSE 8080
ENTRYPOINT ["localrouter"]
```

- [ ] **Step 2: Write config.yaml**

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

- [ ] **Step 3: Run full test suite**

```bash
go test ./... -v
```

Expected: all packages PASS.

- [ ] **Step 4: Commit**

```bash
git add Dockerfile config.yaml
git commit -m "feat: add Dockerfile and example config.yaml"
```

---

## Self-Review

**Spec coverage check:**

| Spec Section | Covered by Task |
|---|---|
| Package structure | Task 1 + all tasks |
| Provider interface + types | Task 2 |
| Cache + error classification | Task 3 + Task 14 |
| OpenAI-compat adapter | Task 7 |
| Ollama adapter | Task 8 |
| Anthropic adapter | Task 9 |
| Google adapter | Task 10 |
| Cohere adapter | Task 11 |
| Provider factory | Task 12 |
| Health monitor + hysteresis | Task 13 |
| Router tier1→tier2 | Task 14 |
| HTTP server | Task 15 |
| SSE streaming + safeguards | Task 16 |
| Config load + hot reload | Task 5 + Task 6 |
| Metrics | Task 4 |
| main.go wiring + shutdown | Task 17 |
| Dockerfile | Task 18 |

**Gaps found and fixed:**
- `Monitor.Stop()` not originally in Task 13 — added in Task 17 step 1
- `health.HealthChecker` interface must be exported — covered in Task 13
- `NewHTTPError(code, nil)` used in google/cohere adapters — `provider.NewHTTPError` handles nil reader (no body to read, writes empty string)

**Type consistency check:** All method signatures consistent across tasks:
- `provider.Provider` interface used uniformly in router and server
- `cache.Tier` (TierA/TierB) used consistently in cache and router
- `health.HealthChecker` interface matches what main.go casts to
- `metrics.Snapshot` struct used in handlers_test matches metrics.go definition
