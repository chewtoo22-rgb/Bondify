// Package cc implements ARCHITECTURE.md §2.4's per-path congestion control: a simplified
// BBR-style controller, not loss-based -- cellular loss is often non-congestive, and a
// Reno/CUBIC-style window collapse on loss would systematically underuse the LTE/5G paths
// this product exists to exploit.
//
// Simplification, tracked honestly: real BBR cycles pacing_gain through
// STARTUP/DRAIN/PROBE_BW/PROBE_RTT phases, driven by a per-RTT (often per-ACK) delivery
// rate sample. BOND/1 now has per-packet ACK/SACK feedback, but Controller is still fed by
// periodic PROBE/PROBE_ACK delivery samples; the ACK path does not yet attribute delivery
// timing back to each original path. There is therefore no trustworthy per-RTT signal to
// cycle a gain schedule against. Controller instead
// uses a single fixed gain over a windowed max-filtered delivery-rate estimate, which
// captures the core formula (cwnd = btl_bw * rt_prop * gain) and the core self-correcting
// property (a stalled path's rate ages out of the window and its cwnd shrinks) without the
// full phase state machine. Revisit once per-packet ACKs exist (phase 4 retransmission
// work is the natural point to add them).
package cc

import (
	"sync"
	"time"
)

const (
	// Gain is BBR's classic bandwidth-delay-product multiplier: enough headroom above the
	// measured bottleneck*RTT product to keep the pipe full without runaway buffering.
	Gain = 2.0

	// btlBwWindow is how many delivery-rate samples the max-filter keeps. At one sample per
	// probe interval (200ms, see core/bond's ProbeInterval) this is a ~2s window -- long
	// enough to smooth over one lost probe, short enough that a genuinely stalled path
	// drains out within a couple seconds.
	btlBwWindow = 10

	// minCWND floors the window so a path with a bad or empty sample history (e.g. one that
	// just joined, or the relay side, which does not run this controller at all yet -- see
	// path.go's doc comment on the client/relay probing asymmetry) is never reduced to
	// zero, which would starve it out of every scheduler tier permanently. 8 packets at a
	// generous 1500-byte MTU.
	minCWND = 8 * 1500

	// maxCWND is a sane absolute ceiling, well above anything a single UDP path on
	// commodity hardware will legitimately need, to bound memory/in-flight exposure if a
	// delivery-rate sample is ever corrupted by a clock or counter glitch.
	maxCWND = 64 * 1024 * 1024

	// initialCWND is the starting window before any sample has arrived: generous enough
	// that a freshly joined path (or any relay-side path, which never samples) isn't
	// artificially bottlenecked relative to established paths.
	initialCWND = 4 * 1024 * 1024
)

// Controller is one path's congestion state. Safe for concurrent use.
type Controller struct {
	mu      sync.Mutex
	samples [btlBwWindow]float64 // bytes/sec, ring buffer
	filled  int
	next    int
	cwnd    int64
}

// NewController returns a controller seeded at initialCWND, before any real sample exists.
func NewController() *Controller {
	return &Controller{cwnd: initialCWND}
}

// Sample feeds one delivery-rate observation: bytesDelivered arrived at the receiver over
// elapsed wall-clock time, alongside rtProp (the path's current windowed-minimum RTT, e.g.
// bond.Path.RTTMin() -- "rt_prop" in BBR terminology). It recomputes cwnd = btl_bw * rt_prop
// * Gain from the resulting windowed-max delivery rate. Call sites with no meaningful
// interval (elapsed <= 0) or no RTT estimate yet (rtProp <= 0) should skip the call rather
// than feed a garbage sample.
func (c *Controller) Sample(bytesDelivered int64, elapsed time.Duration, rtProp time.Duration) {
	if elapsed <= 0 || rtProp <= 0 {
		return
	}
	bps := float64(bytesDelivered) / elapsed.Seconds()

	c.mu.Lock()
	defer c.mu.Unlock()

	c.samples[c.next] = bps
	c.next = (c.next + 1) % btlBwWindow
	if c.filled < btlBwWindow {
		c.filled++
	}

	var btlBw float64
	for i := 0; i < c.filled; i++ {
		if c.samples[i] > btlBw {
			btlBw = c.samples[i]
		}
	}

	cwnd := int64(btlBw * rtProp.Seconds() * Gain)
	if cwnd < minCWND {
		cwnd = minCWND
	}
	if cwnd > maxCWND {
		cwnd = maxCWND
	}
	c.cwnd = cwnd
}

// CWND returns the current congestion window in bytes.
func (c *Controller) CWND() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cwnd
}

// DeliveryRate returns the current windowed-max delivery-rate estimate in bytes/sec -- the
// same "goodput" signal Tier 2's weighted scheduler uses, so both consumers agree on what a
// path is actually delivering rather than maintaining two separate measurements of it.
func (c *Controller) DeliveryRate() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	var btlBw float64
	for i := 0; i < c.filled; i++ {
		if c.samples[i] > btlBw {
			btlBw = c.samples[i]
		}
	}
	return btlBw
}
