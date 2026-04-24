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
}

func New(h HealthReader) *Manager {
	return &Manager{
		blocked:   make(map[string]time.Time),
		exhausted: make(map[string]time.Time),
		health:    h,
	}
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
	defer m.mu.Unlock()
	m.blocked[id] = time.Now().Add(d)
}

func (m *Manager) SetExhausted(id string, resetAt time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.exhausted[id] = resetAt
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
