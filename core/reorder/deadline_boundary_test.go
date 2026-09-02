package reorder

import (
	"testing"
	"time"
)

func TestDeadlineReleasesGapThenDrainsContiguousPackets(t *testing.T) {
	b := NewFrom(0, 20*time.Millisecond, DefaultBufMin)
	b.Push(Packet{GSN: 2, Payload: []byte("two")})
	b.Push(Packet{GSN: 3, Payload: []byte("three")})

	select {
	case got := <-b.Out():
		t.Fatalf("unexpected delivery before deadline: gsn=%d", got.GSN)
	case <-time.After(5 * time.Millisecond):
	}

	select {
	case got := <-b.Out():
		if got.GSN != 2 {
			t.Fatalf("deadline released gsn=%d, want 2", got.GSN)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("timed out waiting for deadline release")
	}

	select {
	case got := <-b.Out():
		if got.GSN != 3 {
			t.Fatalf("post-gap drain gsn=%d, want 3", got.GSN)
		}
	case <-time.After(50 * time.Millisecond):
		t.Fatal("timed out waiting for contiguous drain")
	}

	if got := b.ForcedReleases(); got != 1 {
		t.Fatalf("forced releases=%d, want 1", got)
	}
	if got := b.Occupancy(); got != 0 {
		t.Fatalf("occupancy=%d, want 0", got)
	}
}

func TestPushPacketBypassesOrderingWithoutChangingOccupancy(t *testing.T) {
	b := New(20*time.Millisecond, DefaultBufMin)
	b.Push(Packet{GSN: 99, Payload: []byte("latency"), Push: true})

	select {
	case got := <-b.Out():
		if got.GSN != 99 || !got.Push {
			t.Fatalf("got %+v, want immediate push packet", got)
		}
	case <-time.After(50 * time.Millisecond):
		t.Fatal("timed out waiting for push packet")
	}

	if got := b.Occupancy(); got != 0 {
		t.Fatalf("occupancy=%d, want 0 after bypass", got)
	}
}
