// Package sched implements BOND/1's scheduler ladder (ARCHITECTURE.md §2.1): Tier 1 round
// robin (phase 2, the baseline every later tier is benchmarked against), and Tiers 2-4
// (phase 3) -- weighted-by-goodput, minRTT+cwnd, and HoL-blocking-aware. Tier 5
// (traffic-class routing) is a later phase.
package sched

import (
	"fmt"
	"sync"
	"time"
)

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
	// InFlight and CWND are byte counts, gated by core/cc's per-path congestion window.
	InFlight() int64
	CWND() int64
	// RTTMin is the path's windowed-minimum RTT (PROTOCOL.md §6's scheduling metric --
	// minimum rather than smoothed, since smoothed RTT includes self-induced queueing
	// delay). Zero means no sample yet.
	RTTMin() time.Duration
	// Goodput is the path's current delivered-bytes-per-second estimate (core/cc's
	// windowed-max delivery rate -- the same signal congestion control uses). Zero means
	// no sample yet.
	Goodput() float64
}

// Scheduler picks which path a data-plane engine should send the next size-byte packet on.
type Scheduler interface {
	Name() string
	// Next returns the path to send on, or nil if none should be used right now -- either
	// because none are eligible, or (Tier 4) because using an eligible-but-harmful path
	// would arrive later than simply waiting. The caller queues the packet and retries.
	Next(paths []Path, size int) Path
}

// New builds a Scheduler by its Name(). Empty string defaults to round-robin.
func New(name string) (Scheduler, error) {
	switch name {
	case "", "round-robin":
		return NewRoundRobin(), nil
	case "weighted-goodput":
		return NewWeightedGoodput(), nil
	case "min-rtt-cwnd":
		return NewMinRTTCwnd(), nil
	case "hol-aware":
		return NewHoLAware(), nil
	default:
		return nil, fmt.Errorf("sched: unknown scheduler %q", name)
	}
}

// eligiblePaths returns paths ready to carry a packet right now: ACTIVE, BOND-role, and
// within their congestion window.
func eligiblePaths(paths []Path) []Path {
	out := make([]Path, 0, len(paths))
	for _, p := range paths {
		if p.State() == StateActive && p.Role() == RoleBond && p.InFlight() < p.CWND() {
			out = append(out, p)
		}
	}
	return out
}

// activeBondPaths returns every ACTIVE BOND-role path regardless of congestion window --
// Tier 4 needs this wider set to reason about a path that's currently full.
func activeBondPaths(paths []Path) []Path {
	out := make([]Path, 0, len(paths))
	for _, p := range paths {
		if p.State() == StateActive && p.Role() == RoleBond {
			out = append(out, p)
		}
	}
	return out
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

func (r *RoundRobin) Next(paths []Path, size int) Path {
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
