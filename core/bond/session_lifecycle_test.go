package bond

import (
	"testing"
	"time"

	"github.com/chewtoo22-rgb/bondify/core/crypto"
	"github.com/chewtoo22-rgb/bondify/core/reorder"
	"github.com/chewtoo22-rgb/bondify/core/sched"
)

func makeLifecycleTestRelay(t *testing.T, cidr string) *Relay {
	t.Helper()
	pool, err := NewIPPool(cidr)
	if err != nil {
		t.Fatal(err)
	}
	return &Relay{
		pool:        pool,
		byIndex:     make(map[uint32]*relaySession),
		byTunnelIP:  make(map[string]*relaySession),
		byClientKey: make(map[[crypto.KeyLen]byte]*relaySession),
	}
}

func addLifecycleTestSession(t *testing.T, r *Relay, idx uint32, keyByte byte, started time.Time, state sched.State, lastActivity time.Time) *relaySession {
	t.Helper()
	ip, err := r.pool.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	p := NewPath(PathZero, nil)
	p.state.Store(int32(state))
	if !lastActivity.IsZero() {
		p.lastProbeAckAt.Store(lastActivity.UnixNano())
	}
	s := &relaySession{
		sessionIndex: idx,
		tunnelIP:     ip,
		startedAt:    started,
		paths:        map[uint8]*Path{PathZero: p},
		reorderBuf:   reorder.New(reorder.DefaultDeadlineMin, 0),
	}
	s.schedPathView.Store([]sched.Path{p})
	var key [crypto.KeyLen]byte
	key[0] = keyByte
	r.byIndex[idx] = s
	r.byTunnelIP[ip.String()] = s
	r.byClientKey[key] = s
	return s
}

func TestReapIdleSessionRemovesAllIndexesAndReusesLease(t *testing.T) {
	now := time.Now()
	idle := time.Minute
	r := makeLifecycleTestRelay(t, "10.77.0.0/29")
	s := addLifecycleTestSession(t, r, 7, 1, now.Add(-3*idle), sched.StateDead, now.Add(-2*idle))
	oldIP := append([]byte(nil), s.tunnelIP...)

	if got := r.reapIdleSessions(now, idle); got != 1 {
		t.Fatalf("reaped=%d, want 1", got)
	}
	if len(r.byIndex) != 0 || len(r.byTunnelIP) != 0 || len(r.byClientKey) != 0 {
		t.Fatalf("session indexes not fully cleared: byIndex=%d byTunnelIP=%d byClientKey=%d", len(r.byIndex), len(r.byTunnelIP), len(r.byClientKey))
	}

	reused, err := r.pool.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	if !reused.Equal(oldIP) {
		t.Fatalf("reused ip=%s, want reclaimed %s", reused, oldIP)
	}
	if r.removeSession(s) {
		t.Fatal("duplicate removeSession unexpectedly succeeded")
	}
	next, err := r.pool.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	if next.Equal(oldIP) {
		t.Fatalf("duplicate cleanup double-released %s", oldIP)
	}
}

func TestReapIdleSessionDoesNotRemoveActiveOrRecoveringPath(t *testing.T) {
	now := time.Now()
	idle := time.Minute
	for _, state := range []sched.State{sched.StateJoining, sched.StateActive, sched.StateDegraded} {
		t.Run(state.String(), func(t *testing.T) {
			r := makeLifecycleTestRelay(t, "10.77.0.0/29")
			s := addLifecycleTestSession(t, r, 9, 2, now.Add(-10*idle), state, now.Add(-10*idle))
			if sessionReapEligible(s, now, idle) {
				t.Fatalf("state %v marked reap-eligible", state)
			}
			if got := r.reapIdleSessions(now, idle); got != 0 {
				t.Fatalf("state %v reaped=%d, want 0", state, got)
			}
		})
	}
}

func TestReapIdleSessionHonorsMostRecentAuthenticatedPathActivity(t *testing.T) {
	now := time.Now()
	idle := time.Minute
	r := makeLifecycleTestRelay(t, "10.77.0.0/29")
	s := addLifecycleTestSession(t, r, 11, 3, now.Add(-10*idle), sched.StateDead, now.Add(-idle/2))
	if sessionReapEligible(s, now, idle) {
		t.Fatal("recent authenticated path activity should keep session alive")
	}
}

func TestZeroPathSessionGetsFreshObservedIdleWindow(t *testing.T) {
	now := time.Now()
	idle := time.Minute
	r := makeLifecycleTestRelay(t, "10.77.0.0/29")
	s := addLifecycleTestSession(t, r, 12, 4, now.Add(-10*idle), sched.StateDead, now.Add(-10*idle))
	s.paths = map[uint8]*Path{}
	s.schedPathView.Store([]sched.Path(nil))
	zeroPathSince := make(map[*relaySession]time.Time)

	if got := r.reapIdleSessionsTracked(now, idle, zeroPathSince); got != 0 {
		t.Fatalf("first zero-path observation reaped=%d, want 0", got)
	}
	if since, ok := zeroPathSince[s]; !ok || !since.Equal(now) {
		t.Fatalf("zero-path idle window not initialized at observation time: ok=%v since=%v", ok, since)
	}
	if got := r.reapIdleSessionsTracked(now.Add(idle-time.Second), idle, zeroPathSince); got != 0 {
		t.Fatalf("zero-path session reaped before grace elapsed: %d", got)
	}
	if got := r.reapIdleSessionsTracked(now.Add(idle), idle, zeroPathSince); got != 1 {
		t.Fatalf("zero-path session reaped=%d after full grace, want 1", got)
	}
}

func TestZeroPathRecoveryResetsObservedIdleWindow(t *testing.T) {
	now := time.Now()
	idle := time.Minute
	r := makeLifecycleTestRelay(t, "10.77.0.0/29")
	s := addLifecycleTestSession(t, r, 13, 5, now.Add(-10*idle), sched.StateDead, now.Add(-10*idle))
	zeroPathSince := make(map[*relaySession]time.Time)

	// First outage starts a grace window.
	s.paths = map[uint8]*Path{}
	s.schedPathView.Store([]sched.Path(nil))
	if got := r.reapIdleSessionsTracked(now, idle, zeroPathSince); got != 0 {
		t.Fatalf("first zero-path observation reaped=%d, want 0", got)
	}
	if _, ok := zeroPathSince[s]; !ok {
		t.Fatal("first zero-path grace window was not recorded")
	}

	// The path recovers before the grace expires. That must clear the old zero-path timer,
	// otherwise a later outage could inherit stale idle time and be reaped prematurely.
	recovered := NewPath(PathZero, nil)
	recovered.SetActive()
	s.paths = map[uint8]*Path{PathZero: recovered}
	s.schedPathView.Store([]sched.Path{recovered})
	if got := r.reapIdleSessionsTracked(now.Add(idle/2), idle, zeroPathSince); got != 0 {
		t.Fatalf("recovered session reaped=%d, want 0", got)
	}
	if _, ok := zeroPathSince[s]; ok {
		t.Fatal("recovered path did not clear stale zero-path grace window")
	}

	// Losing every path again starts a brand-new grace window at the second outage.
	secondOutage := now.Add(3 * idle / 4)
	s.paths = map[uint8]*Path{}
	s.schedPathView.Store([]sched.Path(nil))
	if got := r.reapIdleSessionsTracked(secondOutage, idle, zeroPathSince); got != 0 {
		t.Fatalf("second zero-path observation reaped=%d, want 0", got)
	}
	if since := zeroPathSince[s]; !since.Equal(secondOutage) {
		t.Fatalf("second zero-path grace started at %v, want %v", since, secondOutage)
	}
	if got := r.reapIdleSessionsTracked(secondOutage.Add(idle-time.Second), idle, zeroPathSince); got != 0 {
		t.Fatalf("second outage reaped before fresh grace elapsed: %d", got)
	}
	if got := r.reapIdleSessionsTracked(secondOutage.Add(idle), idle, zeroPathSince); got != 1 {
		t.Fatalf("second outage reaped=%d after fresh grace, want 1", got)
	}
}

func TestSessionChurnDoesNotPermanentlyExhaustPool(t *testing.T) {
	now := time.Now()
	idle := time.Minute
	r := makeLifecycleTestRelay(t, "10.88.0.0/28")

	const clients = 13 // /28 minus network, gateway, and broadcast
	for i := 0; i < clients; i++ {
		addLifecycleTestSession(t, r, uint32(i+1), byte(i+1), now.Add(-3*idle), sched.StateDead, now.Add(-2*idle))
	}
	if _, err := r.pool.Allocate(); err == nil {
		t.Fatal("expected pool exhaustion before reclamation")
	}
	if got := r.reapIdleSessions(now, idle); got != clients {
		t.Fatalf("reaped=%d, want %d", got, clients)
	}
	for i := 0; i < clients; i++ {
		if _, err := r.pool.Allocate(); err != nil {
			t.Fatalf("allocation %d after reclamation: %v", i, err)
		}
	}
}
