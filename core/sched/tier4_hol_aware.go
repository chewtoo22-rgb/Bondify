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
// "prefer the fastest path" rule, but when the fastest path is at capacity, it doesn't
// blindly dump onto whatever slower path has room (Tier 3's flaw) -- it estimates whether
// sending on that slower path would actually *arrive* later than simply waiting for the
// fast path to free a slot, and skips the slow path when so. Deliberately returning nil
// here (idle capacity on a path that has room) is correct, not a bug: it's choosing to wait
// rather than let one hopelessly slow path stall reassembly of everything behind it.
type HoLAware struct {
	mu     sync.Mutex
	lambda float64
}

func NewHoLAware() *HoLAware { return &HoLAware{lambda: holLambdaInit} }

func (h *HoLAware) Name() string { return "hol-aware" }

func (h *HoLAware) Next(paths []Path, size int) Path {
	bond := activeBondPaths(paths)
	if len(bond) == 0 {
		return nil
	}

	fastest := fastestByRTT(bond)
	if fastest.InFlight() < fastest.CWND() {
		// The fastest path has room -- no HoL tradeoff to make.
		h.decay()
		return fastest
	}

	// Fastest is full. Find the fastest alternative that still has room.
	var slow Path
	slowRTT := unmeasuredRTT
	for _, p := range bond {
		if p.ID() == fastest.ID() {
			continue
		}
		if p.InFlight() >= p.CWND() {
			continue
		}
		rtt := p.RTTMin()
		if rtt <= 0 {
			rtt = unmeasuredRTT
		}
		if rtt < slowRTT {
			slow = p
			slowRTT = rtt
		}
	}
	if slow == nil {
		return nil // nothing has room; caller queues and waits
	}

	fastRTT := fastest.RTTMin()
	if fastRTT <= 0 {
		// The fastest path is full but has no RTT estimate yet (e.g. it only just
		// finished JOINING) -- nothing to project against, so use the room that exists
		// rather than guess.
		h.decay()
		return slow
	}

	// Should_use_slow_path (ARCHITECTURE.md §2.1): compare projected delivery time on the
	// slow path against the cost of waiting for the fast path to free a slot. Waiting for
	// the fast path costs roughly one more of its own RTTs (ack-clocked window growth);
	// sending on the slow path now costs roughly its own RTT, scaled by lambda -- the
	// adaptive strictness that rises when a skip was actually needed and decays otherwise.
	h.mu.Lock()
	lambda := h.lambda
	h.mu.Unlock()

	projectedWaitForFast := fastRTT
	projectedSlow := time.Duration(lambda * float64(slowRTT))

	if projectedSlow <= projectedWaitForFast {
		h.decay()
		return slow
	}
	h.raise()
	return nil
}

func (h *HoLAware) decay() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lambda *= holLambdaDecay
	if h.lambda < holLambdaMin {
		h.lambda = holLambdaMin
	}
}

func (h *HoLAware) raise() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lambda *= holLambdaRaise
	if h.lambda > holLambdaMax {
		h.lambda = holLambdaMax
	}
}
