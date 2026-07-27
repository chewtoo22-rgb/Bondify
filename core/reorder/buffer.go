// Package reorder implements the BOND/1 reordering buffer described in ARCHITECTURE.md
// §2.2 / PROTOCOL.md §3.2: a min-heap keyed by GSN, a next_expected_gsn cursor, and a
// single deadline timer for the head-of-buffer entry, so multipath reordering never stalls
// delivery indefinitely.
package reorder

import (
	"container/heap"
	"sync"
	"sync/atomic"
	"time"
)

// Packet is one GSN-numbered payload entering the buffer.
type Packet struct {
	GSN     uint64
	Payload []byte
	Push    bool // DATA flag PUSH: release immediately, bypassing ordering (LATENCY class)
}

// DefaultDeadlineMin/Max/Factor and buffer size bounds mirror PROTOCOL.md's Appendix
// default constants (REORDER_DEADLINE_MIN/MAX/FACTOR, REORDER_BUF_MIN/MAX).
const (
	DefaultDeadlineMin = 20 * time.Millisecond
	DefaultDeadlineMax = 400 * time.Millisecond
	DefaultBufMin      = 256 * 1024
	DefaultBufMax      = 16 * 1024 * 1024
)

// Buffer reorders packets for one direction of one session. Deliver receives packets in
// GSN order (or as close as the deadline allows); Push enqueues arrivals from the network.
type Buffer struct {
	mu           sync.Mutex
	h            gsnHeap
	nextExpected uint64
	deadline     time.Duration
	maxBytes     int
	curBytes     int
	headGSN      uint64
	headSet      bool
	timer        *time.Timer

	forcedReleases atomic.Uint64

	out chan Packet
}

// New creates a reordering buffer expecting the session's data stream to start at GSN 0.
// deadline and maxBytes are clamped into the ranges PROTOCOL.md specifies; pass 0 for
// either to get the minimum.
func New(deadline time.Duration, maxBytes int) *Buffer {
	return NewFrom(0, deadline, maxBytes)
}

// NewFrom creates a reordering buffer expecting the first packet at startGSN. Note that
// next_expected_gsn is fixed at construction, not learned from whichever packet happens
// to arrive first — it must reflect the session's actual starting sequence number (0 for
// a fresh session), or a lost first packet would never be noticed and later duplicates
// would never be rejected. Getters/setters for mid-session GSN reassignment aren't needed:
// a single Buffer covers one session's lifetime in one direction.
func NewFrom(startGSN uint64, deadline time.Duration, maxBytes int) *Buffer {
	if deadline < DefaultDeadlineMin {
		deadline = DefaultDeadlineMin
	}
	if deadline > DefaultDeadlineMax {
		deadline = DefaultDeadlineMax
	}
	if maxBytes < DefaultBufMin {
		maxBytes = DefaultBufMin
	}
	if maxBytes > DefaultBufMax {
		maxBytes = DefaultBufMax
	}
	b := &Buffer{
		deadline:     deadline,
		maxBytes:     maxBytes,
		out:          make(chan Packet, 1024),
		nextExpected: startGSN,
	}
	heap.Init(&b.h)
	return b
}

// Out is the in-order (or deadline-forced) delivery channel.
func (b *Buffer) Out() <-chan Packet { return b.out }

// SetDeadline updates the reorder deadline, e.g. after a path joins/leaves and the RTT
// spread changes by more than 20% (PROTOCOL.md §3.2). Clamped to [20ms, 400ms].
func (b *Buffer) SetDeadline(d time.Duration) {
	if d < DefaultDeadlineMin {
		d = DefaultDeadlineMin
	}
	if d > DefaultDeadlineMax {
		d = DefaultDeadlineMax
	}
	b.mu.Lock()
	b.deadline = d
	b.mu.Unlock()
}

// Occupancy reports current buffered byte count, for ACK reporting back to the sender.
func (b *Buffer) Occupancy() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.curBytes
}

// MaxBytes reports the buffer's clamped soft ceiling, for diagnostics/UI gauges.
func (b *Buffer) MaxBytes() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.maxBytes
}

// ForcedReleases counts how many times a gap was declared lost and released early (on
// deadline expiry or overflow), for diagnostics -- distinct from ordinary in-order drains.
func (b *Buffer) ForcedReleases() uint64 { return b.forcedReleases.Load() }

// Push inserts a received packet and delivers everything now releasable. Duplicate or
// already-passed GSNs are dropped silently (this is also where REDUNDANT mode's dedup
// happens for free at a layer above the AEAD replay window, matching PROTOCOL.md §3.2's
// note that no additional dedup logic is needed).
func (b *Packet) size() int { return len(b.Payload) }

func (b *Buffer) Push(pkt Packet) {
	b.mu.Lock()

	if pkt.Push {
		b.mu.Unlock()
		select {
		case b.out <- pkt:
		default:
		}
		return
	}

	if pkt.GSN < b.nextExpected {
		b.mu.Unlock()
		return // duplicate or already-delivered; drop
	}

	heap.Push(&b.h, pkt)
	b.curBytes += pkt.size()

	b.drainLocked()

	if b.curBytes > b.maxBytes {
		b.forceReleaseHeadLocked()
		b.drainLocked()
	}

	b.rearmTimerLocked()
	b.mu.Unlock()
}

// drainLocked releases the head repeatedly while it's exactly next_expected_gsn.
func (b *Buffer) drainLocked() {
	for b.h.Len() > 0 && b.h[0].GSN == b.nextExpected {
		pkt := heap.Pop(&b.h).(Packet)
		b.curBytes -= pkt.size()
		b.nextExpected++
		b.emitLocked(pkt)
	}
}

// forceReleaseHeadLocked declares the gap up to the current head lost, advances past it,
// and releases the head — used both on deadline expiry and buffer overflow.
func (b *Buffer) forceReleaseHeadLocked() {
	if b.h.Len() == 0 {
		return
	}
	pkt := heap.Pop(&b.h).(Packet)
	b.curBytes -= pkt.size()
	b.nextExpected = pkt.GSN + 1
	b.forcedReleases.Add(1)
	b.emitLocked(pkt)
}

func (b *Buffer) emitLocked(pkt Packet) {
	select {
	case b.out <- pkt:
	default:
		// Consumer isn't keeping up; drop rather than block the buffer under its own
		// lock indefinitely. A slow consumer is a bug upstream, not something the
		// reorder buffer should deadlock over.
	}
}

// rearmTimerLocked (re)starts the deadline timer whenever the head-of-buffer entry
// changes, per PROTOCOL.md §3.2: "a single deadline timer for the head-of-buffer entry."
func (b *Buffer) rearmTimerLocked() {
	if b.h.Len() == 0 {
		b.headSet = false
		if b.timer != nil {
			b.timer.Stop()
		}
		return
	}
	head := b.h[0].GSN
	if b.headSet && head == b.headGSN && b.timer != nil {
		return // head unchanged; timer already covers it
	}
	b.headGSN = head
	b.headSet = true
	if b.timer != nil {
		b.timer.Stop()
	}
	d := b.deadline
	b.timer = time.AfterFunc(d, func() { b.onDeadline(head) })
}

func (b *Buffer) onDeadline(expectedHead uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.h.Len() == 0 || b.h[0].GSN != expectedHead {
		return // already resolved by the time the timer fired
	}
	b.forceReleaseHeadLocked()
	b.drainLocked()
	b.rearmTimerLocked()
}

// gsnHeap is a container/heap.Interface min-heap of Packet ordered by GSN.
type gsnHeap []Packet

func (h gsnHeap) Len() int            { return len(h) }
func (h gsnHeap) Less(i, j int) bool  { return h[i].GSN < h[j].GSN }
func (h gsnHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *gsnHeap) Push(x interface{}) { *h = append(*h, x.(Packet)) }
func (h *gsnHeap) Pop() interface{} {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}
