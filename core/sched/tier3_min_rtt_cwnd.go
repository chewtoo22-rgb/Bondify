package sched

import (
	"math"
	"time"
)

// unmeasuredRTT stands in for a path with no RTT sample yet (RTTMin() == 0): treated as the
// worst possible RTT rather than the best, so an unmeasured path doesn't get flooded ahead
// of paths already known to be fast. It naturally gets picked once every eligible path is
// equally unmeasured, or as soon as it reports a real sample.
const unmeasuredRTT = time.Duration(math.MaxInt64)

// fastestByRTT returns the path with the lowest RTTMin() among paths, or nil if paths is
// empty. Shared by Tier 3 and Tier 4, which both need "which path is fastest right now."
func fastestByRTT(paths []Path) Path {
	var best Path
	bestRTT := unmeasuredRTT
	for _, p := range paths {
		rtt := p.RTTMin()
		if rtt <= 0 {
			rtt = unmeasuredRTT
		}
		if best == nil || rtt < bestRTT {
			best = p
			bestRTT = rtt
		}
	}
	return best
}

// MinRTTCwnd is Tier 3 (ARCHITECTURE.md §2.1): the classic MPTCP default. Always prefers
// the lowest-RTT path with room in its congestion window. Better latency on homogeneous
// paths; worse on heterogeneous ones, since once the fast path's window fills it dumps
// straight onto whatever's next-fastest with room, however much slower that is -- exactly
// the behavior Tier 4 exists to fix.
type MinRTTCwnd struct{}

func NewMinRTTCwnd() *MinRTTCwnd { return &MinRTTCwnd{} }

func (m *MinRTTCwnd) Name() string { return "min-rtt-cwnd" }

func (m *MinRTTCwnd) Next(paths []Path, size int) Path {
	elig := eligiblePaths(paths)
	if len(elig) == 0 {
		return nil
	}
	return fastestByRTT(elig)
}
