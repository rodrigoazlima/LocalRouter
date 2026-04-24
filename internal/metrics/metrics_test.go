package metrics_test

import (
	"testing"

	"github.com/rodrigoazlima/localrouter/internal/metrics"
)

func TestCollector_ZeroOnNew(t *testing.T) {
	c := metrics.New()
	s := c.Snapshot()
	if s.Requests != 0 || s.Failures != 0 || s.NoCapacity != 0 {
		t.Fatal("all counters must start at zero")
	}
}

func TestCollector_CounterIncrement(t *testing.T) {
	c := metrics.New()
	c.Requests.Add(3)
	c.Failures.Add(1)
	c.NoCapacity.Add(2)
	s := c.Snapshot()
	if s.Requests != 3 {
		t.Fatalf("expected 3, got %d", s.Requests)
	}
	if s.Failures != 1 {
		t.Fatalf("expected 1, got %d", s.Failures)
	}
	if s.NoCapacity != 2 {
		t.Fatalf("expected 2, got %d", s.NoCapacity)
	}
}

func TestCollector_SnapshotIsImmutable(t *testing.T) {
	c := metrics.New()
	c.Requests.Add(5)
	s1 := c.Snapshot()
	c.Requests.Add(3)
	s2 := c.Snapshot()
	if s1.Requests != 5 {
		t.Fatalf("s1 must be 5, got %d", s1.Requests)
	}
	if s2.Requests != 8 {
		t.Fatalf("s2 must be 8, got %d", s2.Requests)
	}
}

func TestCollector_ProviderOK_RecordsLatency(t *testing.T) {
	c := metrics.New()
	c.ProviderOK("p1", 42)
	s := c.Snapshot()
	p, ok := s.Providers["p1"]
	if !ok {
		t.Fatal("p1 missing from snapshot")
	}
	if p.ChecksOK != 1 {
		t.Fatalf("expected 1 ok, got %d", p.ChecksOK)
	}
	if p.LatencyMs != 42 {
		t.Fatalf("expected 42ms, got %d", p.LatencyMs)
	}
}

func TestCollector_ProviderFail_IncrementsFail(t *testing.T) {
	c := metrics.New()
	c.ProviderFail("p1")
	c.ProviderFail("p1")
	s := c.Snapshot()
	if s.Providers["p1"].ChecksFail != 2 {
		t.Fatalf("expected 2 fails, got %d", s.Providers["p1"].ChecksFail)
	}
}
