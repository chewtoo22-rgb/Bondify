package bond

import (
	"math"
	"sort"
	"sync"
	"time"

	"github.com/chewtoo22-rgb/bondify/core/proto"
	"github.com/chewtoo22-rgb/bondify/core/reorder"
	"github.com/chewtoo22-rgb/bondify/core/sched"
)

// ACK/retransmission bounds. These are deliberately small and fixed: a peer must never
// be able to turn loss into unbounded memory growth or an unlimited resend storm.
const (
	AckEveryPackets         = 8
	AckMaxDelay             = 20 * time.Millisecond
	AckMaxSACKRanges        = 32
	AckReceiveWindow        = 8192
	RetransmitMaxPackets    = 4096
	RetransmitMaxBytes      = 8 * 1024 * 1024
	RetransmitMaxRetries    = 3
	RetransmitMaxBatch      = 64
	RetransmitTick          = 5 * time.Millisecond
	RetransmitSACKThreshold = 3
	RetransmitFastDelay     = 10 * time.Millisecond
	RetransmitDefaultRTO    = 200 * time.Millisecond
	RetransmitMinRTO        = 100 * time.Millisecond
	RetransmitMaxRTO        = time.Second
	retransmitSemanticFlags = proto.FlagLATENCY |
		// DUP and FECProtected describe the original physical transmission, not the
		// logical packet. RTX gets a fresh PSN/nonce and is intentionally outside the
		// old FEC generation. Preserve only application-class/reorder semantics.
		proto.FlagBULK |
		proto.FlagPUSH
)

// AckRange is an inclusive SACK range above CumulativeGSN.
type AckRange struct {
	Start uint64 `cbor:"s"`
	End   uint64 `cbor:"e"`
}

// AckPathCounter reports the receiver's DATA/FEC packet counter for one known path.
type AckPathCounter struct {
	PathID   uint8  `cbor:"pid"`
	Received uint32 `cbor:"recv"`
}

// AckPayload is the authenticated CBOR payload carried by proto.TypeAck.
//
// HasCumulative solves the GSN-0 boundary cleanly: false means no contiguous GSN has been
// received yet, while SACK may still report packets above the initial gap.
type AckPayload struct {
	HasCumulative   bool             `cbor:"has"`
	CumulativeGSN   uint64           `cbor:"cum"`
	SACK            []AckRange       `cbor:"sack,omitempty"`
	PathCounters    []AckPathCounter `cbor:"paths,omitempty"`
	ReorderBytes    uint32           `cbor:"rbuf"`
	ReorderDeadline uint16           `cbor:"rms"`
}

type ackSnapshot struct {
	version uint64
	payload AckPayload
}

// ackState tracks unique received GSNs independently from reorder.Buffer. That separation
// matters: reorder deadlines may deliberately release past a missing GSN, but an ACK must
// never claim that the missing packet was actually received.
type ackState struct {
	mu sync.Mutex

	next     uint64
	received map[uint64]struct{}

	dirty        bool
	urgent       bool
	pending      int
	firstPending time.Time
	version      uint64
}

func newACKState() *ackState {
	return &ackState{received: make(map[uint64]struct{})}
}

// Observe records a received GSN. A gap makes the next ACK urgent so SACK-driven recovery
// can beat the reorder deadline. Duplicates still make the ACK dirty: the duplicate often
// means the sender retransmitted because the previous ACK was lost.
func (a *ackState) Observe(gsn uint64, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.dirty {
		a.firstPending = now
	}
	a.dirty = true
	a.pending++
	a.version++

	if gsn < a.next {
		return
	}
	if gsn == a.next {
		a.next++
		for {
			if _, ok := a.received[a.next]; !ok {
				break
			}
			delete(a.received, a.next)
			a.next++
		}
		return
	}

	a.urgent = true
	if gsn-a.next >= AckReceiveWindow {
		return
	}
	if len(a.received) >= AckReceiveWindow {
		return
	}
	a.received[gsn] = struct{}{}
}

func (a *ackState) SnapshotIfDue(now time.Time) (ackSnapshot, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.dirty {
		return ackSnapshot{}, false
	}
	if !a.urgent && a.pending < AckEveryPackets && now.Sub(a.firstPending) < AckMaxDelay {
		return ackSnapshot{}, false
	}

	keys := make([]uint64, 0, len(a.received))
	for gsn := range a.received {
		keys = append(keys, gsn)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })

	ranges := make([]AckRange, 0, minInt(len(keys), AckMaxSACKRanges))
	for _, gsn := range keys {
		newRange := len(ranges) == 0
		if !newRange {
			end := ranges[len(ranges)-1].End
			newRange = gsn > end && gsn-end > 1
		}
		if newRange {
			if len(ranges) == AckMaxSACKRanges {
				break
			}
			ranges = append(ranges, AckRange{Start: gsn, End: gsn})
			continue
		}
		ranges[len(ranges)-1].End = gsn
	}

	s := ackSnapshot{version: a.version}
	if a.next > 0 {
		s.payload.HasCumulative = true
		s.payload.CumulativeGSN = a.next - 1
	}
	s.payload.SACK = ranges
	return s, true
}

// MarkSent clears the delayed-ACK state only if no packet arrived while this snapshot was
// being encrypted/written. If the version moved, the newer receive event remains pending.
func (a *ackState) MarkSent(version uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.version != version {
		return
	}
	a.dirty = false
	a.urgent = false
	a.pending = 0
	a.firstPending = time.Time{}
}

type pendingPacket struct {
	GSN      uint64
	Payload  []byte
	Flags    uint8
	SentAt   time.Time
	LastSent time.Time
	Retries  int
	fast     bool
	sackHits int
}

// retransmitQueue retains a bounded copy of unacknowledged logical packets.
type retransmitQueue struct {
	mu sync.Mutex

	packets map[uint64]*pendingPacket
	order   []uint64
	bytes   int
}

func newRetransmitQueue() *retransmitQueue {
	return &retransmitQueue{packets: make(map[uint64]*pendingPacket)}
}

func (q *retransmitQueue) Track(gsn uint64, payload []byte, flags uint8, now time.Time) {
	cp := append([]byte(nil), payload...)
	q.mu.Lock()
	defer q.mu.Unlock()

	if _, exists := q.packets[gsn]; exists {
		return
	}
	q.packets[gsn] = &pendingPacket{
		GSN:      gsn,
		Payload:  cp,
		Flags:    flags & retransmitSemanticFlags,
		SentAt:   now,
		LastSent: now,
	}
	q.order = append(q.order, gsn)
	q.bytes += len(cp)
	q.enforceBoundsLocked()
}

func (q *retransmitQueue) enforceBoundsLocked() {
	for len(q.packets) > RetransmitMaxPackets || q.bytes > RetransmitMaxBytes {
		if len(q.order) == 0 {
			return
		}
		gsn := q.order[0]
		q.order = q.order[1:]
		q.deleteLocked(gsn)
	}
}

func (q *retransmitQueue) Acknowledge(ack AckPayload) {
	q.mu.Lock()
	defer q.mu.Unlock()

	var highest uint64
	hasHighest := false
	if ack.HasCumulative {
		highest = ack.CumulativeGSN
		hasHighest = true
	}

	ranges := validACKRanges(ack.SACK)
	for gsn := range q.packets {
		acked := ack.HasCumulative && gsn <= ack.CumulativeGSN
		if !acked {
			acked = inACKRanges(gsn, ranges)
		}
		if acked {
			q.deleteLocked(gsn)
		}
	}
	for _, r := range ranges {
		if !hasHighest || r.End > highest {
			highest = r.End
			hasHighest = true
		}
	}
	if !hasHighest {
		return
	}

	// A packet below a selectively acknowledged successor is a real hole. Mark it for
	// fast retransmit, but let RetransmitFastDelay give an in-flight FEC shard a brief
	// chance to repair it first.
	for gsn, pkt := range q.packets {
		if gsn < highest {
			pkt.sackHits++
			if pkt.sackHits >= RetransmitSACKThreshold {
				pkt.fast = true
			}
		}
	}
}

func validACKRanges(in []AckRange) []AckRange {
	if len(in) > AckMaxSACKRanges {
		in = in[:AckMaxSACKRanges]
	}
	out := make([]AckRange, 0, len(in))
	for _, r := range in {
		if r.Start > r.End {
			continue
		}
		out = append(out, r)
	}
	return out
}

func inACKRanges(gsn uint64, ranges []AckRange) bool {
	for _, r := range ranges {
		if gsn >= r.Start && gsn <= r.End {
			return true
		}
	}
	return false
}

// Due returns at most RetransmitMaxBatch packets and atomically consumes one retry for
// each. Exhausted entries are dropped rather than retried forever.
func (q *retransmitQueue) Due(now time.Time, rto time.Duration) []pendingPacket {
	q.mu.Lock()
	defer q.mu.Unlock()

	out := make([]pendingPacket, 0, RetransmitMaxBatch)
	for _, gsn := range q.order {
		pkt := q.packets[gsn]
		if pkt == nil {
			continue
		}
		delay := rto
		if pkt.fast {
			delay = RetransmitFastDelay
		}
		if now.Sub(pkt.LastSent) < delay {
			continue
		}
		if pkt.Retries >= RetransmitMaxRetries {
			q.deleteLocked(gsn)
			continue
		}
		pkt.Retries++
		pkt.LastSent = now
		pkt.fast = false
		pkt.sackHits = 0
		out = append(out, pendingPacket{
			GSN:      pkt.GSN,
			Payload:  append([]byte(nil), pkt.Payload...),
			Flags:    pkt.Flags,
			SentAt:   pkt.SentAt,
			LastSent: pkt.LastSent,
			Retries:  pkt.Retries,
		})
		if len(out) == RetransmitMaxBatch {
			break
		}
	}
	q.compactOrderLocked()
	return out
}

func (q *retransmitQueue) deleteLocked(gsn uint64) {
	pkt := q.packets[gsn]
	if pkt == nil {
		return
	}
	q.bytes -= len(pkt.Payload)
	delete(q.packets, gsn)
}

func (q *retransmitQueue) compactOrderLocked() {
	if len(q.order) <= len(q.packets)*2+64 {
		return
	}
	out := q.order[:0]
	for _, gsn := range q.order {
		if _, ok := q.packets[gsn]; ok {
			out = append(out, gsn)
		}
	}
	q.order = out
}

func retransmitRTO(paths []*Path) time.Duration {
	p := lowestRTTActivePath(paths)
	if p == nil || p.RTTMin() <= 0 {
		return RetransmitDefaultRTO
	}
	rto := 2 * p.RTTMin()
	if rto < RetransmitMinRTO {
		return RetransmitMinRTO
	}
	if rto > RetransmitMaxRTO {
		return RetransmitMaxRTO
	}
	return rto
}

// lowestRTTActivePath is used for ACKs and retransmissions. Unlike normal data scheduling,
// ACKs may use a BACKUP path and are not blocked by a data congestion window.
func lowestRTTActivePath(paths []*Path) *Path {
	var best *Path
	bestRTT := time.Duration(math.MaxInt64)
	for _, p := range paths {
		if p.State() != sched.StateActive || p.Role() == sched.RoleDisabled {
			continue
		}
		rtt := p.RTTMin()
		if rtt <= 0 {
			rtt = time.Duration(math.MaxInt64)
		}
		if best == nil || rtt < bestRTT {
			best = p
			bestRTT = rtt
		}
	}
	return best
}

func ackPathCounters(paths []*Path) []AckPathCounter {
	out := make([]AckPathCounter, 0, len(paths))
	for _, p := range paths {
		out = append(out, AckPathCounter{PathID: p.id, Received: p.recvPSN.Load()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PathID < out[j].PathID })
	return out
}

func fillACKReceiverState(ack *AckPayload, paths []*Path, b *reorder.Buffer) {
	ack.PathCounters = ackPathCounters(paths)
	if n := b.Occupancy(); uint64(n) > uint64(math.MaxUint32) {
		ack.ReorderBytes = math.MaxUint32
	} else {
		ack.ReorderBytes = uint32(n)
	}
	ms := b.Deadline() / time.Millisecond
	if ms > math.MaxUint16 {
		ms = math.MaxUint16
	}
	ack.ReorderDeadline = uint16(ms)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
