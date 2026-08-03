package bond

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/chewtoo22-rgb/bondify/core/crypto"
	"github.com/chewtoo22-rgb/bondify/core/reorder"
	"github.com/chewtoo22-rgb/bondify/core/sched"
)

// newClientSessionForTest completes a real Noise_IK handshake in-process and returns just
// the client (initiator) side session -- enough to seal outgoing packets for these send-path
// tests, which only check which physical UDP socket a packet lands on, not that a real relay
// can decrypt it (client_runtime_path_test.go already covers full wire round trips).
func newClientSessionForTest(t *testing.T) *crypto.Session {
	t.Helper()
	clientKP, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("client keypair: %v", err)
	}
	relayKP, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("relay keypair: %v", err)
	}
	initiator, err := crypto.NewInitiator(clientKP, relayKP.Public)
	if err != nil {
		t.Fatalf("new initiator: %v", err)
	}
	responder, err := crypto.NewResponder(relayKP)
	if err != nil {
		t.Fatalf("new responder: %v", err)
	}
	initMsg, err := initiator.WriteInit(nil)
	if err != nil {
		t.Fatalf("write init: %v", err)
	}
	if _, _, err := responder.ReadInit(initMsg); err != nil {
		t.Fatalf("read init: %v", err)
	}
	respMsg, _, err := responder.WriteResponse(nil)
	if err != nil {
		t.Fatalf("write response: %v", err)
	}
	_, sess, err := initiator.ReadResponse(respMsg)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return sess
}

// loopbackPathPair returns a *Path backed by a real connected loopback UDP socket, and the
// "peer" socket on the other end that a test reads from to observe what was sent.
func loopbackPathPair(t *testing.T, id uint8) (*Path, *net.UDPConn) {
	t.Helper()
	peer, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen peer: %v", err)
	}
	t.Cleanup(func() { _ = peer.Close() })
	client, err := net.DialUDP("udp", nil, peer.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatalf("dial client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	p := NewPath(id, client)
	p.SetActive()
	return p, peer
}

// newSendTestTunnel builds a *ClientTunnel with n real loopback-backed paths, not going
// through DialHandshake (no relay needed for these send-path dispatch tests). Returns the
// tunnel and each path's peer socket, in path-ID order.
func newSendTestTunnel(t *testing.T, n int) (*ClientTunnel, []*net.UDPConn) {
	t.Helper()
	tun := &ClientTunnel{
		sess:         newClientSessionForTest(t),
		sessionIndex: 1,
		sched:        sched.NewRoundRobin(),
		reorderBuf:   reorder.New(reorder.DefaultDeadlineMin, 0),
		ack:          newACKState(),
	}
	peers := make([]*net.UDPConn, n)
	paths := make([]*Path, n)
	for i := 0; i < n; i++ {
		p, peer := loopbackPathPair(t, uint8(i))
		paths[i] = p
		peers[i] = peer
	}
	tun.paths = paths
	view := make([]sched.Path, n)
	for i, p := range paths {
		view[i] = p
	}
	tun.schedPathView.Store(view)
	tun.rtx = newRetransmitQueue(func(pathID uint8, bytes int) {
		if p := tun.pathByID(pathID); p != nil {
			p.releaseInFlight(bytes)
		}
	})
	return tun, peers
}

func setPathRTT(p *Path, rtt time.Duration) {
	now := time.Now()
	p.HandleProbeAck(ProbeAckPayload{SentAtUnixNano: now.Add(-rtt).UnixNano(), SentPSN: 0, RecvPSN: 0}, now)
}

func recvWithin(t *testing.T, conn *net.UDPConn, d time.Duration) bool {
	t.Helper()
	buf := make([]byte, 2048)
	if err := conn.SetReadDeadline(time.Now().Add(d)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	_, err := conn.Read(buf)
	return err == nil
}

func buildUDPPacket(dstPort uint16) []byte {
	pkt := make([]byte, 28) // 20-byte IPv4 header + 8-byte UDP header
	pkt[0] = 0x45
	pkt[9] = 17 // UDP
	pkt[22] = byte(dstPort >> 8)
	pkt[23] = byte(dstPort)
	return pkt
}

func buildTCPPacket(dstPort uint16) []byte {
	pkt := make([]byte, 40) // 20-byte IPv4 header + 20-byte TCP header
	pkt[0] = 0x45
	pkt[9] = 6 // TCP
	pkt[22] = byte(dstPort >> 8)
	pkt[23] = byte(dstPort)
	return pkt
}

func buildEFMarkedUDPPacket(dstPort uint16) []byte {
	pkt := buildUDPPacket(dstPort)
	pkt[1] = 0x2E << 2 // DSCP EF -- classify.Realtime
	return pkt
}

func TestSendPinnedUsesOnlyLowestRTTPath(t *testing.T) {
	tun, peers := newSendTestTunnel(t, 2)
	setPathRTT(tun.paths[0], 15*time.Millisecond)
	setPathRTT(tun.paths[1], 200*time.Millisecond)

	tun.sendPinned([]byte("latency-sensitive payload"))

	if !recvWithin(t, peers[0], time.Second) {
		t.Fatal("fast path (0) never received the pinned packet")
	}
	if recvWithin(t, peers[1], 100*time.Millisecond) {
		t.Fatal("slow path (1) received a copy -- LATENCY traffic must never split")
	}
}

func TestSendClassifiedRealtimeDuplicatesOntoBothPaths(t *testing.T) {
	tun, peers := newSendTestTunnel(t, 2)
	tun.classify = true
	setPathRTT(tun.paths[0], 15*time.Millisecond)
	setPathRTT(tun.paths[1], 25*time.Millisecond)

	tun.sendClassified(buildEFMarkedUDPPacket(5004)) // DSCP EF -> classify.Realtime

	if !recvWithin(t, peers[0], time.Second) {
		t.Fatal("path 0 never received the REALTIME packet")
	}
	if !recvWithin(t, peers[1], time.Second) {
		t.Fatal("path 1 never received the REALTIME packet -- REALTIME must duplicate onto both eligible paths")
	}
}

func TestSendClassifiedLatencyPinsToSinglePath(t *testing.T) {
	tun, peers := newSendTestTunnel(t, 2)
	tun.classify = true
	setPathRTT(tun.paths[0], 15*time.Millisecond)
	setPathRTT(tun.paths[1], 200*time.Millisecond)

	tun.sendClassified(buildUDPPacket(53)) // DNS -> classify.Latency

	if !recvWithin(t, peers[0], time.Second) {
		t.Fatal("fast path (0) never received the LATENCY packet")
	}
	if recvWithin(t, peers[1], 100*time.Millisecond) {
		t.Fatal("slow path (1) received a copy of a LATENCY packet -- must never split")
	}
}

func TestSendClassifiedInteractiveAndBulkGoThroughScheduler(t *testing.T) {
	tun, peers := newSendTestTunnel(t, 1)
	tun.classify = true

	tun.sendClassified(buildTCPPacket(22)) // SSH -> classify.Interactive
	if !recvWithin(t, peers[0], time.Second) {
		t.Fatal("INTERACTIVE packet never reached the only path via the scheduler")
	}

	tun.sendClassified(buildTCPPacket(443)) // HTTPS -> classify.Bulk (default)
	if !recvWithin(t, peers[0], time.Second) {
		t.Fatal("BULK packet never reached the only path via the headroom-capped scheduler")
	}
}

func TestSendClassifiedNotReachedWhenClassifyDisabled(t *testing.T) {
	// tunToNet only calls sendClassified when t.classify is set; this just documents that
	// invariant lives in tunToNet's switch, not sendClassified itself (which has no such
	// guard -- callers gate it). See client.go's tunToNet.
	tun, _ := newSendTestTunnel(t, 1)
	if tun.classify {
		t.Fatal("classify should default to false (opt-in, existing throughput gates unaffected)")
	}
}

func TestBulkPacerWaitsForHeadroomAndACKReleasesInFlight(t *testing.T) {
	tun, peers := newSendTestTunnel(t, 1)
	tun.classify = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := tun.startBulkPacer(ctx); err != nil {
		t.Fatalf("startBulkPacer: %v", err)
	}
	defer tun.bulkPacer.Load().Close()
	if pacing := tun.Diagnostics().Aggregate.BulkPacing; pacing == nil || pacing.QueueCapacity != DefaultBulkQueuePackets {
		t.Fatalf("diagnostics bulk_pacing = %+v, want active queue capacity %d", pacing, DefaultBulkQueuePackets)
	}

	path := tun.paths[0]
	path.inflight.Store(path.CWND())
	pkt := buildTCPPacket(443) // BULK
	tun.sendClassified(pkt)
	if recvWithin(t, peers[0], 50*time.Millisecond) {
		t.Fatal("BULK packet bypassed the congestion-window headroom cap")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && tun.bulkPacer.Load().Snapshot().SchedulerWaits == 0 {
		time.Sleep(time.Millisecond)
	}
	if tun.bulkPacer.Load().Snapshot().SchedulerWaits == 0 {
		t.Fatal("pacer did not report waiting for scheduler headroom")
	}

	path.inflight.Store(0)
	if !recvWithin(t, peers[0], time.Second) {
		t.Fatal("queued BULK packet was not sent after headroom became available")
	}
	if got := path.InFlight(); got != int64(len(pkt)) {
		t.Fatalf("in-flight after send = %d, want packet size %d", got, len(pkt))
	}
	tun.rtx.Acknowledge(AckPayload{HasCumulative: true, CumulativeGSN: 0}, time.Now())
	if got := path.InFlight(); got != 0 {
		t.Fatalf("in-flight after ACK = %d, want 0", got)
	}
}

// fakeSchedPath is a minimal sched.Path for testing capByHeadroom in isolation, without
// needing a real *Path's congestion controller.
type fakeSchedPath struct {
	id             uint8
	inFlight, cwnd int64
}

func (f fakeSchedPath) ID() uint8             { return f.id }
func (f fakeSchedPath) State() sched.State    { return sched.StateActive }
func (f fakeSchedPath) Role() sched.Role      { return sched.RoleBond }
func (f fakeSchedPath) InFlight() int64       { return f.inFlight }
func (f fakeSchedPath) CWND() int64           { return f.cwnd }
func (f fakeSchedPath) RTTMin() time.Duration { return 0 }
func (f fakeSchedPath) Goodput() float64      { return 0 }

func TestCapByHeadroomExcludesPathsOverThreshold(t *testing.T) {
	paths := []sched.Path{
		fakeSchedPath{id: 0, inFlight: 50, cwnd: 100}, // 50% -- well under 90%
		fakeSchedPath{id: 1, inFlight: 95, cwnd: 100}, // 95% -- over the cap
		fakeSchedPath{id: 2, inFlight: 89, cwnd: 100}, // 89% -- just under the cap
		fakeSchedPath{id: 3, inFlight: 90, cwnd: 100}, // exactly at the cap -- excluded (strict <)
	}
	got := capByHeadroom(paths, bulkHeadroomFraction, 1)
	gotIDs := map[uint8]bool{}
	for _, p := range got {
		gotIDs[p.ID()] = true
	}
	want := map[uint8]bool{0: true, 2: true}
	if len(got) != len(want) || gotIDs[1] || gotIDs[3] || !gotIDs[0] || !gotIDs[2] {
		t.Fatalf("capByHeadroom kept ids %v, want exactly {0, 2}", gotIDs)
	}
}
