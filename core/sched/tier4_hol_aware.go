package sched

import (
	"sync"
	"time"
)

const (
	// holLambdaInit/Min/Max bound the adaptive strictness factor: how much slower a
	// backup path is allowed to be, relative to just waiting for the fastest path to free
	// up, before HoLAware refuses to use it.
	holLambdaInit = 1.0
	holLambdaMin  = 0.5
	holLambdaMax  = 4.0

	// holLambdaRaise multiplies lambda up (more reluctant to use a slow path) each time a
	// decision actually skips one -- an observed HoL-blocking-shaped moment.
	holLambdaRaise = 1.15
	// holLambdaDecay multiplies lambda back down (more willing to use a slow path) on
	// every decision that didn't need to skip one, so strictness relaxes once conditions
	// improve instead of staying pinned at its worst historical value forever.
	holLambdaDecay = 0.98
)

// HoLAware is Tier 4 (ARCHITECTURE.md §2.1): a BLEST/ECF-style hybrid. Starts from Tier 3's
// "prefer the fastest path(s)" rule -- round-robining among whatever is tied for fastest,
// per fastestTiedSet's doc comment, so multiple equally-good paths actually share load. Only
// when NOTHING tied-for-fastest has room does it consider a genuinely slower alternate: it
// estimates whether sending on that slower path would actually *arrive* later than simply
// waiting for the fast path to free a slot, and skips the slow path when so. Deliberately
// returning nil here (idle capacity on a path that has room) is correct, not a bug: it's
// choosing to wait rather than let one hopelessly slow path stall reassembly of everything
// behind it.
type HoLAware struct {
	mu     sync.Mutex
	lambda float64
	cursor int
}

func NewHoLAware() *HoLAware { return &HoLAware{lambda: holLambdaInit} }

func (h *HoLAware) Name() string { return "hol-aware" }

func (h *HoLAware) Next(paths []Path, size int) Path {
	bond := activeBondPaths(paths)
	if len(bond) == 0 {
		return nil
	}

	fastestRTT, primary := fastestTiedSet(bond)

	h.mu.Lock()
	defer h.mu.Unlock()

	// Round-robin among paths tied for fastest -- no HoL tradeoff to make as long as one
	// of them has room.
	n := len(primary)
	if n > 0 {
		if h.cursor >= n {
			h.cursor = 0
		}
		for i := 0; i < n; i++ {
			idx := (h.cursor + i) % n
			p := primary[idx]
			if p.InFlight() < p.CWND() {
				h.cursor = idx + 1
				if h.cursor >= n {
					h.cursor = 0
				}
				h.decay()
				return p
			}
		}
	}

	// Nothing tied-for-fastest has room. Find the fastest genuinely-slower alternative
	// that does. foundSlow is tracked separately from slowRTT's sentinel value: a lone
	// candidate whose RTT is itself unmeasuredRTT must still be selectable (rtt < slowRTT
	// is never true when both sides equal the sentinel, which silently dropped every
	// candidate here before this fix).
	var slow Path
	slowRTT := unmeasuredRTT
	foundSlow := false
	for _, p := range bond {
		if pathIn(primary, p) {
			continue
		}
		if p.InFlight() >= p.CWND() {
			continue
		}
		rtt := p.RTTMin()
		if rtt <= 0 {
			rtt = unmeasuredRTT
		}
		if !foundSlow || rtt < slowRTT {
			slow = p
			slowRTT = rtt
			foundSlow = true
		}
	}
	if !foundSlow {
		return nil // nothing has room; caller queues and waits
	}

	if fastestRTT >= unmeasuredRTT {
		// The tied-for-fastest set is full but has no RTT estimate yet (e.g. it only just
		// finished JOINING) -- nothing to project against, so use the room that exists
		// rather than guess.
		h.decay()
		return slow
	}

	// Should_use_slow_path (ARCHITECTURE.md §2.1): compare projected delivery time on the
	// slow path against the cost of waiting for the fast set to free a slot. Waiting for
	// the fast set costs roughly one more of its own RTTs (ack-clocked window growth);
	// sending on the slow path now costs roughly its own RTT, scaled by lambda -- the
	// adaptive strictness that rises when a skip was actually needed and decays otherwise.
	projectedWaitForFast := fastestRTT
	projectedSlow := time.Duration(h.lambda * float64(slowRTT))

	if projectedSlow <= projectedWaitForFast {
		h.decay()
		return slow
	}
	h.raise()
	return nil
}

// decay/raise assume h.mu is already held.
func (h *HoLAware) decay() {
	h.lambda *= holLambdaDecay
	if h.lambda < holLambdaMin {
		h.lambda = holLambdaMin
	}
}

func (h *HoLAware) raise() {
	h.lambda *= holLambdaRaise
	if h.lambda > holLambdaMax {
		h.lambda = holLambdaMax
	}
}
