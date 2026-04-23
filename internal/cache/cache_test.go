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
