package bond

import (
	"net"
	"testing"

	"github.com/chewtoo22-rgb/bondify/core/proto"
)

func TestRelayPathAddBindsPayloadIDToAuthenticatedPathID(t *testing.T) {
	r, relayKP := mustRelay(t)
	tun, sessionIndex := mustClientTunnel(t, r, relayKP)

	sess := relaySessionFor(r, sessionIndex)
	if sess == nil {
		t.Fatal("relay session missing after handshake")
	}

	src := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 31001}

	// The packet is genuinely authenticated as path 1, but its CBOR payload tries to
	// register path 2. Authentication alone must not let the plaintext select a different
	// control-plane identity than the one bound into the AEAD nonce.
	mismatchPayload, err := marshalCBOR(PathAddPayload{PathID: 2})
	if err != nil {
		t.Fatalf("marshal mismatched path add: %v", err)
	}
	mismatch, err := sealControl(tun.sess, proto.TypePathAdd, sessionIndex, 1, mismatchPayload)
	if err != nil {
		t.Fatalf("seal mismatched path add: %v", err)
	}
	r.handleUDP(mismatch, src)

	if sess.pathByID(1) != nil {
		t.Fatal("mismatched PATH_ADD unexpectedly created authenticated path 1")
	}
	if sess.pathByID(2) != nil {
		t.Fatal("mismatched PATH_ADD registered payload-selected path 2")
	}

	// A correctly bound PATH_ADD must still work.
	matchPayload, err := marshalCBOR(PathAddPayload{PathID: 1})
	if err != nil {
		t.Fatalf("marshal matching path add: %v", err)
	}
	match, err := sealControl(tun.sess, proto.TypePathAdd, sessionIndex, 1, matchPayload)
	if err != nil {
		t.Fatalf("seal matching path add: %v", err)
	}
	r.handleUDP(match, src)

	p := sess.pathByID(1)
	if p == nil {
		t.Fatal("matching PATH_ADD did not create path 1")
	}
	if got := p.RemoteAddr(); got == nil || !udpAddrEqual(got, src) {
		t.Fatalf("path 1 remote = %v, want %v", got, src)
	}
}

func TestRelayPathAddReregistrationRefreshesAuthenticatedEndpoint(t *testing.T) {
	r, relayKP := mustRelay(t)
	tun, sessionIndex := mustClientTunnel(t, r, relayKP)

	sess := relaySessionFor(r, sessionIndex)
	if sess == nil {
		t.Fatal("relay session missing after handshake")
	}

	pathID := uint8(3)
	payload, err := marshalCBOR(PathAddPayload{PathID: pathID})
	if err != nil {
		t.Fatalf("marshal path add: %v", err)
	}

	firstSrc := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 32001}
	first, err := sealControl(tun.sess, proto.TypePathAdd, sessionIndex, pathID, payload)
	if err != nil {
		t.Fatalf("seal first path add: %v", err)
	}
	r.handleUDP(first, firstSrc)

	p := sess.pathByID(pathID)
	if p == nil {
		t.Fatal("first PATH_ADD did not create path")
	}
	if got := p.RemoteAddr(); got == nil || !udpAddrEqual(got, firstSrc) {
		t.Fatalf("first remote = %v, want %v", got, firstSrc)
	}

	// Re-register the same authenticated PathID from a new UDP source. Android network
	// callbacks and NAT churn can produce this exact sequence before a probe/data packet is
	// available. The PATH_ADD ACK is proof that the relay accepted the new source, so return
	// traffic must immediately follow it rather than continue targeting the stale endpoint.
	secondSrc := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 32002}
	second, err := sealControl(tun.sess, proto.TypePathAdd, sessionIndex, pathID, payload)
	if err != nil {
		t.Fatalf("seal second path add: %v", err)
	}
	r.handleUDP(second, secondSrc)

	if got := p.RemoteAddr(); got == nil || !udpAddrEqual(got, secondSrc) {
		t.Fatalf("re-registered remote = %v, want %v", got, secondSrc)
	}
	if current := sess.pathByID(pathID); current != p {
		t.Fatal("re-registration replaced path object instead of refreshing endpoint")
	}
}
