package limits

import (
	"sync"
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

// Tracker counts requests per provider and enforces fixed-window limits.
// Multiple windows per provider are all enforced: a request is exhausted if
// any window is over its limit.
// It is safe for concurrent use.
type Tracker struct {
	mu      sync.Mutex
	configs map[string][]Config
	windows map[string][]*window
	onSave  func(id string, states []WindowState)
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
	}
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
