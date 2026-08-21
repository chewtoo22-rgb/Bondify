package bond

import (
	"net"
	"testing"
	"time"

	"github.com/chewtoo22-rgb/bondify/core/proto"
)

func TestRelayNATRebindAcceptsAuthenticatedProbeAndRepliesToNewSource(t *testing.T) {
	r, relayKP := mustRelay(t)
	tun, sessionIndex := mustClientTunnel(t, r, relayKP)

	sess := relaySessionFor(r, sessionIndex)
	if sess == nil {
		t.Fatal("relay session missing after handshake")
	}
	path := sess.pathByID(0)
	if path == nil || path.RemoteAddr() == nil {
		t.Fatal("relay path 0 missing after handshake")
	}
	oldRemote := path.RemoteAddr()

	relayAddr := r.conn.LocalAddr().(*net.UDPAddr)
	rebound, err := net.DialUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")}, relayAddr)
	if err != nil {
		t.Fatalf("dial rebound socket: %v", err)
	}
	defer rebound.Close()
	newRemote := rebound.LocalAddr().(*net.UDPAddr)
	if udpAddrEqual(oldRemote, newRemote) {
		t.Fatalf("test did not obtain a new source tuple: old=%v new=%v", oldRemote, newRemote)
	}

	payload, err := marshalCBOR(ProbePayload{SentAtUnixNano: time.Now().UnixNano(), SentPSN: 0})
	if err != nil {
		t.Fatalf("marshal probe: %v", err)
	}
	pkt, err := sealControl(tun.sess, proto.TypeProbe, sessionIndex, 0, payload)
	if err != nil {
		t.Fatalf("seal probe: %v", err)
	}

	if err := rebound.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	if _, err := rebound.Write(pkt); err != nil {
		t.Fatalf("write rebound probe: %v", err)
	}

	buf := make([]byte, 2048)
	n, err := rebound.Read(buf)
	if err != nil {
		t.Fatalf("relay did not reply to authenticated rebound source: %v", err)
	}
	oh, _, err := proto.UnmarshalOuter(buf[:n])
	if err != nil {
		t.Fatalf("decode probe ack: %v", err)
	}
	if oh.Type != proto.TypeProbeAck {
		t.Fatalf("rebound reply type = %v, want %v", oh.Type, proto.TypeProbeAck)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		cur := path.RemoteAddr()
		if cur != nil && udpAddrEqual(cur, newRemote) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("relay did not update path 0 remote after authenticated rebind: old=%v current=%v want=%v", oldRemote, path.RemoteAddr(), newRemote)
}
