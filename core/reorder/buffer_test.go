package reorder

import (
	"testing"
	"time"
)

func recvOne(t *testing.T, b *Buffer, timeout time.Duration) Packet {
	t.Helper()
	select {
	case p := <-b.Out():
		return p
	case <-time.After(timeout):
		t.Fatal("timed out waiting for packet")
		return Packet{}
	}
}

func TestInOrderDelivery(t *testing.T) {
	b := New(20*time.Millisecond, 0)
	for i := uint64(0); i < 10; i++ {
		b.Push(Packet{GSN: i, Payload: []byte{byte(i)}})
	}
	for i := uint64(0); i < 10; i++ {
		p := recvOne(t, b, time.Second)
		if p.GSN != i {
			t.Fatalf("got GSN %d, want %d", p.GSN, i)
		}
	}
}

func TestOutOfOrderDeliveredInOrder(t *testing.T) {
	b := New(50*time.Millisecond, 0)
	order := []uint64{3, 1, 0, 2, 4}
	for _, gsn := range order {
		b.Push(Packet{GSN: gsn, Payload: []byte{byte(gsn)}})
	}
	for i := uint64(0); i < 5; i++ {
		p := recvOne(t, b, time.Second)
		if p.GSN != i {
			t.Fatalf("got GSN %d, want %d (out-of-order arrivals must still deliver in order)", p.GSN, i)
		}
	}
}

func TestGapReleasedOnDeadline(t *testing.T) {
	b := New(30*time.Millisecond, 0)
	// GSN 0 arrives, GSN 1 never arrives (lost), GSN 2 arrives.
	b.Push(Packet{GSN: 0, Payload: []byte{0}})
	p0 := recvOne(t, b, time.Second)
	if p0.GSN != 0 {
		t.Fatalf("got %d, want 0", p0.GSN)
	}
	b.Push(Packet{GSN: 2, Payload: []byte{2}})
	// GSN 2 must wait for the deadline (GSN 1 is missing), then release.
	select {
	case p := <-b.Out():
		t.Fatalf("GSN 2 delivered too early (got %d) before the gap deadline expired", p.GSN)
	case <-time.After(10 * time.Millisecond):
	}
	p2 := recvOne(t, b, time.Second)
	if p2.GSN != 2 {
		t.Fatalf("got %d, want 2 (released after deadline expiry)", p2.GSN)
	}
}

func TestPushFlagBypassesOrdering(t *testing.T) {
	b := New(200*time.Millisecond, 0)
	// GSN 0 never arrives; GSN 5 has PUSH set and must be delivered immediately
	// regardless of the gap, per PROTOCOL.md's LATENCY-class semantics.
	b.Push(Packet{GSN: 5, Payload: []byte{5}, Push: true})
	p := recvOne(t, b, 50*time.Millisecond)
	if p.GSN != 5 {
		t.Fatalf("got %d, want 5", p.GSN)
	}
}

func TestDuplicateDropped(t *testing.T) {
	b := New(20*time.Millisecond, 0)
	b.Push(Packet{GSN: 0, Payload: []byte{0}})
	recvOne(t, b, time.Second)
	b.Push(Packet{GSN: 0, Payload: []byte{0}}) // replay of already-delivered GSN
	select {
	case p := <-b.Out():
		t.Fatalf("duplicate GSN 0 should have been dropped, got delivery of %d", p.GSN)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestDuplicateWhileBufferedDropped(t *testing.T) {
	// Regression test for a real bug found under real REDUNDANT-mode traffic: a duplicate
	// arriving while the first copy is still buffered (out of order, waiting on a gap --
	// exactly what happens when the same GSN is sent on two independent paths and both
	// copies arrive before the gap ahead of them closes) used to create a second heap
	// entry with the same GSN. drainLocked only matches head==nextExpected, so once the
	// real copy delivered and nextExpected moved past it, that stale twin could never
	// drain normally again -- but it wasn't inert: if it later surfaced as the heap's new
	// minimum (deadline expiry or overflow), forceReleaseHeadLocked delivered it anyway, a
	// second, spurious copy of an already-delivered GSN. The 30ms wait this test used
	// against a 50ms deadline was too short to observe that: it passed even with the bug
	// present, since the stale entry hadn't been force-released yet. Waiting past the
	// deadline (so a still-present bug's forced release would fire) and asserting zero
	// occupancy after draining are both needed to actually exercise the failure path.
	const deadline = 50 * time.Millisecond
	b := New(deadline, 0)
	b.Push(Packet{GSN: 3, Payload: []byte{3}}) // out of order (0-2 not yet here); buffered
	b.Push(Packet{GSN: 3, Payload: []byte{3}}) // duplicate while still buffered

	b.Push(Packet{GSN: 0, Payload: []byte{0}})
	b.Push(Packet{GSN: 1, Payload: []byte{1}})
	b.Push(Packet{GSN: 2, Payload: []byte{2}})

	for i := uint64(0); i < 4; i++ {
		p := recvOne(t, b, time.Second)
		if p.GSN != i {
			t.Fatalf("got GSN %d, want %d", p.GSN, i)
		}
	}
	if got := b.Occupancy(); got != 0 {
		t.Fatalf("buffer retained %d bytes after draining GSNs 0-3; the stale duplicate wasn't dropped", got)
	}
	select {
	case p := <-b.Out():
		t.Fatalf("unexpected extra delivery of GSN %d; the buffered duplicate should have been dropped, not force-released later", p.GSN)
	case <-time.After(2 * deadline):
	}
}

func TestDuplicateWhileBufferedDoesNotDoubleCountOccupancy(t *testing.T) {
	b := New(200*time.Millisecond, 0)
	payload := []byte{1, 2, 3, 4}
	b.Push(Packet{GSN: 5, Payload: payload}) // out of order; buffered
	before := b.Occupancy()
	b.Push(Packet{GSN: 5, Payload: payload}) // duplicate while still buffered
	after := b.Occupancy()
	if after != before {
		t.Fatalf("Occupancy changed from %d to %d on a buffered duplicate; want unchanged", before, after)
	}
}

func TestOverflowForceReleases(t *testing.T) {
	// Buffer sized far below the DefaultBufMin clamp isn't representable, so build a
	// buffer whose max is the clamped minimum and push enough large packets to force
	// an overflow release well before the deadline would otherwise fire.
	b := New(200*time.Millisecond, 0) // clamped to DefaultBufMin = 256KiB
	big := make([]byte, 64*1024)      // 64KiB per packet
	// GSN 0 is missing; push GSN 1..5 (320KiB total), exceeding the 256KiB cap.
	for i := uint64(1); i <= 5; i++ {
		b.Push(Packet{GSN: i, Payload: big})
	}
	// Overflow must force-release the head (GSN 1) well before the 200ms deadline.
	p := recvOne(t, b, 100*time.Millisecond)
	if p.GSN != 1 {
		t.Fatalf("got %d, want 1 (overflow should force-release the head)", p.GSN)
	}
}

func TestOccupancyTracksBufferedBytes(t *testing.T) {
	b := New(200*time.Millisecond, 0)
	b.Push(Packet{GSN: 1, Payload: make([]byte, 100)}) // GSN 0 missing, so this stays buffered
	if occ := b.Occupancy(); occ != 100 {
		t.Fatalf("occupancy = %d, want 100", occ)
	}
	b.Push(Packet{GSN: 0, Payload: make([]byte, 50)})
	// Both should drain immediately now (0 then 1 released), occupancy back to 0.
	recvOne(t, b, time.Second)
	recvOne(t, b, time.Second)
	time.Sleep(10 * time.Millisecond)
	if occ := b.Occupancy(); occ != 0 {
		t.Fatalf("occupancy = %d, want 0 after drain", occ)
	}
}
