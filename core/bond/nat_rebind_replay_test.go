package bond

import (
	"net"
	"testing"
	"time"

	"github.com/chewtoo22-rgb/bondify/core/proto"
)

func TestRelayNATRebindReplayCannotMovePathAgain(t *testing.T) {
	r, relayKP := mustRelay(t)
	tun, sessionIndex := mustClientTunnel(t, r, relayKP)

	sess := relaySessionFor(r, sessionIndex)
	if sess == nil {
		t.Fatal("relay session missing after handshake")
	}
	path := sess.pathByID(PathZero)
	if path == nil || path.RemoteAddr() == nil {
		t.Fatal("relay path 0 missing after handshake")
	}

	relayAddr := r.conn.LocalAddr().(*net.UDPAddr)
	first, err := net.DialUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")}, relayAddr)
	if err != nil {
		t.Fatalf("dial first rebound socket: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := net.DialUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")}, relayAddr)
	if err != nil {
		t.Fatalf("dial second rebound socket: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	firstRemote := first.LocalAddr().(*net.UDPAddr)
	secondRemote := second.LocalAddr().(*net.UDPAddr)
	if udpAddrEqual(firstRemote, secondRemote) {
		t.Fatalf("test did not obtain distinct source tuples: first=%v second=%v", firstRemote, secondRemote)
	}

	payload, err := marshalCBOR(ProbePayload{SentAtUnixNano: time.Now().UnixNano(), SentPSN: 7})
	if err != nil {
		t.Fatalf("marshal probe: %v", err)
	}
	pkt, err := sealControl(tun.sess, proto.TypeProbe, sessionIndex, PathZero, payload)
	if err != nil {
		t.Fatalf("seal probe: %v", err)
	}

	if _, err := first.Write(pkt); err != nil {
		t.Fatalf("write first rebound probe: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if cur := path.RemoteAddr(); cur != nil && udpAddrEqual(cur, firstRemote) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if cur := path.RemoteAddr(); cur == nil || !udpAddrEqual(cur, firstRemote) {
		t.Fatalf("first authenticated rebind did not move path: current=%v want=%v", cur, firstRemote)
	}

	// Replaying the exact same authenticated packet from another source must fail the
	// session replay window before NAT-rebind state is updated. A captured packet must not
	// be reusable as a one-shot return-address steering primitive.
	if _, err := second.Write(pkt); err != nil {
		t.Fatalf("write replayed probe: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	if cur := path.RemoteAddr(); cur == nil || !udpAddrEqual(cur, firstRemote) {
		t.Fatalf("replayed probe moved relay path: current=%v want=%v (second=%v)", cur, firstRemote, secondRemote)
	}
}
