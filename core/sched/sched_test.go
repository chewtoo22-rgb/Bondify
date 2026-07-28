package sched

import (
	"testing"
	"time"
)

type fakePath struct {
	id       uint8
	state    State
	role     Role
	inflight int64
	cwnd     int64
	rttMin   time.Duration
	goodput  float64
}

func (p *fakePath) ID() uint8             { return p.id }
func (p *fakePath) State() State          { return p.state }
func (p *fakePath) Role() Role            { return p.role }
func (p *fakePath) InFlight() int64       { return p.inflight }
func (p *fakePath) CWND() int64           { return p.cwnd }
func (p *fakePath) RTTMin() time.Duration { return p.rttMin }
func (p *fakePath) Goodput() float64      { return p.goodput }

func TestRoundRobinAlternates(t *testing.T) {
	paths := []Path{
		&fakePath{id: 0, state: StateActive, role: RoleBond, cwnd: 1 << 30},
		&fakePath{id: 1, state: StateActive, role: RoleBond, cwnd: 1 << 30},
	}
	rr := NewRoundRobin()
	var seq []uint8
	for i := 0; i < 6; i++ {
		p := rr.Next(paths, 100)
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
		p := rr.Next(paths, 100)
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
	if p := rr.Next(paths, 100); p != nil {
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
		p := rr.Next(paths, 100)
		if p == nil || p.ID() != 1 {
			t.Fatalf("expected path 1 (path 0 is at cwnd), got %v", p)
		}
	}
}

func TestRoundRobinEmptySet(t *testing.T) {
	rr := NewRoundRobin()
	if p := rr.Next(nil, 100); p != nil {
		t.Fatalf("expected nil for empty path set, got %v", p)
	}
}

func TestSchedulerFactory(t *testing.T) {
	for _, name := range []string{"", "round-robin", "weighted-goodput", "min-rtt-cwnd", "hol-aware"} {
		s, err := New(name)
		if err != nil {
			t.Errorf("New(%q) error: %v", name, err)
			continue
		}
		if s == nil {
			t.Errorf("New(%q) returned nil scheduler", name)
		}
	}
	if _, err := New("nonexistent-tier"); err == nil {
		t.Error("New(\"nonexistent-tier\") expected an error, got nil")
	}
}
