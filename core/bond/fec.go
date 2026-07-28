package bond

import (
	"sync"
	"time"

	"github.com/chewtoo22-rgb/bondify/core/fec"
	"github.com/chewtoo22-rgb/bondify/core/proto"
)

// fecSender batches one direction's outgoing DATA packets into generations and, once each
// generation closes (fec.K reached, or Flush is called after FECGenTimeout has elapsed),
// computes and emits Reed-Solomon parity for it (ARCHITECTURE.md §2.3). It protects the
// full inner plaintext -- marshaled InnerDataHeader plus IP payload, i.e. exactly what
// sealPacket would encrypt -- not just the IP payload, so a reconstructed shard on the
// receiving end carries its own GSN and can go straight into the reorder buffer with no
// separate lookup. One instance covers one direction's stream (the client's outgoing
// client->relay traffic, or the relay's outgoing relay->client traffic for one session).
type fecSender struct {
	mu       sync.Mutex
	genID    uint16
	shards   [][]byte
	maxLen   int
	openedAt time.Time

	lossEstimate func() float64
	sendParity   func(genID uint16, genIndex, n, m, w int, shard []byte)
}

// FECGenTimeout mirrors PROTOCOL.md's FEC_GEN_TIMEOUT: a generation that hasn't reached
// fec.K packets closes anyway after this long, so a quiet tunnel doesn't leave a partial
// generation permanently unprotected.
const FECGenTimeout = 30 * time.Millisecond

// newFECSender creates a sender that estimates packet loss and emits generated parity shards through the provided callback.
func newFECSender(lossEstimate func() float64, sendParity func(genID uint16, genIndex, n, m, w int, shard []byte)) *fecSender {
	return &fecSender{lossEstimate: lossEstimate, sendParity: sendParity}
}

// NextSlot reserves the next (generationID, genIndex) pair for an outgoing DATA packet.
// Call this before building that packet's InnerDataHeader, which must stamp both values so
// the receiver can place it (and, if needed, use it in reconstruction) correctly.
func (f *fecSender) NextSlot() (genID uint16, genIndex int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.shards) == 0 {
		f.openedAt = time.Now()
	}
	return f.genID, len(f.shards)
}

// Record stores genIndex's full inner plaintext (marshaled InnerDataHeader + IP payload)
// for FEC purposes, once the caller has finalized and marshaled it. genID/genIndex must be
// exactly what NextSlot returned for this packet. Closes and emits parity for the
// generation automatically once it reaches fec.K shards.
func (f *fecSender) Record(genID uint16, genIndex int, innerPlaintext []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if genID != f.genID || genIndex != len(f.shards) {
		return // stale slot (a concurrent Flush already closed this generation); drop
	}
	cp := append([]byte(nil), innerPlaintext...)
	f.shards = append(f.shards, cp)
	if len(cp) > f.maxLen {
		f.maxLen = len(cp)
	}
	if len(f.shards) >= fec.K {
		f.closeLocked()
	}
}

// Flush closes the current generation if it's non-empty and at least age old. Call this
// periodically (e.g. every few ms) from a ticker goroutine so a generation that never
// reaches fec.K packets still closes within FECGenTimeout instead of sitting open forever
// on a quiet tunnel.
func (f *fecSender) Flush(age time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.shards) == 0 || time.Since(f.openedAt) < age {
		return
	}
	f.closeLocked()
}

// closeLocked computes and emits parity for the current generation (if any redundancy is
// warranted) and starts a new, empty one. Caller must hold f.mu.
func (f *fecSender) closeLocked() {
	n := len(f.shards)
	if n == 0 {
		return
	}
	m := fec.RedundancyFor(f.lossEstimate(), n)
	if m > 0 {
		w := fec.ShardWidth(f.maxLen)
		parity, err := fec.EncodeParity(f.shards, w, m)
		// Encoding errors are only reachable from an internally inconsistent n/m/w
		// combination -- never from real traffic -- so treat failure as "skip parity for
		// this generation" rather than propagate: FEC is a loss-recovery nicety, not a
		// correctness requirement, and every DATA packet in this generation has already
		// been (or will be) sent on its own regardless.
		if err == nil {
			for i, shard := range parity {
				f.sendParity(f.genID, n+i, n, m, w, shard)
			}
		}
	}
	f.genID++
	f.shards = nil
	f.maxLen = 0
}

// fecGeneration is one generation's receive-side bookkeeping: whatever data and parity
// shards have arrived so far, keyed by their genIndex.
type fecGeneration struct {
	n, m, w   int // 0 until the first FEC packet for this generation arrives
	data      map[int][]byte
	parity    map[int][]byte
	firstSeen time.Time
}

// fecGenBuffer tracks partially-received generations for one direction's incoming DATA
// stream and reconstructs missing data shards once enough of a generation has arrived.
// One instance covers one direction (a client's incoming relay->client stream, or the
// relay's incoming client->relay stream for one session).
type fecGenBuffer struct {
	mu   sync.Mutex
	gens map[uint16]*fecGeneration
}

// newFECGenBuffer creates an empty FEC generation buffer.
func newFECGenBuffer() *fecGenBuffer {
	return &fecGenBuffer{gens: make(map[uint16]*fecGeneration)}
}

func (b *fecGenBuffer) getOrCreateLocked(genID uint16) *fecGeneration {
	g, ok := b.gens[genID]
	if !ok {
		g = &fecGeneration{
			data:      make(map[int][]byte),
			parity:    make(map[int][]byte),
			firstSeen: time.Now(),
		}
		b.gens[genID] = g
	}
	return g
}

// HandleData records one FEC-protected DATA packet's full inner plaintext for possible
// future reconstruction of a sibling shard in the same generation. Cheap and a no-op on
// memory pressure over time thanks to GC; call for every DATA packet carrying
// proto.FlagFECProtected (skip entirely for non-FEC traffic -- no bookkeeping cost).
func (b *fecGenBuffer) HandleData(genID uint16, genIndex int, innerPlaintext []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	g := b.getOrCreateLocked(genID)
	g.data[genIndex] = append([]byte(nil), innerPlaintext...)
}

// HandleFEC records one parity shard and, once the generation has n or more total shards
// present (data+parity) and is missing at least one data shard, attempts reconstruction.
// Returns a map of genIndex -> recovered inner plaintext for every data shard that was
// missing and could be recovered (empty if nothing was missing, or reconstruction wasn't
// yet possible -- both are ordinary, expected outcomes, not errors).
func (b *fecGenBuffer) HandleFEC(genID uint16, n, m, w, parityIndex int, shard []byte) map[int][]byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	g := b.getOrCreateLocked(genID)
	g.n, g.m, g.w = n, m, w
	g.parity[parityIndex] = append([]byte(nil), shard...)

	if len(g.data) >= n {
		return nil // nothing missing
	}
	if len(g.data)+len(g.parity) < n {
		return nil // not enough shards yet to even attempt reconstruction
	}

	shards := make([][]byte, n+m)
	present := make([]bool, n+m)
	for i, d := range g.data {
		if i < n {
			shards[i] = d
			present[i] = true
		}
	}
	for i, p := range g.parity {
		if i < m {
			shards[n+i] = p
			present[n+i] = true
		}
	}

	recovered, err := fec.Reconstruct(shards, present, n, m, w)
	if err != nil {
		return nil // includes fec.ErrTooFewShards -- an expected outcome under heavy loss
	}
	for idx, payload := range recovered {
		g.data[idx] = payload
	}
	return recovered
}

// GC evicts generations whose first shard arrived more than maxAge ago, bounding memory
// growth from generations that never complete (e.g. every copy of every shard was lost).
// Call periodically alongside fecSender.Flush.
func (b *fecGenBuffer) GC(maxAge time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	for id, g := range b.gens {
		if now.Sub(g.firstSeen) > maxAge {
			delete(b.gens, id)
		}
	}
}

// unmarshalRecovered parses a reconstructed inner plaintext (header + payload, exactly
// what fecSender protected) back into its InnerDataHeader and payload, so the caller can
// push the payload into its reorder buffer under the recovered header's GSN. Reports false
// if the reconstructed bytes don't parse as a valid inner header -- reconstruction succeeds
// at the erasure-coding level but the result is still just bytes, so this stays a normal,
// It reports whether the plaintext contains a valid inner data header.
func unmarshalRecovered(innerPlaintext []byte) (proto.InnerDataHeader, []byte, bool) {
	h, n, err := proto.UnmarshalInner(innerPlaintext)
	if err != nil {
		return proto.InnerDataHeader{}, nil, false
	}
	return h, innerPlaintext[n:], true
}
