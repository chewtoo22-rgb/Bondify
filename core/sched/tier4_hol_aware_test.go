package sched

import (
	"testing"
	"time"
)

func TestHoLAwareUsesFastPathWhenRoomAvailable(t *testing.T) {
	paths := []Path{
		&fakePath{id: 0, state: StateActive, role: RoleBond, inflight: 0, cwnd: 100, rttMin: 15 * time.Millisecond},
		&fakePath{id: 1, state: StateActive, role: RoleBond, inflight: 0, cwnd: 100, rttMin: 200 * time.Millisecond},
	}
	h := NewHoLAware()
	for i := 0; i < 5; i++ {
		p := h.Next(paths, 100)
		if p == nil || p.ID() != 0 {
			t.Fatalf("expected the fast path (0) with room, got %v", p)
		}
	}
}

func TestHoLAwareSkipsSlowPathWhenNetHarmful(t *testing.T) {
	// Mirrors the HoL gate topology: fast 15ms full, slow 200ms with room. Dumping onto
	// the slow path would arrive far later than just waiting -- HoLAware should refuse.
	paths := []Path{
		&fakePath{id: 0, state: StateActive, role: RoleBond, inflight: 100, cwnd: 100, rttMin: 15 * time.Millisecond},
		&fakePath{id: 1, state: StateActive, role: RoleBond, inflight: 0, cwnd: 100, rttMin: 200 * time.Millisecond},
	}
	h := NewHoLAware()
	if p := h.Next(paths, 100); p != nil {
		t.Fatalf("expected nil (deliberate underutilization), got path %d", p.ID())
	}
}

func TestHoLAwareUsesSlowPathWhenComparable(t *testing.T) {
	// Fast is full, but the alternative is equally fast (a tie): using it is exactly as
	// good as waiting, so HoLAware should use it rather than idle needlessly.
	paths := []Path{
		&fakePath{id: 0, state: StateActive, role: RoleBond, inflight: 100, cwnd: 100, rttMin: 50 * time.Millisecond},
		&fakePath{id: 1, state: StateActive, role: RoleBond, inflight: 0, cwnd: 100, rttMin: 50 * time.Millisecond},
	}
	h := NewHoLAware()
	p := h.Next(paths, 100)
	if p == nil || p.ID() != 1 {
		t.Fatalf("expected the comparable alternate path 1, got %v", p)
	}
}

func TestHoLAwareSharesLoadAcrossEquallyFastPaths(t *testing.T) {
	// Regression test for the exact bug found in CI on a homogeneous two-path run:
	// hol-aware measured roughly half of round-robin's throughput because it locked onto
	// a single "fastest" path (ties always resolved to the same winner) and never gave a
	// second, equally-fast, equally-idle path anything to carry.
	paths := []Path{
		&fakePath{id: 0, state: StateActive, role: RoleBond, inflight: 0, cwnd: 100, rttMin: 20 * time.Millisecond},
		&fakePath{id: 1, state: StateActive, role: RoleBond, inflight: 0, cwnd: 100, rttMin: 20 * time.Millisecond},
	}
	h := NewHoLAware()
	seen := map[uint8]int{}
	for i := 0; i < 6; i++ {
		p := h.Next(paths, 100)
		if p == nil {
			t.Fatal("expected a path, got nil")
		}
		seen[p.ID()]++
	}
	if seen[0] == 0 || seen[1] == 0 {
		t.Fatalf("expected both equally-fast paths to be used, got counts %v", seen)
	}
}

func TestHoLAwareBothPathsUnmeasuredRTT(t *testing.T) {
	// Regression test for a real bug found in CI: the relay side never actively probes
	// (see core/bond/path.go's doc comment on the client/relay asymmetry), so relay-side
	// paths' RTTMin() is always 0/unmeasured. fastestTiedSet used to return an empty
	// primary set whenever the fastest sample itself was the unmeasuredRTT sentinel, which
	// forced every call down the "nothing tied-for-fastest has room" branch even though
	// both paths plainly had room -- and a second bug in that branch's tie-breaking
	// (rtt < slowRTT is never true when both sides equal the sentinel) meant it silently
	// returned nil forever afterward too. Reproduced for real: a two-path tunnel with
	// hol-aware on the relay side stalled completely, sending zero return traffic.
	paths := []Path{
		&fakePath{id: 0, state: StateActive, role: RoleBond, inflight: 0, cwnd: 1 << 20, rttMin: 0},
		&fakePath{id: 1, state: StateActive, role: RoleBond, inflight: 0, cwnd: 1 << 20, rttMin: 0},
	}
	h := NewHoLAware()
	for i := 0; i < 5; i++ {
		p := h.Next(paths, 100)
		if p == nil {
			t.Fatalf("call %d: expected a path, got nil (both paths have room and no RTT data)", i)
		}
	}
}

func TestHoLAwareLambdaDecayEnablesBorderlineSlowPath(t *testing.T) {
	h := NewHoLAware()

	// Warm up: present a scenario where the fast path always has room, so every decision
	// decays lambda. 20 calls at a 0.98 decay factor brings lambda to roughly 0.98^20 =
	// 0.667, comfortably under the 0.833 threshold the borderline case below needs.
	roomy := []Path{
		&fakePath{id: 0, state: StateActive, role: RoleBond, inflight: 0, cwnd: 100, rttMin: 10 * time.Millisecond},
	}
	for i := 0; i < 20; i++ {
		h.Next(roomy, 100)
	}

	// fastRTT=100ms, slowRTT=120ms (1.2x): at lambda=1.0 this would skip (120 > 100); once
	// lambda has decayed below 100/120=0.833, it should switch to using the slow path.
	borderline := []Path{
		&fakePath{id: 0, state: StateActive, role: RoleBond, inflight: 100, cwnd: 100, rttMin: 100 * time.Millisecond},
		&fakePath{id: 1, state: StateActive, role: RoleBond, inflight: 0, cwnd: 100, rttMin: 120 * time.Millisecond},
	}
	p := h.Next(borderline, 100)
	if p == nil || p.ID() != 1 {
		t.Fatalf("expected decayed lambda to permit the borderline slow path, got %v", p)
	}
}

func TestHoLAwareNoRoomAnywhere(t *testing.T) {
	paths := []Path{
		&fakePath{id: 0, state: StateActive, role: RoleBond, inflight: 100, cwnd: 100, rttMin: 15 * time.Millisecond},
		&fakePath{id: 1, state: StateActive, role: RoleBond, inflight: 100, cwnd: 100, rttMin: 200 * time.Millisecond},
	}
	h := NewHoLAware()
	if p := h.Next(paths, 100); p != nil {
		t.Fatalf("expected nil, got path %d", p.ID())
	}
}

func TestHoLAwareEmptySet(t *testing.T) {
	h := NewHoLAware()
	if p := h.Next(nil, 100); p != nil {
		t.Fatalf("expected nil, got %v", p)
	}
}

func TestHoLAwareIgnoresIneligibleRoles(t *testing.T) {
	paths := []Path{
		&fakePath{id: 0, state: StateActive, role: RoleBackup, inflight: 0, cwnd: 100, rttMin: time.Millisecond},
		&fakePath{id: 1, state: StateDead, role: RoleBond, inflight: 0, cwnd: 100, rttMin: time.Millisecond},
	}
	h := NewHoLAware()
	if p := h.Next(paths, 100); p != nil {
		t.Fatalf("expected nil (no ACTIVE+BOND paths), got %v", p)
	}
}
