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
	failThreshold          = 3
	successThreshold       = 2
	latencyBreachThreshold = 3
)

// HealthChecker is the subset of provider.Provider the monitor needs.
type HealthChecker interface {
	ID() string
	HealthCheck(ctx context.Context) error
}

type NodeStatus struct {
	State           NodeState
	LatencyMs       int64
	successRun      int
	failureRun      int
	latencyBreaches int
}

type nodeWorker struct {
	cancel context.CancelFunc
}

type Monitor struct {
	mu                 sync.RWMutex
	states             map[string]NodeStatus
	workers            map[string]*nodeWorker
	metrics            *metrics.Collector
	latencyThresholdMs int64
}

func New(m *metrics.Collector, latencyThresholdMs int64) *Monitor {
	return &Monitor{
		states:             make(map[string]NodeStatus),
		workers:            make(map[string]*nodeWorker),
		metrics:            m,
		latencyThresholdMs: latencyThresholdMs,
	}
}

func (mon *Monitor) AddNode(id string, hc HealthChecker, timeoutMs, intervalMs int) {
	ctx, cancel := context.WithCancel(context.Background())
	mon.mu.Lock()
	mon.states[id] = NodeStatus{State: StateUnavailable}
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
	return ok && s.State == StateReady
}

func (mon *Monitor) Snapshot() map[string]NodeStatus {
	mon.mu.RLock()
	defer mon.mu.RUnlock()
	out := make(map[string]NodeStatus, len(mon.states))
	for k, v := range mon.states {
		out[k] = v
	}
	return out
}

func (mon *Monitor) Stop() {
	mon.mu.Lock()
	defer mon.mu.Unlock()
	for id, w := range mon.workers {
		w.cancel()
		delete(mon.workers, id)
	}
}

func (mon *Monitor) runNode(ctx context.Context, id string, hc HealthChecker, timeoutMs, intervalMs int) {
	base := time.Duration(intervalMs) * time.Millisecond
	backoff := base

	for {
		jittered := backoff + time.Duration(rand.Int63n(int64(backoff/4+1)))
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
				s.State = StateUnavailable
			}
			mon.states[id] = s
			mon.mu.Unlock()
			mon.metrics.NodeFail(id)
			backoff = min(backoff*2, 5*time.Minute)
		} else {
			s.successRun++
			s.failureRun = 0
			s.LatencyMs = latency
			if latency > mon.latencyThresholdMs {
				s.latencyBreaches++
				if s.latencyBreaches >= latencyBreachThreshold {
					s.State = StateDegraded
					s.successRun = 0 // reset so 2-check hysteresis for DEGRADED→READY is enforced
				}
			} else {
				s.latencyBreaches = 0
				if s.successRun >= successThreshold {
					s.State = StateReady
				}
			}
			mon.states[id] = s
			mon.mu.Unlock()
			mon.metrics.NodeOK(id, latency)
			backoff = base
		}
	}
}
