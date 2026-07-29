package sched

import (
	"testing"
	"time"
)

func TestMinRTTPrefersLowerRTT(t *testing.T) {
	paths := []Path{
		&fakePath{id: 0, state: StateActive, role: RoleBond, cwnd: 1 << 30, rttMin: 50 * time.Millisecond},
		&fakePath{id: 1, state: StateActive, role: RoleBond, cwnd: 1 << 30, rttMin: 10 * time.Millisecond},
	}
	m := NewMinRTTCwnd()
	for i := 0; i < 5; i++ {
		p := m.Next(paths, 100)
		if p == nil || p.ID() != 1 {
			t.Fatalf("expected the lower-RTT path (1), got %v", p)
		}
	}
}

func TestMinRTTFallsBackWhenFastPathFull(t *testing.T) {
	paths := []Path{
		&fakePath{id: 0, state: StateActive, role: RoleBond, inflight: 100, cwnd: 100, rttMin: 10 * time.Millisecond}, // fast, full
		&fakePath{id: 1, state: StateActive, role: RoleBond, inflight: 0, cwnd: 100, rttMin: 80 * time.Millisecond},   // slow, room
	}
	m := NewMinRTTCwnd()
	p := m.Next(paths, 10)
	if p == nil || p.ID() != 1 {
		t.Fatalf("expected fallback to slow path 1 once fast path 0 is full, got %v", p)
	}
}

func TestMinRTTTreatsUnmeasuredAsWorst(t *testing.T) {
	paths := []Path{
		&fakePath{id: 0, state: StateActive, role: RoleBond, cwnd: 1 << 30, rttMin: 0}, // unmeasured
		&fakePath{id: 1, state: StateActive, role: RoleBond, cwnd: 1 << 30, rttMin: 200 * time.Millisecond},
	}
	m := NewMinRTTCwnd()
	p := m.Next(paths, 100)
	if p == nil || p.ID() != 1 {
		t.Fatalf("expected the measured (if slower-looking) path 1 over the unmeasured path 0, got %v", p)
	}
}

func TestMinRTTSharesLoadAcrossEquallyFastPaths(t *testing.T) {
	// Regression test: found for real on a homogeneous two-path CI run, where a strict
	// single-winner comparison let one path absorb 100% of traffic forever because its own
	// (self-referential) congestion window never signaled "full" -- the second, equally
	// capable path never got anything to prove itself against. Two paths tied at the same
	// RTT should alternate, not perpetually favor one.
	paths := []Path{
		&fakePath{id: 0, state: StateActive, role: RoleBond, cwnd: 1 << 30, rttMin: 20 * time.Millisecond},
		&fakePath{id: 1, state: StateActive, role: RoleBond, cwnd: 1 << 30, rttMin: 20 * time.Millisecond},
	}
	m := NewMinRTTCwnd()
	var seq []uint8
	for i := 0; i < 6; i++ {
		p := m.Next(paths, 100)
		if p == nil {
			t.Fatal("expected a path, got nil")
		}
		seq = append(seq, p.ID())
	}
	want := []uint8{0, 1, 0, 1, 0, 1}
	for i, id := range seq {
		if id != want[i] {
			t.Fatalf("sequence = %v, want %v (tied paths should alternate)", seq, want)
		}
	}
}

func TestMinRTTBothPathsUnmeasuredRTT(t *testing.T) {
	// Same relay-side scenario as TestHoLAwareBothPathsUnmeasuredRTT: MinRTTCwnd shares
	// fastestTiedSet with HoLAware, so it had the identical bug -- both paths unmeasured
	// meant an empty primary set and a permanent nil return, even with room on both paths.
	paths := []Path{
		&fakePath{id: 0, state: StateActive, role: RoleBond, cwnd: 1 << 20, rttMin: 0},
		&fakePath{id: 1, state: StateActive, role: RoleBond, cwnd: 1 << 20, rttMin: 0},
	}
	m := NewMinRTTCwnd()
	for i := 0; i < 5; i++ {
		p := m.Next(paths, 100)
		if p == nil {
			t.Fatalf("call %d: expected a path, got nil (both paths have room and no RTT data)", i)
		}
	}
}

func TestMinRTTDoesNotCacheUnmeasuredTie(t *testing.T) {
	fast := &fakePath{id: 0, state: StateActive, role: RoleBond, cwnd: 1 << 20}
	slow := &fakePath{id: 1, state: StateActive, role: RoleBond, cwnd: 1 << 20}
	paths := []Path{fast, slow}
	m := NewMinRTTCwnd()
	_ = m.Next(paths, 100)

	fast.rttMin = 15 * time.Millisecond
	slow.rttMin = 200 * time.Millisecond
	if p := m.Next(paths, 100); p == nil || p.ID() != fast.ID() {
		t.Fatalf("first measured decision = %v, want fast path %d", p, fast.ID())
	}
}

func TestMinRTTEmptySet(t *testing.T) {
	m := NewMinRTTCwnd()
	if p := m.Next(nil, 100); p != nil {
		t.Fatalf("expected nil, got %v", p)
	}
}

func TestMinRTTNoneEligible(t *testing.T) {
	paths := []Path{
		&fakePath{id: 0, state: StateDegraded, role: RoleBond, cwnd: 1 << 30, rttMin: time.Millisecond},
	}
	m := NewMinRTTCwnd()
	if p := m.Next(paths, 100); p != nil {
		t.Fatalf("expected nil, got %v", p)
	}
}

func TestMinRTTSteadyStateDoesNotAllocate(t *testing.T) {
	paths := []Path{
		&fakePath{id: 0, state: StateActive, role: RoleBond, cwnd: 1 << 30, rttMin: 15 * time.Millisecond},
		&fakePath{id: 1, state: StateActive, role: RoleBond, cwnd: 1 << 30, rttMin: 200 * time.Millisecond},
	}
	m := NewMinRTTCwnd()
	allocs := testing.AllocsPerRun(1000, func() {
		if p := m.Next(paths, 1400); p == nil {
			panic("unexpected nil path")
		}
	})
	if allocs != 0 {
		t.Fatalf("steady-state Next allocated %v times per packet, want 0", allocs)
	}
}
