package state

import (
	"sync"
	"time"
)

type State int

const (
	StateAvailable State = iota
	StateUnhealthy
	StateExhausted
	StateBlocked
)

func (s State) String() string {
	switch s {
	case StateAvailable:
		return "available"
	case StateUnhealthy:
		return "unhealthy"
	case StateExhausted:
		return "exhausted"
	case StateBlocked:
		return "blocked"
	default:
		return "unknown"
	}
}

type HealthReader interface {
	IsReady(id string) bool
}

type Manager struct {
	mu        sync.RWMutex
	blocked   map[string]time.Time // provider id → blocked until
	exhausted map[string]time.Time // provider id → exhausted until
	health    HealthReader
	onSave    func(id string, blockedUntil, exhaustedUntil time.Time)
}

func New(h HealthReader) *Manager {
	return &Manager{
		blocked:   make(map[string]time.Time),
		exhausted: make(map[string]time.Time),
		health:    h,
	}
}

// SetSaveHook registers a callback invoked whenever blocked/exhausted state changes.
func (m *Manager) SetSaveHook(fn func(id string, blockedUntil, exhaustedUntil time.Time)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onSave = fn
}

func (m *Manager) GetState(id string) State {
	now := time.Now()

	m.mu.RLock()
	bu := m.blocked[id]
	eu := m.exhausted[id]
	m.mu.RUnlock()

	if now.Before(bu) {
		return StateBlocked
	}
	if now.Before(eu) {
		return StateExhausted
	}
	if !m.health.IsReady(id) {
		return StateUnhealthy
	}
	return StateAvailable
}

func (m *Manager) Block(id string, d time.Duration) {
	m.mu.Lock()
	until := time.Now().Add(d)
	m.blocked[id] = until
	hook := m.onSave
	eu := m.exhausted[id]
	m.mu.Unlock()
	if hook != nil {
		go hook(id, until, eu)
	}
}

// BlockUntil sets the block expiry to an absolute time (used to restore persisted state).
func (m *Manager) BlockUntil(id string, until time.Time) {
	m.mu.Lock()
	m.blocked[id] = until
	m.mu.Unlock()
}

func (m *Manager) SetExhausted(id string, resetAt time.Time) {
	m.mu.Lock()
	m.exhausted[id] = resetAt
	hook := m.onSave
	bu := m.blocked[id]
	m.mu.Unlock()
	if hook != nil {
		go hook(id, bu, resetAt)
	}
}

func (m *Manager) BlockedUntil(id string) time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.blocked[id]
}

func (m *Manager) ExhaustedUntil(id string) time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.exhausted[id]
}
