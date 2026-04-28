package limits_test

import (
	"testing"
	"time"

	"github.com/rodrigoazlima/localrouter/internal/limits"
)

func TestTracker_NoLimits(t *testing.T) {
	tr := limits.New(nil)
	for i := 0; i < 1000; i++ {
		exhausted, resetAt := tr.Record("p1")
		if exhausted {
			t.Fatal("should never be exhausted with no limits")
		}
		if !resetAt.IsZero() {
			t.Fatal("resetAt should be zero with no limits")
		}
	}
}

func TestTracker_WithinLimit(t *testing.T) {
	tr := limits.New(map[string][]limits.Config{"p1": {{Requests: 3, Window: time.Minute}}})
	for i := 0; i < 3; i++ {
		exhausted, _ := tr.Record("p1")
		if exhausted {
			t.Fatalf("call %d: should not be exhausted (limit is 3)", i+1)
		}
	}
}

func TestTracker_Exhausted(t *testing.T) {
	tr := limits.New(map[string][]limits.Config{"p1": {{Requests: 2, Window: time.Minute}}})
	tr.Record("p1") // 1
	tr.Record("p1") // 2
	exhausted, resetAt := tr.Record("p1") // 3 — over limit
	if !exhausted {
		t.Fatal("expected exhausted=true on 3rd call with limit 2")
	}
	if resetAt.IsZero() {
		t.Fatal("expected non-zero resetAt when exhausted")
	}
	if resetAt.Before(time.Now()) {
		t.Fatal("resetAt should be in the future")
	}
}

func TestTracker_WindowReset(t *testing.T) {
	tr := limits.New(map[string][]limits.Config{"p1": {{Requests: 1, Window: 50 * time.Millisecond}}})
	tr.Record("p1") // 1 — at limit
	exhausted, _ := tr.Record("p1") // over limit
	if !exhausted {
		t.Fatal("expected exhausted before window reset")
	}
	time.Sleep(60 * time.Millisecond)
	exhausted, _ = tr.Record("p1") // window reset → counter = 1 again
	if exhausted {
		t.Fatal("expected not exhausted after window reset")
	}
}

func TestTracker_ResetAt(t *testing.T) {
	tr := limits.New(map[string][]limits.Config{"p1": {{Requests: 5, Window: time.Hour}}})
	if !tr.ResetAt("p1").IsZero() {
		t.Error("ResetAt should be zero before first Record")
	}
	tr.Record("p1")
	if tr.ResetAt("p1").IsZero() {
		t.Error("ResetAt should be non-zero after first Record")
	}
}

func TestTracker_IndependentProviders(t *testing.T) {
	tr := limits.New(map[string][]limits.Config{
		"p1": {{Requests: 1, Window: time.Minute}},
		"p2": {{Requests: 10, Window: time.Minute}},
	})
	tr.Record("p1")
	exhausted, _ := tr.Record("p1")
	if !exhausted {
		t.Error("p1 should be exhausted")
	}
	exhausted, _ = tr.Record("p2")
	if exhausted {
		t.Error("p2 should not be exhausted")
	}
}

func TestTracker_ConfigsFor(t *testing.T) {
	tr := limits.New(map[string][]limits.Config{"p1": {{Requests: 100, Window: time.Hour}}})
	cfgs, ok := tr.ConfigsFor("p1")
	if !ok {
		t.Fatal("expected ok=true for p1")
	}
	if len(cfgs) != 1 || cfgs[0].Requests != 100 {
		t.Errorf("want [{100, 1h}], got %v", cfgs)
	}
	_, ok = tr.ConfigsFor("unknown")
	if ok {
		t.Error("expected ok=false for unknown provider")
	}
}
