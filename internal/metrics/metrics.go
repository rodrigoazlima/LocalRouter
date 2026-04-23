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
	ProviderBlockEvents atomic.Int64 // TODO: replace with a gauge computed from cache state

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
	ProviderBlockEvents int64                   `json:"provider_block_events"`
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
		ProviderBlockEvents: c.ProviderBlockEvents.Load(),
		Nodes:               nodes,
	}
}
