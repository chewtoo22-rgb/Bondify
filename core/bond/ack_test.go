package bond

import (
	"reflect"
	"testing"
	"time"
)

func TestACKPayloadCBORRoundTrip(t *testing.T) {
	want := AckPayload{
		HasCumulative:   true,
		CumulativeGSN:   8,
		SACK:            []AckRange{{Start: 10, End: 12}, {Start: 15, End: 15}},
		PathCounters:    []AckPathCounter{{PathID: 0, Received: 40, RTTUsec: 15_000}, {PathID: 2, Received: 9, RTTUsec: 200_000}},
		ReorderBytes:    1234,
		ReorderDeadline: 20,
	}
	wire, err := marshalCBOR(want)
	if err != nil {
		t.Fatalf("marshal ACK: %v", err)
	}
	var got AckPayload
	if err := unmarshalCBOR(wire, &got); err != nil {
		t.Fatalf("unmarshal ACK: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ACK round trip = %#v, want %#v", got, want)
	}
}

func TestACKPathStateSharesRTTWithUnprobedPeer(t *testing.T) {
	fast := NewPath(0, nil)
	slow := NewPath(1, nil)
	applyACKPathState(AckPayload{PathCounters: []AckPathCounter{
		{PathID: 0, RTTUsec: 15_000},
		{PathID: 1, RTTUsec: 200_000},
		{PathID: 9, RTTUsec: 1}, // unknown paths are ignored
	}}, []*Path{fast, slow})

	if got := fast.RTTMin(); got != 15*time.Millisecond {
		t.Fatalf("fast peer RTT = %s, want 15ms", got)
	}
	if got := slow.RTTMin(); got != 200*time.Millisecond {
		t.Fatalf("slow peer RTT = %s, want 200ms", got)
	}
}

func TestACKPathCountersIncludeMeasuredRTT(t *testing.T) {
	p := NewPath(3, nil)
	p.SetPeerRTTMin(27 * time.Millisecond)
	got := ackPathCounters([]*Path{p})
	if len(got) != 1 || got[0].PathID != 3 || got[0].RTTUsec != 27_000 {
		t.Fatalf("path counters = %#v, want path 3 with 27000us RTT", got)
	}
}

func TestACKStateCumulativeAndSACK(t *testing.T) {
	a := newACKState()
	now := time.Unix(1_700_000_000, 0)

	a.Observe(0, now)
	a.Observe(2, now)
	a.Observe(3, now)
	a.Observe(5, now)

	s, ok := a.SnapshotIfDue(now)
	if !ok {
		t.Fatal("gap should make ACK immediately due")
	}
	if !s.payload.HasCumulative || s.payload.CumulativeGSN != 0 {
		t.Fatalf("cumulative = (%v, %d), want (true, 0)", s.payload.HasCumulative, s.payload.CumulativeGSN)
	}
	want := []AckRange{{Start: 2, End: 3}, {Start: 5, End: 5}}
	if len(s.payload.SACK) != len(want) {
		t.Fatalf("SACK ranges = %#v, want %#v", s.payload.SACK, want)
	}
	for i := range want {
		if s.payload.SACK[i] != want[i] {
			t.Fatalf("SACK[%d] = %#v, want %#v", i, s.payload.SACK[i], want[i])
		}
	}

	a.Observe(1, now)
	s, ok = a.SnapshotIfDue(now)
	if !ok || s.payload.CumulativeGSN != 3 {
		t.Fatalf("after filling gap cumulative = (%v, %d), want (true, 3)", ok, s.payload.CumulativeGSN)
	}
}

func TestACKStateCapsSACKRanges(t *testing.T) {
	a := newACKState()
	now := time.Unix(1_700_000_000, 0)
	for gsn := uint64(1); gsn < 100; gsn += 2 {
		a.Observe(gsn, now)
	}
	s, ok := a.SnapshotIfDue(now)
	if !ok {
		t.Fatal("gapped ACK not due")
	}
	if len(s.payload.SACK) != AckMaxSACKRanges {
		t.Fatalf("SACK range count = %d, want cap %d", len(s.payload.SACK), AckMaxSACKRanges)
	}
}

func TestACKStateRepresentsMissingGSNZero(t *testing.T) {
	a := newACKState()
	now := time.Unix(1_700_000_000, 0)
	a.Observe(1, now)

	s, ok := a.SnapshotIfDue(now)
	if !ok {
		t.Fatal("gap should make ACK due")
	}
	if s.payload.HasCumulative {
		t.Fatalf("HasCumulative = true before GSN 0 arrived")
	}
	if len(s.payload.SACK) != 1 || s.payload.SACK[0] != (AckRange{Start: 1, End: 1}) {
		t.Fatalf("SACK = %#v, want [1,1]", s.payload.SACK)
	}
}

func TestACKStateDelayedACKAndVersionSafety(t *testing.T) {
	a := newACKState()
	now := time.Unix(1_700_000_000, 0)
	a.Observe(0, now)
	if _, ok := a.SnapshotIfDue(now.Add(AckMaxDelay - time.Nanosecond)); ok {
		t.Fatal("ACK became due before max delay")
	}
	s, ok := a.SnapshotIfDue(now.Add(AckMaxDelay))
	if !ok {
		t.Fatal("ACK not due at max delay")
	}

	a.Observe(1, now.Add(AckMaxDelay))
	a.MarkSent(s.version, now.Add(AckMaxDelay))
	if _, ok := a.SnapshotIfDue(now.Add(2*AckMaxDelay - time.Nanosecond)); ok {
		t.Fatal("newer receive event inherited the old snapshot's expired timer")
	}
	if _, ok := a.SnapshotIfDue(now.Add(2 * AckMaxDelay)); !ok {
		t.Fatal("old snapshot incorrectly cleared a newer receive event")
	}
}

func TestACKStateSentSnapshotDoesNotRetainCoveredPacketCount(t *testing.T) {
	a := newACKState()
	now := time.Unix(1_700_000_000, 0)
	for gsn := uint64(0); gsn < AckEveryPackets; gsn++ {
		a.Observe(gsn, now)
	}
	s, ok := a.SnapshotIfDue(now)
	if !ok {
		t.Fatal("packet threshold should make ACK due")
	}

	// Model another path delivering one packet while the snapshot is written.
	a.Observe(AckEveryPackets, now.Add(time.Millisecond))
	a.MarkSent(s.version, now.Add(2*time.Millisecond))
	if _, ok := a.SnapshotIfDue(now.Add(2 * time.Millisecond)); ok {
		t.Fatal("covered packet count caused a back-to-back ACK")
	}
	if _, ok := a.SnapshotIfDue(now.Add(2*time.Millisecond + AckMaxDelay)); !ok {
		t.Fatal("concurrent receive event did not remain pending")
	}
}

func TestACKStateSendsOneUrgentACKPerGap(t *testing.T) {
	a := newACKState()
	now := time.Unix(1_700_000_000, 0)
	a.Observe(1, now)
	first, ok := a.SnapshotIfDue(now)
	if !ok {
		t.Fatal("first report of a gap should be urgent")
	}
	a.MarkSent(first.version, now)

	a.Observe(2, now.Add(time.Millisecond))
	if _, ok := a.SnapshotIfDue(now.Add(time.Millisecond)); ok {
		t.Fatal("same gap produced another immediate ACK")
	}
	if _, ok := a.SnapshotIfDue(now.Add(time.Millisecond + AckMaxDelay)); !ok {
		t.Fatal("persistent gap did not fall back to delayed-ACK timer")
	}

	a.Observe(0, now.Add(2*AckMaxDelay))
	a.MarkSent(a.version, now.Add(2*AckMaxDelay))
	a.Observe(4, now.Add(2*AckMaxDelay))
	if _, ok := a.SnapshotIfDue(now.Add(2 * AckMaxDelay)); !ok {
		t.Fatal("new cumulative gap should get a new urgent ACK")
	}
}

func TestRetransmitQueueSelectiveACKAndFastRetransmit(t *testing.T) {
	q := newRetransmitQueue()
	now := time.Unix(1_700_000_000, 0)
	for gsn := uint64(0); gsn < 4; gsn++ {
		q.Track(gsn, []byte{byte(gsn)}, 0, now)
	}

	q.Acknowledge(AckPayload{
		HasCumulative: true,
		CumulativeGSN: 0,
		SACK:          []AckRange{{Start: 2, End: 3}},
	}, now)
	q.Acknowledge(AckPayload{
		HasCumulative: true,
		CumulativeGSN: 0,
		SACK:          []AckRange{{Start: 2, End: 3}},
	}, now)
	if got := q.Due(now.Add(RetransmitFastDelay), time.Second); len(got) != 0 {
		t.Fatalf("fast retransmit fired before %d SACK reports: %#v", RetransmitSACKThreshold, got)
	}
	q.Acknowledge(AckPayload{
		HasCumulative: true,
		CumulativeGSN: 0,
		SACK:          []AckRange{{Start: 2, End: 3}},
	}, now)

	if got := q.Due(now.Add(RetransmitFastDelay-time.Nanosecond), time.Second); len(got) != 0 {
		t.Fatalf("fast retransmit fired before grace: %#v", got)
	}
	got := q.Due(now.Add(RetransmitFastDelay), time.Second)
	if len(got) != 1 || got[0].GSN != 1 || got[0].Retries != 1 {
		t.Fatalf("fast retransmit = %#v, want only GSN 1 retry 1", got)
	}
}

func TestFastRetransmitGraceStartsAtSACKThreshold(t *testing.T) {
	q := newRetransmitQueue()
	now := time.Unix(1_700_000_000, 0)
	q.Track(1, []byte{1}, 0, now.Add(-time.Second))
	ack := AckPayload{SACK: []AckRange{{Start: 2, End: 2}}}
	for i := 0; i < RetransmitSACKThreshold; i++ {
		q.Acknowledge(ack, now)
	}

	if got := q.Due(now.Add(RetransmitFastDelay-time.Nanosecond), 10*time.Second); len(got) != 0 {
		t.Fatalf("aged original bypassed SACK-time grace: %#v", got)
	}
	got := q.Due(now.Add(RetransmitFastDelay), 10*time.Second)
	if len(got) != 1 || got[0].GSN != 1 {
		t.Fatalf("fast retransmit at SACK-time grace = %#v, want GSN 1", got)
	}
}

func TestFastRetransmitAllowsMultipathReordering(t *testing.T) {
	q := newRetransmitQueue()
	now := time.Unix(1_700_000_000, 0)
	q.Track(1, []byte{1}, 0, now)
	ack := AckPayload{
		SACK:         []AckRange{{Start: 2, End: 2}},
		PathCounters: []AckPathCounter{{PathID: 0}, {PathID: 1}},
	}
	for i := 0; i < RetransmitSACKThreshold; i++ {
		q.Acknowledge(ack, now)
	}

	if got := q.Due(now.Add(RetransmitMultiDelay-time.Nanosecond), time.Second); len(got) != 0 {
		t.Fatalf("multipath retransmit fired inside reorder grace: %#v", got)
	}
	got := q.Due(now.Add(RetransmitMultiDelay), time.Second)
	if len(got) != 1 || got[0].GSN != 1 {
		t.Fatalf("multipath fast retransmit = %#v, want GSN 1", got)
	}
}

func TestRetransmitRTOMultipathFloor(t *testing.T) {
	one := NewPath(0, nil)
	one.SetActive()
	if got := retransmitRTO([]*Path{one}); got != RetransmitDefaultRTO {
		t.Fatalf("unmeasured single-path RTO = %s, want %s", got, RetransmitDefaultRTO)
	}

	two := NewPath(1, nil)
	two.SetActive()
	if got := retransmitRTO([]*Path{one, two}); got != RetransmitMultiDelay {
		t.Fatalf("unmeasured multipath RTO = %s, want %s", got, RetransmitMultiDelay)
	}
}

func TestRetransmitQueueRetryLimit(t *testing.T) {
	q := newRetransmitQueue()
	now := time.Unix(1_700_000_000, 0)
	q.Track(7, []byte("packet"), 0, now)

	for retry := 1; retry <= RetransmitMaxRetries; retry++ {
		got := q.Due(now.Add(time.Duration(retry)*RetransmitDefaultRTO), RetransmitDefaultRTO)
		if len(got) != 1 || got[0].Retries != retry {
			t.Fatalf("retry %d = %#v", retry, got)
		}
	}
	if got := q.Due(now.Add(time.Duration(RetransmitMaxRetries+1)*RetransmitDefaultRTO), RetransmitDefaultRTO); len(got) != 0 {
		t.Fatalf("queue retried beyond limit: %#v", got)
	}
	if len(q.packets) != 0 || q.bytes != 0 {
		t.Fatalf("exhausted packet retained: packets=%d bytes=%d", len(q.packets), q.bytes)
	}
}

func TestRetransmitQueuePacketBound(t *testing.T) {
	q := newRetransmitQueue()
	now := time.Unix(1_700_000_000, 0)
	for gsn := uint64(0); gsn < RetransmitMaxPackets+10; gsn++ {
		q.Track(gsn, []byte{1}, 0, now)
	}
	if len(q.packets) != RetransmitMaxPackets {
		t.Fatalf("retained packets = %d, want %d", len(q.packets), RetransmitMaxPackets)
	}
	if _, ok := q.packets[0]; ok {
		t.Fatal("oldest packet was not evicted")
	}
}

func TestRetransmitQueueStripsPhysicalFlags(t *testing.T) {
	q := newRetransmitQueue()
	now := time.Unix(1_700_000_000, 0)
	q.Track(1, []byte{1}, 0xff, now)

	got := q.Due(now.Add(RetransmitDefaultRTO), RetransmitDefaultRTO)
	if len(got) != 1 {
		t.Fatalf("due packets = %#v", got)
	}
	if got[0].Flags != retransmitSemanticFlags {
		t.Fatalf("retained flags = %#02x, want semantic-only %#02x", got[0].Flags, retransmitSemanticFlags)
	}
}
