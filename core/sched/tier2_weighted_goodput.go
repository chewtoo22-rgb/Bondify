package sched

import "sync"

// WeightedGoodput is Tier 2 (ARCHITECTURE.md §2.1): each path's share of traffic is
// proportional to its EWMA... here, windowed-max (core/cc.Controller.DeliveryRate)
// goodput. A path whose goodput collapses (stalled, congested) is reweighted down on the
// very next call and drains out of rotation on its own -- no separate "is this path bad"
// check needed.
//
// Implementation note, tracked honestly: ARCHITECTURE.md's phrasing ("deficit round
// robin") describes DRR as classically implemented against a separate packet queue per
// flow, where a flow's whole queued backlog is drained (one quantum's worth of packets at
// a time) before moving on. BOND/1's scheduler has no such per-path queue -- Next is called
// once per already-dequeued TUN packet and must return an immediate assignment for that one
// packet -- so there is nothing to "drain." What's implemented instead is Nginx-style
// smooth weighted round robin: every path's running current-weight is incremented by its
// goodput-derived weight each call, the highest current-weight path is picked and debited
// by the sum of all weights. This achieves the same goal (long-run share proportional to
// weight, self-correcting as weight changes) with even interleaving instead of DRR's
// characteristic same-flow bursts, which suits a single shared output stream better.
type WeightedGoodput struct {
	mu            sync.Mutex
	currentWeight map[uint8]float64
}

func NewWeightedGoodput() *WeightedGoodput {
	return &WeightedGoodput{currentWeight: make(map[uint8]float64)}
}

func (w *WeightedGoodput) Name() string { return "weighted-goodput" }

func (w *WeightedGoodput) Next(paths []Path, size int) Path {
	elig := eligiblePaths(paths)
	if len(elig) == 0 {
		return nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	// Normalize against the smallest positive goodput sample this round, so a path with no
	// sample yet (Goodput()==0 -- just joined, or the relay side, which doesn't run
	// core/cc's sampler at all yet) gets a fair, un-starved weight of 1 rather than being
	// zeroed out of rotation entirely.
	minGoodput := 0.0
	for _, p := range elig {
		if g := p.Goodput(); g > 0 && (minGoodput == 0 || g < minGoodput) {
			minGoodput = g
		}
	}
	weightOf := func(p Path) float64 {
		g := p.Goodput()
		if minGoodput <= 0 || g <= 0 {
			return 1
		}
		return g / minGoodput
	}

	var totalWeight float64
	var best Path
	var bestCW float64
	for _, p := range elig {
		wt := weightOf(p)
		cw := w.currentWeight[p.ID()] + wt
		w.currentWeight[p.ID()] = cw
		totalWeight += wt
		if best == nil || cw > bestCW {
			best = p
			bestCW = cw
		}
	}
	w.currentWeight[best.ID()] -= totalWeight
	return best
}
