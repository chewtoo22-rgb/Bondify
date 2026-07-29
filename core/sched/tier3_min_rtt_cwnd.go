package sched

import (
	"math"
	"sync"
	"time"
)

// unmeasuredRTT stands in for a path with no RTT sample yet (RTTMin() == 0): treated as the
// worst possible RTT rather than the best, so an unmeasured path doesn't get flooded ahead
// of paths already known to be fast. It naturally gets picked once every eligible path is
// equally unmeasured, or as soon as it reports a real sample.
const unmeasuredRTT = time.Duration(math.MaxInt64)

// tieRTTFactor/tieRTTMin define how close two paths' RTTMin() must be to count as "tied"
// for fastest-path selection. Without a tie window, a strict single-winner comparison lets
// one path permanently absorb 100% of traffic even when an equally capable path sits
// completely idle: core/cc's congestion window is a passive estimate of what a path has
// actually been asked to carry (see core/cc's doc comment on deferred PROBE_BW-style active
// bandwidth probing), so the sole active path's InFlight()<CWND() check keeps reading
// "room" forever -- there is no negative feedback that would ever make the scheduler try
// the idle path, because it's never given anything to prove itself against. This was found
// for real, not by inspection: on a homogeneous two-path CI run, Tier 4 measured roughly
// half of Tier 1's throughput before this fix, because it never gave the second path
// anything to carry at all. Treating near-tied paths as a set and round-robining within it
// gives every equally-good path a genuine chance to be exercised.
const (
	tieRTTFactor = 1.15
	tieRTTMin    = 2 * time.Millisecond
	// RTT/path classification only changes at probe or lifecycle cadence, not per data
	// packet. Rechecking every 256 selections keeps response time well below the 200 ms
	// probe interval at ordinary traffic rates without rescanning paths for every packet.
	schedulerClassifyEvery = 256
)

// fastestTiedSetInto returns the lowest RTTMin() among paths (unmeasuredRTT if paths is
// empty) and appends every path within its tie window to reusable dst. When the fastest
// sample itself is unmeasuredRTT -- no path in the set has a real RTT sample yet -- every
// path is trivially tied and shares load via round robin. Callers keep dst as
// scheduler-owned scratch storage under their own lock.
func fastestTiedSetInto(paths, dst []Path) (time.Duration, []Path) {
	fastest := unmeasuredRTT
	for _, p := range paths {
		rtt := p.RTTMin()
		if rtt <= 0 {
			rtt = unmeasuredRTT
		}
		if rtt < fastest {
			fastest = rtt
		}
	}
	var window time.Duration
	if fastest < unmeasuredRTT {
		window = time.Duration(float64(fastest) * (tieRTTFactor - 1))
		if window < tieRTTMin {
			window = tieRTTMin
		}
	}
	primary := dst[:0]
	for _, p := range paths {
		rtt := p.RTTMin()
		if rtt <= 0 {
			rtt = unmeasuredRTT
		}
		if fastest >= unmeasuredRTT || rtt <= fastest+window {
			primary = append(primary, p)
		}
	}
	return fastest, primary
}

// pathIn reports whether p (by ID) is present in set.
func pathIn(set []Path, p Path) bool {
	for _, q := range set {
		if q.ID() == p.ID() {
			return true
		}
	}
	return false
}

// MinRTTCwnd is Tier 3 (ARCHITECTURE.md §2.1): the classic MPTCP default. Prefers the
// lowest-RTT path(s) with room in their congestion window, round-robining among any that
// are tied for fastest so equally-good paths share load instead of one perpetually
// winning. Better latency on homogeneous paths; worse on genuinely heterogeneous ones,
// since once the fast path's window fills it dumps straight onto whatever's next-fastest
// with room, however much slower that is -- exactly the behavior Tier 4 exists to fix.
type MinRTTCwnd struct {
	mu              sync.Mutex
	cursor          int
	scratchEligible []Path
	scratchPrimary  []Path
	decisions       int
	cachedPathCount int
	cacheReady      bool
}

func NewMinRTTCwnd() *MinRTTCwnd { return &MinRTTCwnd{} }

func (m *MinRTTCwnd) Name() string { return "min-rtt-cwnd" }

func (m *MinRTTCwnd) Next(paths []Path, size int) Path {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.decisions++
	if m.cacheReady &&
		len(m.scratchPrimary) > 0 &&
		m.cachedPathCount == len(paths) &&
		m.decisions < schedulerClassifyEvery {
		n := len(m.scratchPrimary)
		if m.cursor >= n {
			m.cursor = 0
		}
		for i := 0; i < n; i++ {
			idx := (m.cursor + i) % n
			p := m.scratchPrimary[idx]
			if p.State() == StateActive && p.Role() == RoleBond && p.InFlight() < p.CWND() {
				m.cursor = (idx + 1) % n
				return p
			}
		}
	}

	elig := m.scratchEligible[:0]
	for _, p := range paths {
		if p.State() == StateActive && p.Role() == RoleBond && p.InFlight() < p.CWND() {
			elig = append(elig, p)
		}
	}
	m.scratchEligible = elig
	if len(elig) == 0 {
		return nil
	}
	fastestRTT, primary := fastestTiedSetInto(elig, m.scratchPrimary)
	m.scratchPrimary = primary
	m.cachedPathCount = len(paths)
	m.decisions = 0
	m.cacheReady = fastestRTT < unmeasuredRTT
	if len(primary) == 0 {
		return nil
	}

	n := len(primary)
	if m.cursor >= n {
		m.cursor = 0
	}
	p := primary[m.cursor]
	m.cursor = (m.cursor + 1) % n
	return p
}
