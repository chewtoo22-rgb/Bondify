package bond

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/chewtoo22-rgb/bondify/core/sched"
)

// Mode selects one direction's sending behavior (PROTOCOL.md §9's `mode_set` CTRL message:
// SPEED / REDUNDANT / STREAM / CUSTOM). Only SPEED (the default: scheduler-picked single
// path per packet, optionally FEC-protected) and REDUNDANT (duplicate every packet onto
// DupFactor distinct healthy paths) are implemented; STREAM/CUSTOM are later-phase scope.
type Mode int

const (
	ModeSpeed Mode = iota
	ModeRedundant
)

func (m Mode) String() string {
	switch m {
	case ModeSpeed:
		return "SPEED"
	case ModeRedundant:
		return "REDUNDANT"
	default:
		return "UNKNOWN"
	}
}

// ModeFromString parses a -mode flag value. Empty defaults to SPEED.
func ModeFromString(s string) (Mode, error) {
	switch s {
	case "", "speed":
		return ModeSpeed, nil
	case "redundant":
		return ModeRedundant, nil
	default:
		return 0, fmt.Errorf("bond: unknown mode %q", s)
	}
}

// DupFactor is PROTOCOL.md's DUP_FACTOR: how many distinct paths carry each packet in
// REDUNDANT mode.
const DupFactor = 2

// selectRedundantPaths returns up to n ACTIVE+BOND paths with room, ordered by ascending
// RTT (redundancy is most valuable spent on healthy paths -- duplicating onto a path
// that's already struggling buys little). Fewer than n eligible paths is not an error:
// send on however many exist. A copy of the underlying slice is sorted in place, so
// callers must pass a slice they own (e.g. from ClientTunnel.Paths(), already a copy).
func selectRedundantPaths(paths []*Path, n int) []*Path {
	elig := make([]*Path, 0, len(paths))
	for _, p := range paths {
		if p.State() == sched.StateActive && p.Role() == sched.RoleBond && p.InFlight() < p.CWND() {
			elig = append(elig, p)
		}
	}
	sort.Slice(elig, func(i, j int) bool {
		ri, rj := elig[i].RTTMin(), elig[j].RTTMin()
		if ri <= 0 {
			ri = time.Duration(math.MaxInt64)
		}
		if rj <= 0 {
			rj = time.Duration(math.MaxInt64)
		}
		return ri < rj
	})
	if len(elig) > n {
		elig = elig[:n]
	}
	return elig
}

// healthiestPath returns the ACTIVE+BOND path with the lowest Loss() (ties broken by
// lowest RTT), or nil if none are eligible. Used to place FEC parity on "the least loaded
// path, never the lossy one" (ARCHITECTURE.md §2.3).
func healthiestPath(paths []*Path) *Path {
	var best *Path
	var bestLoss float64
	var bestRTT time.Duration
	for _, p := range paths {
		if p.State() != sched.StateActive || p.Role() != sched.RoleBond {
			continue
		}
		loss := p.Loss()
		rtt := p.RTTMin()
		if rtt <= 0 {
			rtt = time.Duration(math.MaxInt64)
		}
		if best == nil || loss < bestLoss || (loss == bestLoss && rtt < bestRTT) {
			best = p
			bestLoss = loss
			bestRTT = rtt
		}
	}
	return best
}
