package crypto

import (
	"bytes"
	"testing"
)

func TestIKHandshakeAndDataRoundTrip(t *testing.T) {
	clientKP, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("client keypair: %v", err)
	}
	relayKP, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("relay keypair: %v", err)
	}

	initiator, err := NewInitiator(clientKP, relayKP.Public)
	if err != nil {
		t.Fatalf("new initiator: %v", err)
	}
	responder, err := NewResponder(relayKP)
	if err != nil {
		t.Fatalf("new responder: %v", err)
	}

	initMsg, err := initiator.WriteInit(nil)
	if err != nil {
		t.Fatalf("write init: %v", err)
	}

	_, remoteStatic, err := responder.ReadInit(initMsg)
	if err != nil {
		t.Fatalf("read init: %v", err)
	}
	if remoteStatic != clientKP.Public {
		t.Fatalf("responder learned wrong client static key")
	}

	respMsg, relaySess, err := responder.WriteResponse(nil)
	if err != nil {
		t.Fatalf("write response: %v", err)
	}
	if relaySess == nil {
		t.Fatal("relay session should be non-nil after WriteResponse completes IK")
	}

	_, clientSess, err := initiator.ReadResponse(respMsg)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if clientSess == nil {
		t.Fatal("client session should be non-nil after ReadResponse completes IK")
	}

	// Client -> relay data packet on path 0.
	plaintext := []byte("hello from client, path 0")
	ad := []byte{0x10, 0x01, 0x00, 0x00} // pretend outer header bytes
	ct, nonce, err := clientSess.Seal(0, plaintext, ad)
	if err != nil {
		t.Fatalf("client seal: %v", err)
	}
	got, err := relaySess.Open(0, ct, ad, nonce)
	if err != nil {
		t.Fatalf("relay open: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("relay decrypted %q, want %q", got, plaintext)
	}

	// Relay -> client on the same path.
	reply := []byte("hello back from relay")
	ct2, nonce2, err := relaySess.Seal(0, reply, ad)
	if err != nil {
		t.Fatalf("relay seal: %v", err)
	}
	got2, err := clientSess.Open(0, ct2, ad, nonce2)
	if err != nil {
		t.Fatalf("client open: %v", err)
	}
	if !bytes.Equal(got2, reply) {
		t.Fatalf("client decrypted %q, want %q", got2, reply)
	}

	// Replay of the exact same ciphertext must be rejected.
	if _, err := relaySess.Open(0, ct, ad, nonce); err != ErrReplay {
		t.Fatalf("expected ErrReplay on replayed packet, got %v", err)
	}

	// Tampered ciphertext must fail authentication, not silently decrypt garbage.
	tampered := append([]byte(nil), ct2...)
	tampered[0] ^= 0xFF
	if _, err := clientSess.Open(0, tampered, ad, nonce2); err == nil {
		t.Fatal("tampered ciphertext should fail to decrypt")
	}
}

func TestNonceNeverReusedAcrossPaths(t *testing.T) {
	clientKP, _ := GenerateKeypair()
	relayKP, _ := GenerateKeypair()
	initiator, _ := NewInitiator(clientKP, relayKP.Public)
	responder, _ := NewResponder(relayKP)
	initMsg, _ := initiator.WriteInit(nil)
	responder.ReadInit(initMsg)
	respMsg, _, _ := responder.WriteResponse(nil)
	_, clientSess, err := initiator.ReadResponse(respMsg)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}

	// Send one packet on 20 different paths, each starting its own counter at 0.
	// The nonce gate (PROTOCOL.md's mandatory fuzz/nonce test) requires that no two
	// packets under the same key ever reuse a nonce; verify that holds by construction.
	seen := make(map[[NonceLen]byte]bool)
	for path := uint8(0); path < 20; path++ {
		for i := 0; i < 50; i++ {
			_, nonce, err := clientSess.Seal(path, []byte("x"), nil)
			if err != nil {
				t.Fatalf("seal: %v", err)
			}
			if seen[nonce] {
				t.Fatalf("nonce %x reused (path=%d, i=%d)", nonce, path, i)
			}
			seen[nonce] = true
			if nonce[0] != path {
				t.Fatalf("nonce top byte = %d, want path id %d", nonce[0], path)
			}
		}
	}
}
