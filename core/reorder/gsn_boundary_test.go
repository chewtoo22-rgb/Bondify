package reorder

import (
	"testing"
	"time"
)

// --- GSN boundary / large-value property coverage (release hardening) ---
//
// GSN is uint64. Real sessions will not approach 2^64 packets; these tests document
// and lock the arithmetic and comparison behavior near the high end so a future change
// cannot silently invert ordering or accept stale traffic after a theoretical wrap.

func TestNewFromHighStartGSNInOrder(t *testing.T) {
	const start = uint64(1<<63 - 5) // near mid-high; far from 0 and from MaxUint64
	b := NewFrom(start, 20*time.Millisecond, 0)
	for i := uint64(0); i < 10; i++ {
		b.Push(Packet{GSN: start + i, Payload: []byte{byte(i)}})
	}
	for i := uint64(0); i < 10; i++ {
		p := recvOne(t, b, time.Second)
		if p.GSN != start+i {
			t.Fatalf("got GSN %d, want %d", p.GSN, start+i)
		}
	}
}

func TestLargeGSNOutOfOrderAndDuplicates(t *testing.T) {
	const base = ^uint64(0) - 20 // MaxUint64 - 20
	b := NewFrom(base, 50*time.Millisecond, 0)

	// Arrive out of order near the high end.
	order := []uint64{base + 3, base + 1, base, base + 2, base + 4}
	for _, gsn := range order {
		b.Push(Packet{GSN: gsn, Payload: []byte{byte(gsn)}})
	}
	// Duplicate of an already-buffered GSN must not increase occupancy or deliver twice.
	before := b.Occupancy()
	b.Push(Packet{GSN: base + 3, Payload: []byte{0xff}})
	if b.Occupancy() != before {
		t.Fatalf("duplicate while buffered changed occupancy")
	}

	for i := uint64(0); i < 5; i++ {
		p := recvOne(t, b, time.Second)
		if p.GSN != base+i {
			t.Fatalf("got GSN %d, want %d", p.GSN, base+i)
		}
	}

	// Stale / already-delivered high GSN must be dropped.
	b.Push(Packet{GSN: base + 2, Payload: []byte{0xee}})
	select {
	case p := <-b.Out():
		t.Fatalf("stale high GSN delivered again: %d", p.GSN)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestLargeGapNearMaxForceReleaseOnDeadline(t *testing.T) {
	const base = ^uint64(0) - 10
	deadline := 30 * time.Millisecond
	b := NewFrom(base, deadline, 0)

	b.Push(Packet{GSN: base, Payload: []byte{0}})
	if p := recvOne(t, b, time.Second); p.GSN != base {
		t.Fatalf("got %d, want base", p.GSN)
	}

	// Gap at base+1; deliver base+2 only after deadline force-release of the gap.
	b.Push(Packet{GSN: base + 2, Payload: []byte{2}})
	select {
	case p := <-b.Out():
		t.Fatalf("GSN %d delivered before gap deadline", p.GSN)
	case <-time.After(10 * time.Millisecond):
	}
	p := recvOne(t, b, time.Second)
	if p.GSN != base+2 {
		t.Fatalf("got %d, want base+2 after force release", p.GSN)
	}
	if b.ForcedReleases() == 0 {
		t.Fatal("expected at least one forced release across the gap")
	}
}

func TestStalePacketBelowHighNextExpectedDropped(t *testing.T) {
	const start = uint64(1 << 60)
	b := NewFrom(start, 20*time.Millisecond, 0)
	b.Push(Packet{GSN: start, Payload: []byte{1}})
	recvOne(t, b, time.Second)

	// Anything strictly below nextExpected must be dropped, including values that would
	// look "large" if someone cast to signed integers.
	for _, gsn := range []uint64{0, 1, start - 1, start} {
		b.Push(Packet{GSN: gsn, Payload: []byte{0xaa}})
	}
	select {
	case p := <-b.Out():
		t.Fatalf("unexpected delivery of stale GSN %d", p.GSN)
	case <-time.After(40 * time.Millisecond):
	}
}

func TestForceReleaseAdvancesPastHighGSN(t *testing.T) {
	// Overflow path: missing head, push enough large packets to force release.
	const base = ^uint64(0) - 5
	b := NewFrom(base, 200*time.Millisecond, 0) // maxBytes clamped to DefaultBufMin
	big := make([]byte, 64*1024)
	// Missing base; push base+1 .. base+5 to exceed soft byte ceiling.
	for i := uint64(1); i <= 5; i++ {
		b.Push(Packet{GSN: base + i, Payload: big})
	}
	p := recvOne(t, b, 100*time.Millisecond)
	if p.GSN != base+1 {
		t.Fatalf("overflow force-release head = %d, want %d", p.GSN, base+1)
	}
}

func TestGSNWrapSemanticsDocumented(t *testing.T) {
	// Document the arithmetic if nextExpected ever reaches MaxUint64 and is advanced.
	// A real Bondify session will never emit 2^64 DATA packets; this locks the observed
	// unsigned wrap behavior so a future change cannot silently treat wrap as a security
	// boundary without an explicit design decision.
	const max = ^uint64(0)
	b := NewFrom(max, 20*time.Millisecond, 0)
	b.Push(Packet{GSN: max, Payload: []byte{0xff}})
	p := recvOne(t, b, time.Second)
	if p.GSN != max {
		t.Fatalf("got %d, want MaxUint64", p.GSN)
	}

	// After delivering MaxUint64, nextExpected has wrapped to 0 (unsigned ++).
	// A late duplicate of MaxUint64 is "stale" relative to the wrapped cursor only if
	// comparison is unsigned < ; MaxUint64 < 0 is false, so a pure < check would NOT
	// treat it as stale. The buffer still drops it via the inHeap / already-seen path
	// only while it was buffered; once delivered, the sole protection is GSN < nextExpected.
	// Because MaxUint64 < 0 is false, a replay of MaxUint64 after wrap is accepted as a
	// "future" packet and buffered until forced out. That is the documented wrap hazard.
	b.Push(Packet{GSN: max, Payload: []byte{0xee}})
	// It should sit buffered (nextExpected==0, max is not 0), not deliver immediately.
	select {
	case p := <-b.Out():
		// If deadline is short enough it might force-release; either outcome is recorded.
		t.Logf("post-wrap MaxUint64 replay delivered via force path as GSN %d (documented wrap behavior)", p.GSN)
	case <-time.After(5 * time.Millisecond):
		// Still buffered — expected for a short wait with default min deadline 20ms.
		if occ := b.Occupancy(); occ == 0 {
			t.Fatal("post-wrap MaxUint64 replay vanished without delivery or buffering")
		}
	}
	b.Stop()
}

func TestStopClearsHighGSNState(t *testing.T) {
	const base = ^uint64(0) - 3
	b := NewFrom(base, 100*time.Millisecond, 0)
	b.Push(Packet{GSN: base + 1, Payload: make([]byte, 32)})
	if b.Occupancy() == 0 {
		t.Fatal("expected buffered occupancy before Stop")
	}
	b.Stop()
	if b.Occupancy() != 0 {
		t.Fatalf("occupancy after Stop = %d, want 0", b.Occupancy())
	}
	// Further pushes after Stop should not panic; they may buffer again if Stop only
	// clears timer/heap once — Stop is for session teardown, not permanent disable.
}
