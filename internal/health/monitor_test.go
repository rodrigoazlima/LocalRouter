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

func TestMonitor_ColdStart_Unavailable(t *testing.T) {
	m := health.New(metrics.New(), 2000)
	if m.IsReady("n1") {
		t.Fatal("node must be UNAVAILABLE before first check")
	}
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
