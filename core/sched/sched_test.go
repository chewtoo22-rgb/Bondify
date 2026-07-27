package sched

import "testing"

type fakePath struct {
	id       uint8
	state    State
	role     Role
	inflight int64
	cwnd     int64
}

func (p *fakePath) ID() uint8       { return p.id }
func (p *fakePath) State() State    { return p.state }
func (p *fakePath) Role() Role      { return p.role }
func (p *fakePath) InFlight() int64 { return p.inflight }
func (p *fakePath) CWND() int64     { return p.cwnd }

func TestRoundRobinAlternates(t *testing.T) {
	paths := []Path{
		&fakePath{id: 0, state: StateActive, role: RoleBond, cwnd: 1 << 30},
		&fakePath{id: 1, state: StateActive, role: RoleBond, cwnd: 1 << 30},
	}
	rr := NewRoundRobin()
	var seq []uint8
	for i := 0; i < 6; i++ {
		p := rr.Next(paths)
		if p == nil {
			t.Fatal("expected a path, got nil")
		}
		seq = append(seq, p.ID())
	}
	want := []uint8{0, 1, 0, 1, 0, 1}
	for i, id := range seq {
		if id != want[i] {
			t.Fatalf("sequence = %v, want %v", seq, want)
		}
	}
}

func TestRoundRobinSkipsIneligible(t *testing.T) {
	paths := []Path{
		&fakePath{id: 0, state: StateDegraded, role: RoleBond, cwnd: 1 << 30},
		&fakePath{id: 1, state: StateActive, role: RoleBond, cwnd: 1 << 30},
		&fakePath{id: 2, state: StateActive, role: RoleBackup, cwnd: 1 << 30},
		&fakePath{id: 3, state: StateActive, role: RoleBond, cwnd: 1 << 30},
	}
	rr := NewRoundRobin()
	for i := 0; i < 4; i++ {
		p := rr.Next(paths)
		if p == nil {
			t.Fatal("expected a path, got nil")
		}
		if p.ID() != 1 && p.ID() != 3 {
			t.Fatalf("selected ineligible path %d (state=%v role=%v)", p.ID(), p.State(), p.Role())
		}
	}
}

func TestRoundRobinNoneEligible(t *testing.T) {
	paths := []Path{
		&fakePath{id: 0, state: StateDead, role: RoleBond, cwnd: 1 << 30},
		&fakePath{id: 1, state: StateJoining, role: RoleBond, cwnd: 1 << 30},
	}
	rr := NewRoundRobin()
	if p := rr.Next(paths); p != nil {
		t.Fatalf("expected nil, got path %d", p.ID())
	}
}

func TestRoundRobinRespectsCWND(t *testing.T) {
	paths := []Path{
		&fakePath{id: 0, state: StateActive, role: RoleBond, inflight: 100, cwnd: 100}, // full
		&fakePath{id: 1, state: StateActive, role: RoleBond, inflight: 0, cwnd: 100},
	}
	rr := NewRoundRobin()
	for i := 0; i < 3; i++ {
		p := rr.Next(paths)
		if p == nil || p.ID() != 1 {
			t.Fatalf("expected path 1 (path 0 is at cwnd), got %v", p)
		}
	}
}

func TestRoundRobinEmptySet(t *testing.T) {
	rr := NewRoundRobin()
	if p := rr.Next(nil); p != nil {
		t.Fatalf("expected nil for empty path set, got %v", p)
	}
}
