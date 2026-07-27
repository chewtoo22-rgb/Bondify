package sched

import "testing"

func TestWeightedGoodputFavorsHigherGoodput(t *testing.T) {
	a := &fakePath{id: 0, state: StateActive, role: RoleBond, cwnd: 1 << 30, goodput: 10_000_000}
	b := &fakePath{id: 1, state: StateActive, role: RoleBond, cwnd: 1 << 30, goodput: 1_000_000}
	paths := []Path{a, b}

	w := NewWeightedGoodput()
	counts := map[uint8]int{}
	const n = 2000
	for i := 0; i < n; i++ {
		p := w.Next(paths, 1000)
		if p == nil {
			t.Fatal("expected a path, got nil")
		}
		counts[p.ID()]++
	}

	ratio := float64(counts[0]) / float64(counts[1])
	// True DRR with a 10:1 quantum ratio converges to ~10:1 picks; allow generous slack for
	// the mixed real/floor quantum arithmetic.
	if ratio < 6 || ratio > 14 {
		t.Errorf("picks a=%d b=%d, ratio=%.2f, want roughly 10:1", counts[0], counts[1], ratio)
	}
}

func TestWeightedGoodputZeroGoodputSplitsEvenly(t *testing.T) {
	a := &fakePath{id: 0, state: StateActive, role: RoleBond, cwnd: 1 << 30, goodput: 0}
	b := &fakePath{id: 1, state: StateActive, role: RoleBond, cwnd: 1 << 30, goodput: 0}
	paths := []Path{a, b}

	w := NewWeightedGoodput()
	counts := map[uint8]int{}
	const n = 200
	for i := 0; i < n; i++ {
		p := w.Next(paths, 1000)
		counts[p.ID()]++
	}
	diff := counts[0] - counts[1]
	if diff < -2 || diff > 2 {
		t.Errorf("picks a=%d b=%d, want roughly equal (both fall back to the quantum floor)", counts[0], counts[1])
	}
}

func TestWeightedGoodputSkipsIneligible(t *testing.T) {
	paths := []Path{
		&fakePath{id: 0, state: StateDegraded, role: RoleBond, cwnd: 1 << 30, goodput: 1e9},
		&fakePath{id: 1, state: StateActive, role: RoleBond, cwnd: 1 << 30, goodput: 1},
		&fakePath{id: 2, state: StateActive, role: RoleBackup, cwnd: 1 << 30, goodput: 1e9},
	}
	w := NewWeightedGoodput()
	for i := 0; i < 5; i++ {
		p := w.Next(paths, 1000)
		if p == nil || p.ID() != 1 {
			t.Fatalf("expected only eligible path 1, got %v", p)
		}
	}
}

func TestWeightedGoodputRespectsCWND(t *testing.T) {
	paths := []Path{
		&fakePath{id: 0, state: StateActive, role: RoleBond, inflight: 100, cwnd: 100, goodput: 1e9},
		&fakePath{id: 1, state: StateActive, role: RoleBond, inflight: 0, cwnd: 100, goodput: 1},
	}
	w := NewWeightedGoodput()
	for i := 0; i < 3; i++ {
		p := w.Next(paths, 10)
		if p == nil || p.ID() != 1 {
			t.Fatalf("expected path 1 (path 0 is at cwnd), got %v", p)
		}
	}
}

func TestWeightedGoodputEmptySet(t *testing.T) {
	w := NewWeightedGoodput()
	if p := w.Next(nil, 100); p != nil {
		t.Fatalf("expected nil for empty path set, got %v", p)
	}
}

func TestWeightedGoodputNoneEligible(t *testing.T) {
	paths := []Path{
		&fakePath{id: 0, state: StateDead, role: RoleBond, cwnd: 1 << 30},
	}
	w := NewWeightedGoodput()
	if p := w.Next(paths, 100); p != nil {
		t.Fatalf("expected nil, got %v", p)
	}
}
