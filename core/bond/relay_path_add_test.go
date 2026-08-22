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
