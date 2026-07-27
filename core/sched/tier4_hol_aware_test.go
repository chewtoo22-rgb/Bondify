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
