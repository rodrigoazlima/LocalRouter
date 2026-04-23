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

func (c *Cache) Unblock(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, id)
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
