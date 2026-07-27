// Package sched implements BOND/1's scheduler ladder (ARCHITECTURE.md §2.1). Phase 2
// implements only Tier 1 (round robin) — the baseline that proves framing, path
// management, and reordering work, and the mode every later tier must still be
// benchmarked against. Tiers 2-4 land in phase 3.
package sched

import "sync"

// State is a path's position in the PROTOCOL.md §5 lifecycle state machine.
type State int32

const (
	StateJoining State = iota
	StateActive
	StateDegraded
	StateDead
)

func (s State) String() string {
	switch s {
	case StateJoining:
		return "JOINING"
	case StateActive:
		return "ACTIVE"
	case StateDegraded:
		return "DEGRADED"
	case StateDead:
		return "DEAD"
	default:
		return "UNKNOWN"
	}
}

// Role is a path's user-assigned scheduling role. CUSTOM per-path role assignment over
// CTRL's path_role message is phase 7 scope; phase 2 paths are always RoleBond.
type Role int32

const (
	RoleBond Role = iota
	RoleBackup
	RoleDisabled
)

func (r Role) String() string {
	switch r {
	case RoleBond:
		return "BOND"
	case RoleBackup:
		return "BACKUP"
	case RoleDisabled:
		return "DISABLED"
	default:
		return "UNKNOWN"
	}
}

// Path is the minimal read-only view a Scheduler needs of a path's current condition.
// core/bond's Path type implements this; core/sched deliberately has no dependency on
// core/bond, so the scheduler is independently testable and swappable.
type Path interface {
	ID() uint8
	State() State
	Role() Role
	// InFlight and CWND are byte counts. Phase 2 has no real congestion control yet
	// (that's core/cc, phase 3) so core/bond's CWND() returns an effectively unbounded
	// value — the gating condition is present in the scheduler now so tier 3's real
	// congestion window drops in without changing this interface.
	InFlight() int64
	CWND() int64
}

// Scheduler picks which path a data-plane engine should send the next packet on.
type Scheduler interface {
	Name() string
	// Next returns the path to send on, or nil if none are currently eligible (caller
	// should queue the packet and wait for the next ACK/state change).
	Next(paths []Path) Path
}

// RoundRobin is Tier 1: ARCHITECTURE.md §2.1's baseline. Cycles through paths in a fixed
// rotation, skipping any that aren't ACTIVE+BOND+within-window. Never removed even once
// later tiers exist — it's the mode that wins under small receive buffers, and the
// debugging baseline every other tier is benchmarked against (ARCHITECTURE.md §1.3 law 6,
// and the empirical result in the source spec: round robin never induces HoL blocking
// because it never favors one path over another).
type RoundRobin struct {
	mu     sync.Mutex
	cursor int
}

func NewRoundRobin() *RoundRobin { return &RoundRobin{} }

func (r *RoundRobin) Name() string { return "round-robin" }

func (r *RoundRobin) Next(paths []Path) Path {
	n := len(paths)
	if n == 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := 0; i < n; i++ {
		idx := (r.cursor + i) % n
		p := paths[idx]
		if p.State() == StateActive && p.Role() == RoleBond && p.InFlight() < p.CWND() {
			r.cursor = idx + 1
			if r.cursor >= n {
				r.cursor = 0
			}
			return p
		}
	}
	return nil // all blocked; caller queues and waits for ACK, per ARCHITECTURE.md pseudocode
}
