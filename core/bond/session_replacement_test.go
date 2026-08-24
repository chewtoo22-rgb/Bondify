package bond

import (
	"testing"
	"time"

	"github.com/chewtoo22-rgb/bondify/core/reorder"
)

func TestReplacedSessionCannotReleaseCurrentLease(t *testing.T) {
	now := time.Now()
	r := makeLifecycleTestRelay(t, "10.77.0.0/29")
	old := addLifecycleTestSession(t, r, 41, 9, now, 0, time.Time{})
	oldIP := append([]byte(nil), old.tunnelIP...)

	// Simulate a same-client reconnect that has already atomically replaced every lookup
	// with a fresh session while intentionally reusing the stable session index and tunnel
	// lease. A stale lifecycle cleanup for old must not return that lease to the pool.
	replacement := &relaySession{
		sessionIndex: old.sessionIndex,
		tunnelIP:     append([]byte(nil), old.tunnelIP...),
		startedAt:    now,
		paths:        make(map[uint8]*Path),
		reorderBuf:   reorder.New(reorder.DefaultDeadlineMin, 0),
	}
	r.byIndex[replacement.sessionIndex] = replacement
	r.byTunnelIP[replacement.tunnelIP.String()] = replacement
	for key := range r.byClientKey {
		r.byClientKey[key] = replacement
	}

	if r.removeSession(old) {
		t.Fatal("stale replaced session unexpectedly removed current session")
	}

	// If the stale cleanup had released replacement's live lease, the pool's LIFO reuse
	// would hand oldIP straight back out here.
	next, err := r.pool.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	if next.Equal(oldIP) {
		t.Fatalf("stale cleanup released live replacement lease %s", oldIP)
	}
	r.pool.Release(next)

	if !r.removeSession(replacement) {
		t.Fatal("current replacement session was not removable")
	}
	reused, err := r.pool.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	if !reused.Equal(oldIP) {
		t.Fatalf("released replacement lease=%s, want %s", reused, oldIP)
	}
}
