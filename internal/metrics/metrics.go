package metrics

import (
	"sync"
	"sync/atomic"
)

type Collector struct {
	Requests                atomic.Int64
	Failures                atomic.Int64
	NoCapacity              atomic.Int64
	StreamsStarted          atomic.Int64
	StreamsCompleted        atomic.Int64
	StreamsDisconnected     atomic.Int64
	StreamDuration          atomic.Int64
	ProviderBlockEvents     atomic.Int64
	ProviderExhaustedEvents atomic.Int64

	// Request source tracking.
	LocalRequests  atomic.Int64
	RemoteRequests atomic.Int64

	mu                 sync.RWMutex
	providerChecksOK   map[string]*atomic.Int64
	providerChecksFail map[string]*atomic.Int64
	providerLatencyMs  map[string]*atomic.Int64
	concurrentActive   map[string]*atomic.Int64
	concurrentRejected map[string]*atomic.Int64
}

type Snapshot struct {
	Requests                int64 `json:"requests"`
	Failures                int64 `json:"failures"`
	NoCapacity              int64 `json:"no_capacity"`
	StreamsStarted          int64 `json:"streams_started"`
	StreamsCompleted        int64 `json:"streams_completed"`
	StreamsDisconnected     int64 `json:"streams_disconnected"`
	StreamDurationMs        int64 `json:"stream_duration_ms"`
	ProviderBlockEvents     int64 `json:"provider_block_events"`
	ProviderExhaustedEvents int64 `json:"provider_exhausted_events"`

	LocalRequests  int64 `json:"local_requests"`
	RemoteRequests int64 `json:"remote_requests"`

	// Providers maps provider ID to per-provider health metrics.
	// JSON key is "nodes" to match the new API contract.
	Providers map[string]ProviderSnapshot `json:"nodes"`
}

type ProviderSnapshot struct {
	ChecksOK           int64 `json:"checks_ok"`
	ChecksFail         int64 `json:"checks_fail"`
	LatencyMs          int64 `json:"latency_ms"`
	ConcurrentActive   int64 `json:"concurrent_active"`
	ConcurrentRejected int64 `json:"concurrent_rejected"`
}

func New() *Collector {
	return &Collector{
		providerChecksOK:   make(map[string]*atomic.Int64),
		providerChecksFail: make(map[string]*atomic.Int64),
		providerLatencyMs:  make(map[string]*atomic.Int64),
		concurrentActive:   make(map[string]*atomic.Int64),
		concurrentRejected: make(map[string]*atomic.Int64),
	}
}

func (c *Collector) ensureProvider(id string) {
	if _, ok := c.providerChecksOK[id]; !ok {
		c.providerChecksOK[id] = &atomic.Int64{}
		c.providerChecksFail[id] = &atomic.Int64{}
		c.providerLatencyMs[id] = &atomic.Int64{}
		c.concurrentActive[id] = &atomic.Int64{}
		c.concurrentRejected[id] = &atomic.Int64{}
	}
}

func (c *Collector) ProviderOK(id string, latencyMs int64) {
	c.mu.Lock()
	c.ensureProvider(id)
	ok := c.providerChecksOK[id]
	lat := c.providerLatencyMs[id]
	c.mu.Unlock()
	ok.Add(1)
	lat.Store(latencyMs)
}

func (c *Collector) ProviderFail(id string) {
	c.mu.Lock()
	c.ensureProvider(id)
	fail := c.providerChecksFail[id]
	c.mu.Unlock()
	fail.Add(1)
}

// AddConcurrentActive adjusts the in-flight request counter for id by delta (+1 acquire, -1 release).
func (c *Collector) AddConcurrentActive(id string, delta int64) {
	c.mu.Lock()
	c.ensureProvider(id)
	counter := c.concurrentActive[id]
	c.mu.Unlock()
	counter.Add(delta)
}

// RecordConcurrencyRejected increments the concurrency-rejected counter for id.
func (c *Collector) RecordConcurrencyRejected(id string) {
	c.mu.Lock()
	c.ensureProvider(id)
	counter := c.concurrentRejected[id]
	c.mu.Unlock()
	counter.Add(1)
}

func (c *Collector) Snapshot() Snapshot {
	c.mu.RLock()
	providers := make(map[string]ProviderSnapshot, len(c.providerChecksOK))
	for id := range c.providerChecksOK {
		providers[id] = ProviderSnapshot{
			ChecksOK:           c.providerChecksOK[id].Load(),
			ChecksFail:         c.providerChecksFail[id].Load(),
			LatencyMs:          c.providerLatencyMs[id].Load(),
			ConcurrentActive:   c.concurrentActive[id].Load(),
			ConcurrentRejected: c.concurrentRejected[id].Load(),
		}
	}
	c.mu.RUnlock()
	return Snapshot{
		Requests:                c.Requests.Load(),
		Failures:                c.Failures.Load(),
		NoCapacity:              c.NoCapacity.Load(),
		StreamsStarted:          c.StreamsStarted.Load(),
		StreamsCompleted:        c.StreamsCompleted.Load(),
		StreamsDisconnected:     c.StreamsDisconnected.Load(),
		StreamDurationMs:        c.StreamDuration.Load(),
		ProviderBlockEvents:     c.ProviderBlockEvents.Load(),
		ProviderExhaustedEvents: c.ProviderExhaustedEvents.Load(),
		LocalRequests:           c.LocalRequests.Load(),
		RemoteRequests:          c.RemoteRequests.Load(),
		Providers:               providers,
	}
}
