package limits

import (
	"sync"
	"sync/atomic"
	"time"
)

// Config holds rate-limit parameters for a single window.
type Config struct {
	Requests int
	Window   time.Duration
}

// WindowState captures the state of one window for persistence.
type WindowState struct {
	Count   int
	ResetAt time.Time
}

// window tracks the in-memory state of one fixed window.
type window struct {
	count   int
	resetAt time.Time
}

// ConcurrencyTracker limits in-flight requests atomically with no mutexes.
type ConcurrencyTracker struct {
	active atomic.Int64
	limit  int64 // 0 = unlimited
}

// TryAcquire increments active if active < limit (or limit == 0). Returns true on success.
func (c *ConcurrencyTracker) TryAcquire() bool {
	if c.limit == 0 {
		c.active.Add(1)
		return true
	}
	for {
		cur := c.active.Load()
		if cur >= c.limit {
			return false
		}
		if c.active.CompareAndSwap(cur, cur+1) {
			return true
		}
	}
}

// Release decrements active, never below zero.
func (c *ConcurrencyTracker) Release() {
	for {
		cur := c.active.Load()
		if cur <= 0 {
			return
		}
		if c.active.CompareAndSwap(cur, cur-1) {
			return
		}
	}
}

// Active returns the current in-flight count.
func (c *ConcurrencyTracker) Active() int64 { return c.active.Load() }

// Limit returns the configured cap (0 = unlimited).
func (c *ConcurrencyTracker) Limit() int64 { return c.limit }

// Tracker counts requests per provider and enforces fixed-window limits.
// Multiple windows per provider are all enforced: a request is exhausted if
// any window is over its limit.
// It is safe for concurrent use.
type Tracker struct {
	mu      sync.Mutex
	configs map[string][]Config
	windows map[string][]*window
	blocked map[string]time.Time // upstream-reported cooldowns (e.g. per-model 429)
	onSave  func(id string, states []WindowState)

	// concurrency is set once via SetConcurrencyLimits and read atomically thereafter.
	concurrency atomic.Pointer[map[string]*ConcurrencyTracker]
}

// New creates a Tracker with the given per-provider configs.
// The map is copied so later mutations by the caller do not affect the tracker.
func New(configs map[string][]Config) *Tracker {
	cp := make(map[string][]Config, len(configs))
	for k, v := range configs {
		cp[k] = append([]Config(nil), v...)
	}
	return &Tracker{
		configs: cp,
		windows: make(map[string][]*window),
		blocked: make(map[string]time.Time),
	}
}

// SetConcurrencyLimits configures per-id concurrency caps.
// limits maps id → max concurrent requests; 0 or absent means unlimited.
// Safe to call once before concurrent use; subsequent calls replace the map atomically.
func (t *Tracker) SetConcurrencyLimits(limits map[string]int) {
	m := make(map[string]*ConcurrencyTracker, len(limits))
	for id, lim := range limits {
		if lim > 0 {
			m[id] = &ConcurrencyTracker{limit: int64(lim)}
		}
	}
	t.concurrency.Store(&m)
}

// TryAcquireConcurrency attempts to acquire a concurrency slot for id.
// Returns true when no limit is configured or a slot is available.
func (t *Tracker) TryAcquireConcurrency(id string) bool {
	ct := t.concurrencyFor(id)
	if ct == nil {
		return true
	}
	return ct.TryAcquire()
}

// ReleaseConcurrency releases a previously-acquired slot for id.
func (t *Tracker) ReleaseConcurrency(id string) {
	ct := t.concurrencyFor(id)
	if ct != nil {
		ct.Release()
	}
}

// ActiveConcurrency returns current in-flight requests for id (0 if unlimited/unconfigured).
func (t *Tracker) ActiveConcurrency(id string) int64 {
	ct := t.concurrencyFor(id)
	if ct == nil {
		return 0
	}
	return ct.Active()
}

// ConcurrencyLimit returns the configured concurrency cap for id (0 = unlimited/unconfigured).
func (t *Tracker) ConcurrencyLimit(id string) int64 {
	ct := t.concurrencyFor(id)
	if ct == nil {
		return 0
	}
	return ct.Limit()
}

func (t *Tracker) concurrencyFor(id string) *ConcurrencyTracker {
	mp := t.concurrency.Load()
	if mp == nil {
		return nil
	}
	return (*mp)[id]
}

// SetSaveHook registers a callback invoked after every Record call.
func (t *Tracker) SetSaveHook(fn func(id string, states []WindowState)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.onSave = fn
}

// RestoreWindows restores previously persisted window states for id.
// Entries whose ResetAt is in the past are ignored.
// Called at startup; index i of states corresponds to config entry i.
func (t *Tracker) RestoreWindows(id string, states []WindowState) {
	now := time.Now()
	cfgs, ok := t.configs[id]
	if !ok {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	ws := t.ensureWindows(id, cfgs)
	for i, s := range states {
		if i >= len(ws) {
			break
		}
		if now.Before(s.ResetAt) {
			ws[i].count = s.Count
			ws[i].resetAt = s.ResetAt
		}
	}
}

// RestoreActiveRequests restores a persisted in-flight count for id.
// If the value exceeds the configured concurrency limit, it is reset to 0 (fail-safe).
func (t *Tracker) RestoreActiveRequests(id string, active int) {
	ct := t.concurrencyFor(id)
	if ct == nil {
		return
	}
	if active < 0 || (ct.limit > 0 && int64(active) > ct.limit) {
		return // mismatch → reset (start at 0)
	}
	ct.active.Store(int64(active))
}

// Record increments the request counter for id across all configured windows
// and returns whether any window is exhausted and the earliest reset time among
// exhausted windows.
//
// If no limit is configured for id, Record always returns (false, zero).
func (t *Tracker) Record(id string) (exhausted bool, resetAt time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()

	cfgs, ok := t.configs[id]
	if !ok {
		return false, time.Time{}
	}

	now := time.Now()
	ws := t.ensureWindows(id, cfgs)

	for i, cfg := range cfgs {
		w := ws[i]

		// Lazy reset.
		if !w.resetAt.IsZero() && now.After(w.resetAt) {
			w.count = 0
			w.resetAt = time.Time{}
		}
		if w.resetAt.IsZero() {
			w.resetAt = now.Add(cfg.Window)
		}

		w.count++

		if w.count > cfg.Requests {
			if !exhausted || w.resetAt.Before(resetAt) {
				resetAt = w.resetAt
			}
			exhausted = true
		}
	}

	if hook := t.onSave; hook != nil {
		states := snapshotWindows(ws)
		go hook(id, states)
	}

	if exhausted {
		return true, resetAt
	}
	return false, time.Time{}
}

// ResetAt returns the earliest non-zero window reset time for id.
func (t *Tracker) ResetAt(id string) time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()

	ws, ok := t.windows[id]
	if !ok {
		return time.Time{}
	}
	var earliest time.Time
	for _, w := range ws {
		if !w.resetAt.IsZero() && (earliest.IsZero() || w.resetAt.Before(earliest)) {
			earliest = w.resetAt
		}
	}
	return earliest
}

// ConfigsFor returns all configs for id.
func (t *Tracker) ConfigsFor(id string) ([]Config, bool) {
	cfgs, ok := t.configs[id]
	return cfgs, ok
}

// WindowStates returns a snapshot of all window states for id (nil if unknown).
func (t *Tracker) WindowStates(id string) []WindowState {
	t.mu.Lock()
	defer t.mu.Unlock()
	ws, ok := t.windows[id]
	if !ok {
		return nil
	}
	return snapshotWindows(ws)
}

// ModelBlockedUntil returns the upstream-cooldown expiry for key (zero if not blocked).
func (t *Tracker) ModelBlockedUntil(key string) time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.blocked[key]
}

// Block marks key as blocked until until. Used to enforce upstream-reported cooldowns.
func (t *Tracker) Block(key string, until time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.blocked[key] = until
}

// IsBlocked reports whether key is currently under an upstream-reported cooldown.
func (t *Tracker) IsBlocked(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	until, ok := t.blocked[key]
	if !ok {
		return false
	}
	if time.Now().After(until) {
		delete(t.blocked, key)
		return false
	}
	return true
}

// ensureWindows returns (creating if needed) the window slice for id, sized to match cfgs.
// Must be called with t.mu held.
func (t *Tracker) ensureWindows(id string, cfgs []Config) []*window {
	ws := t.windows[id]
	for len(ws) < len(cfgs) {
		ws = append(ws, &window{})
	}
	t.windows[id] = ws
	return ws
}

func snapshotWindows(ws []*window) []WindowState {
	out := make([]WindowState, len(ws))
	for i, w := range ws {
		out[i] = WindowState{Count: w.count, ResetAt: w.resetAt}
	}
	return out
}
