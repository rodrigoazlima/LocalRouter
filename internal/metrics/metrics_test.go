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
