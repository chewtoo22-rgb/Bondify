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
// future reconstruction of a sibling shard in the same generation. DATA genIndex is bounded
// by the sender's protocol constant fec.K before any allocation/copy: a peer can authenticate
// arbitrary inner headers, but it cannot legitimately produce index K or larger because a
// generation closes as soon as K data shards are recorded.
func (b *fecGenBuffer) HandleData(genID uint16, genIndex int, innerPlaintext []byte) {
	if genIndex < 0 || genIndex >= fec.K {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	g := b.getOrCreateLocked(genID)
	if g.n > 0 && genIndex >= g.n {
		return
	}
	g.data[genIndex] = append([]byte(nil), innerPlaintext...)
}

// HandleFEC records one parity shard and, once the generation has n or more total shards
// present (data+parity) and is missing at least one data shard, attempts reconstruction.
// Returns a map of genIndex -> recovered inner plaintext for every data shard that was
// missing and could be recovered (empty if nothing was missing, or reconstruction wasn't
// yet possible -- both are ordinary, expected outcomes, not errors).
//
// All wire-supplied geometry is constrained to values Bondify's own sender can emit before
// it is retained or passed to Reed-Solomon. That is a resource-safety boundary as well as a
// correctness check: authenticated peers are still untrusted inputs, and allowing n/m far
// above fec.K/MaxRedundancy can turn one small control stream into disproportionate decoder
// allocations/CPU. Parity shard width must also exactly match W before it is copied.
func (b *fecGenBuffer) HandleFEC(genID uint16, n, m, w, parityIndex int, shard []byte) map[int][]byte {
	if n <= 0 || n > fec.K || m <= 0 || m > fec.RedundancyFor(1, n) || w <= 0 || len(shard) != w || parityIndex < 0 || parityIndex >= m {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	g := b.getOrCreateLocked(genID)
	if g.n == 0 {
		g.n, g.m, g.w = n, m, w
		// DATA can arrive before the parity packet that tells us N. Keep the sender-side
		// NextSlot/Flush race tolerance for indices < fec.K, but once N is known discard
		// entries outside this generation's actual data range.
		for i := range g.data {
			if i >= g.n {
				delete(g.data, i)
			}
		}
	} else if g.n != n || g.m != m || g.w != w {
		return nil // geometry mismatch with what this generation was first observed as
	}
	g.parity[parityIndex] = append([]byte(nil), shard...)

	// Count only data entries at a valid index -- a stale/out-of-range genIndex (e.g. from
	// a data packet stamped just before its generation closed, see ARCHITECTURE.md §9)
	// must not be able to masquerade as "nothing missing" and suppress a real recovery.
	haveData := 0
	for i := range g.data {
		if i < g.n {
			haveData++
		}
	}
	if haveData >= g.n {
		// No future parity can add value once every data shard is already present. Drop the
		// generation immediately instead of retaining it until the periodic one-second GC.
		delete(b.gens, genID)
		return nil
	}
	if haveData+len(g.parity) < g.n {
		return nil // not enough shards yet to even attempt reconstruction
	}

	shards := make([][]byte, g.n+g.m)
	present := make([]bool, g.n+g.m)
	for i, d := range g.data {
		if i < g.n {
			shards[i] = d
			present[i] = true
		}
	}
	for i, p := range g.parity {
		if i < g.m {
			shards[g.n+i] = p
			present[g.n+i] = true
		}
	}

	recovered, err := fec.Reconstruct(shards, present, g.n, g.m, g.w)
	if err != nil {
		return nil // includes fec.ErrTooFewShards -- an expected outcome under heavy loss
	}
	for idx, payload := range recovered {
		g.data[idx] = payload
	}
	// Successful Reed-Solomon reconstruction fills every missing data slot for this
	// generation, so retaining its maps until GC only increases the memory footprint under
	// sustained loss. The returned slices remain valid after removing the map entry.
	delete(b.gens, genID)
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
// push the payload into the reorder buffer under the recovered header's GSN. Reports false
// if the reconstructed bytes don't parse as a valid inner header -- reconstruction succeeds
// at the erasure-coding level but the result is still just bytes, so this stays a normal,
// handled outcome rather than a panic-worthy invariant violation.
func unmarshalRecovered(innerPlaintext []byte) (proto.InnerDataHeader, []byte, bool) {
	h, n, err := proto.UnmarshalInner(innerPlaintext)
	if err != nil {
		return proto.InnerDataHeader{}, nil, false
	}
	return h, innerPlaintext[n:], true
}
