package limits

import (
	"sync"
	"time"
)

// Config holds rate-limit parameters for a single provider.
type Config struct {
	Requests int
	Window   time.Duration
}

// window tracks the state of one fixed window for a provider.
type window struct {
	count   int
	resetAt time.Time
}

// Tracker counts requests per provider and enforces fixed-window limits.
// It is safe for concurrent use.
type Tracker struct {
	mu      sync.Mutex
	configs map[string]Config
	windows map[string]*window
}

// New creates a Tracker with the given per-provider configs.
// The map is copied so later mutations by the caller do not affect the tracker.
// A nil configs is treated as an empty map (no limits for anyone).
func New(configs map[string]Config) *Tracker {
	cp := make(map[string]Config, len(configs))
	for k, v := range configs {
		cp[k] = v
	}
	return &Tracker{
		configs: cp,
		windows: make(map[string]*window),
	}
}

// Record increments the request counter for id and returns whether the limit
// is exhausted and when the current window will reset.
//
// If no limit is configured for id, Record always returns (false, zero).
// The window starts on the first Record call and resets lazily when
// time.Now() is past the window's resetAt timestamp.
func (t *Tracker) Record(id string) (exhausted bool, resetAt time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()

	cfg, ok := t.configs[id]
	if !ok {
		// No limit configured — never exhausted.
		return false, time.Time{}
	}

	now := time.Now()

	w, exists := t.windows[id]
	if !exists {
		w = &window{}
		t.windows[id] = w
	}

	// Lazy reset: if the window has expired, start a fresh one.
	if !w.resetAt.IsZero() && now.After(w.resetAt) {
		w.count = 0
		w.resetAt = time.Time{}
	}

	// Start window on first request (or after reset).
	if w.resetAt.IsZero() {
		w.resetAt = now.Add(cfg.Window)
	}

	w.count++

	if w.count > cfg.Requests {
		return true, w.resetAt
	}

	return false, time.Time{}
}

// ResetAt returns when the current window expires for id.
// Returns the zero time if no window has been started yet (no Record call
// made) or if id has no configured limit.
func (t *Tracker) ResetAt(id string) time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()

	w, ok := t.windows[id]
	if !ok {
		return time.Time{}
	}
	return w.resetAt
}

// ConfigFor returns the Config for id and true if a limit is configured for
// that provider. Returns the zero Config and false for unknown providers.
func (t *Tracker) ConfigFor(id string) (Config, bool) {
	// configs is read-only after construction, no lock needed.
	cfg, ok := t.configs[id]
	return cfg, ok
}
