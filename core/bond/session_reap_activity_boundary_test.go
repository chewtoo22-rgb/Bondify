package bond

import (
	"testing"
	"time"

	"github.com/chewtoo22-rgb/bondify/core/sched"
)

func TestSessionReapExactNowActivityIsNotTreatedAsNeverObserved(t *testing.T) {
	now := time.Unix(1_800_000_000, 123_456_789)
	idle := time.Minute
	r := makeLifecycleTestRelay(t, "10.77.0.0/29")
	s := addLifecycleTestSession(t, r, 21, 9, now.Add(-10*idle), sched.StateDead, now)

	if sessionReapEligible(s, now, idle) {
		t.Fatal("authenticated activity exactly at reap time must keep the session alive")
	}
	if got := r.reapIdleSessions(now, idle); got != 0 {
		t.Fatalf("reaped=%d, want 0 after exact-now authenticated activity", got)
	}
}

func TestSessionReapFutureActivityClampsToNowAfterClockRollback(t *testing.T) {
	now := time.Unix(1_800_000_100, 0)
	idle := time.Minute
	r := makeLifecycleTestRelay(t, "10.77.0.0/29")
	s := addLifecycleTestSession(t, r, 22, 10, now.Add(-10*idle), sched.StateDead, now.Add(5*time.Second))

	if sessionReapEligible(s, now, idle) {
		t.Fatal("future authenticated activity after a wall-clock rollback must not make the session look old")
	}
	if got := r.reapIdleSessions(now, idle); got != 0 {
		t.Fatalf("reaped=%d, want 0 with a future activity timestamp", got)
	}
}

func TestSessionReapNeverObservedDeadPathStillUsesSessionAge(t *testing.T) {
	now := time.Unix(1_800_000_200, 0)
	idle := time.Minute
	r := makeLifecycleTestRelay(t, "10.77.0.0/29")
	s := addLifecycleTestSession(t, r, 23, 11, now.Add(-2*idle), sched.StateDead, time.Time{})

	if !sessionReapEligible(s, now, idle) {
		t.Fatal("a never-observed DEAD path must still allow an old session to become reap-eligible")
	}
	if got := r.reapIdleSessions(now, idle); got != 1 {
		t.Fatalf("reaped=%d, want 1 for an old session whose DEAD path never observed traffic", got)
	}
}
