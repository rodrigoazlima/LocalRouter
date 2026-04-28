package limits_test

import (
	"sync"
	"testing"

	"github.com/rodrigoazlima/localrouter/internal/limits"
)

func TestConcurrencyTracker_Unlimited(t *testing.T) {
	tr := limits.New(nil)
	// No concurrency limits set — every acquire succeeds.
	for i := 0; i < 1000; i++ {
		if !tr.TryAcquireConcurrency("p1") {
			t.Fatal("unlimited tracker should always allow acquire")
		}
	}
}

func TestConcurrencyTracker_LimitEnforced(t *testing.T) {
	tr := limits.New(nil)
	tr.SetConcurrencyLimits(map[string]int{"p1": 2})

	if !tr.TryAcquireConcurrency("p1") {
		t.Fatal("first acquire should succeed")
	}
	if !tr.TryAcquireConcurrency("p1") {
		t.Fatal("second acquire should succeed")
	}
	if tr.TryAcquireConcurrency("p1") {
		t.Fatal("third acquire should fail (limit=2)")
	}
	if tr.ActiveConcurrency("p1") != 2 {
		t.Fatalf("want active=2, got %d", tr.ActiveConcurrency("p1"))
	}
}

func TestConcurrencyTracker_Release(t *testing.T) {
	tr := limits.New(nil)
	tr.SetConcurrencyLimits(map[string]int{"p1": 1})

	if !tr.TryAcquireConcurrency("p1") {
		t.Fatal("first acquire should succeed")
	}
	if tr.TryAcquireConcurrency("p1") {
		t.Fatal("second acquire should fail (limit=1)")
	}
	tr.ReleaseConcurrency("p1")
	if !tr.TryAcquireConcurrency("p1") {
		t.Fatal("acquire after release should succeed")
	}
}

func TestConcurrencyTracker_ReleaseNeverNegative(t *testing.T) {
	tr := limits.New(nil)
	tr.SetConcurrencyLimits(map[string]int{"p1": 5})

	// Release without acquire — must not go negative.
	tr.ReleaseConcurrency("p1")
	tr.ReleaseConcurrency("p1")
	if tr.ActiveConcurrency("p1") != 0 {
		t.Fatalf("active should stay at 0, got %d", tr.ActiveConcurrency("p1"))
	}
}

func TestConcurrencyTracker_Race(t *testing.T) {
	tr := limits.New(nil)
	tr.SetConcurrencyLimits(map[string]int{"p1": 10})

	var wg sync.WaitGroup
	const goroutines = 100
	acquired := make([]bool, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			acquired[idx] = tr.TryAcquireConcurrency("p1")
			if acquired[idx] {
				tr.ReleaseConcurrency("p1")
			}
		}(i)
	}
	wg.Wait()

	// After all goroutines finish, active must be 0.
	if tr.ActiveConcurrency("p1") != 0 {
		t.Fatalf("after all releases active should be 0, got %d", tr.ActiveConcurrency("p1"))
	}
}

func TestConcurrencyTracker_ConcurrencyLimit(t *testing.T) {
	tr := limits.New(nil)
	if tr.ConcurrencyLimit("p1") != 0 {
		t.Error("unconfigured provider should have limit=0")
	}
	tr.SetConcurrencyLimits(map[string]int{"p1": 5})
	if tr.ConcurrencyLimit("p1") != 5 {
		t.Errorf("want limit=5, got %d", tr.ConcurrencyLimit("p1"))
	}
}

func TestConcurrencyTracker_RestoreActiveRequests(t *testing.T) {
	tr := limits.New(nil)
	tr.SetConcurrencyLimits(map[string]int{"p1": 5})

	// Restore within limit.
	tr.RestoreActiveRequests("p1", 3)
	if tr.ActiveConcurrency("p1") != 3 {
		t.Fatalf("want active=3 after restore, got %d", tr.ActiveConcurrency("p1"))
	}

	// Restore exceeding limit → reset to 0 (fail-safe).
	tr.RestoreActiveRequests("p1", 10)
	if tr.ActiveConcurrency("p1") != 3 {
		// Value should be unchanged (10 > limit=5, so ignored).
		t.Fatalf("restore exceeding limit should be ignored, got %d", tr.ActiveConcurrency("p1"))
	}
}

func TestConcurrencyTracker_UnknownProviderIgnored(t *testing.T) {
	tr := limits.New(nil)
	tr.SetConcurrencyLimits(map[string]int{"p1": 3})

	// Operations on unconfigured provider should not panic or error.
	if !tr.TryAcquireConcurrency("p2") {
		t.Fatal("unknown provider should always allow (unlimited)")
	}
	tr.ReleaseConcurrency("p2") // should not panic
	tr.RestoreActiveRequests("p2", 5)
}
