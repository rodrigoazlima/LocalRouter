package state_test

import (
	"testing"
	"time"

	"github.com/rodrigoazlima/localrouter/internal/state"
)

type fakeHealth struct {
	ready map[string]bool
}

func (f *fakeHealth) IsReady(id string) bool {
	return f.ready[id]
}

func TestGetState_Available(t *testing.T) {
	h := &fakeHealth{ready: map[string]bool{"p1": true}}
	m := state.New(h)
	if got := m.GetState("p1"); got != state.StateAvailable {
		t.Errorf("want Available, got %v", got)
	}
}

func TestGetState_Unhealthy(t *testing.T) {
	h := &fakeHealth{ready: map[string]bool{"p1": false}}
	m := state.New(h)
	if got := m.GetState("p1"); got != state.StateUnhealthy {
		t.Errorf("want Unhealthy, got %v", got)
	}
}

func TestBlock_BeforeExpiry(t *testing.T) {
	h := &fakeHealth{ready: map[string]bool{"p1": true}}
	m := state.New(h)
	m.Block("p1", time.Minute)
	if got := m.GetState("p1"); got != state.StateBlocked {
		t.Errorf("want Blocked, got %v", got)
	}
}

func TestBlock_AfterExpiry(t *testing.T) {
	h := &fakeHealth{ready: map[string]bool{"p1": true}}
	m := state.New(h)
	m.Block("p1", 10*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	if got := m.GetState("p1"); got != state.StateAvailable {
		t.Errorf("want Available after expiry, got %v", got)
	}
}

func TestExhausted_BeforeReset(t *testing.T) {
	h := &fakeHealth{ready: map[string]bool{"p1": true}}
	m := state.New(h)
	m.SetExhausted("p1", time.Now().Add(time.Minute))
	if got := m.GetState("p1"); got != state.StateExhausted {
		t.Errorf("want Exhausted, got %v", got)
	}
}

func TestExhausted_AfterReset(t *testing.T) {
	h := &fakeHealth{ready: map[string]bool{"p1": true}}
	m := state.New(h)
	m.SetExhausted("p1", time.Now().Add(10*time.Millisecond))
	time.Sleep(100 * time.Millisecond)
	if got := m.GetState("p1"); got != state.StateAvailable {
		t.Errorf("want Available after reset, got %v", got)
	}
}

func TestPrecedence_BlockedOverExhausted(t *testing.T) {
	h := &fakeHealth{ready: map[string]bool{"p1": true}}
	m := state.New(h)
	m.Block("p1", time.Minute)
	m.SetExhausted("p1", time.Now().Add(time.Minute))
	if got := m.GetState("p1"); got != state.StateBlocked {
		t.Errorf("want Blocked (highest precedence), got %v", got)
	}
}

func TestPrecedence_ExhaustedOverUnhealthy(t *testing.T) {
	h := &fakeHealth{ready: map[string]bool{"p1": false}} // unhealthy
	m := state.New(h)
	m.SetExhausted("p1", time.Now().Add(time.Minute))
	if got := m.GetState("p1"); got != state.StateExhausted {
		t.Errorf("want Exhausted over Unhealthy, got %v", got)
	}
}

func TestStateString(t *testing.T) {
	cases := []struct {
		s    state.State
		want string
	}{
		{state.StateAvailable, "available"},
		{state.StateUnhealthy, "unhealthy"},
		{state.StateExhausted, "exhausted"},
		{state.StateBlocked, "blocked"},
	}
	for _, c := range cases {
		if got := c.s.String(); got != c.want {
			t.Errorf("State(%d).String() = %q, want %q", c.s, got, c.want)
		}
	}
}

func TestGetState_UnknownProvider(t *testing.T) {
	m := state.New(&fakeHealth{ready: map[string]bool{}})
	if got := m.GetState("nobody"); got != state.StateUnhealthy {
		t.Errorf("want Unhealthy for unknown provider, got %v", got)
	}
}

func TestBlockedUntil(t *testing.T) {
	h := &fakeHealth{ready: map[string]bool{"p1": true}}
	m := state.New(h)
	if !m.BlockedUntil("p1").IsZero() {
		t.Error("BlockedUntil should be zero before any Block call")
	}
	before := time.Now()
	m.Block("p1", time.Hour)
	after := time.Now()
	bu := m.BlockedUntil("p1")
	if bu.Before(before.Add(time.Hour)) || bu.After(after.Add(time.Hour)) {
		t.Errorf("BlockedUntil out of expected range: %v", bu)
	}
}

func TestExhaustedUntil(t *testing.T) {
	h := &fakeHealth{ready: map[string]bool{"p1": true}}
	m := state.New(h)
	if !m.ExhaustedUntil("p1").IsZero() {
		t.Error("ExhaustedUntil should be zero before any SetExhausted call")
	}
	resetAt := time.Now().Add(time.Hour)
	m.SetExhausted("p1", resetAt)
	if got := m.ExhaustedUntil("p1"); !got.Equal(resetAt) {
		t.Errorf("ExhaustedUntil = %v, want %v", got, resetAt)
	}
}
