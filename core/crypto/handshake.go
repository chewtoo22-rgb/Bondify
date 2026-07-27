// Package crypto implements BOND/1's Noise_IK_25519_ChaChaPoly_BLAKE2s handshake and
// per-path AEAD data encryption, per PROTOCOL.md §3.
//
// The handshake itself is delegated to github.com/flynn/noise, a generic Noise Protocol
// Framework implementation, configured for the exact pattern the spec calls for (IK,
// Curve25519, ChaChaPoly, BLAKE2s) — see the note in PROTOCOL.md §3 for why this is used
// instead of wireguard-go's (non-exported) internal handshake state machine.
//
// Data-plane packets are NOT encrypted through flynn/noise's CipherState. That type only
// exposes a uint64 nonce counter and always zero-pads the top 4 bytes of the 12-byte
// ChaCha20-Poly1305 nonce itself, which cannot express this protocol's required nonce
// layout of path_id(8 bits) || counter(88 bits) — the entire point of which is to make
// nonce collisions across paths structurally impossible even though every path shares the
// same transport key. So after the handshake, this package extracts the raw 32-byte
// transport keys and drives golang.org/x/crypto/chacha20poly1305 directly, constructing
// nonces exactly as specified. No custom AEAD or DH primitives are implemented anywhere in
// this package — only composition of audited building blocks.
package crypto

import (
	"crypto/rand"
	"errors"

	"github.com/flynn/noise"
)

// suite is the fixed BOND/1 handshake cipher suite: Noise_IK_25519_ChaChaPoly_BLAKE2s.
var suite = noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashBLAKE2s)

const KeyLen = 32

// Keypair is an X25519 static keypair used as a Noise identity.
type Keypair struct {
	Private [KeyLen]byte
	Public  [KeyLen]byte
}

// GenerateKeypair creates a new random X25519 static keypair.
func GenerateKeypair() (Keypair, error) {
	dh, err := noise.DH25519.GenerateKeypair(rand.Reader)
	if err != nil {
		return Keypair{}, err
	}
	var kp Keypair
	copy(kp.Private[:], dh.Private)
	copy(kp.Public[:], dh.Public)
	return kp, nil
}

var (
	ErrHandshakeNotComplete = errors.New("crypto: handshake not complete")
	ErrHandshakeComplete    = errors.New("crypto: handshake already complete")
)

// Initiator drives the client side of Noise_IK: the client always initiates, and it must
// know the relay's static public key in advance (from its config), giving 1-RTT
// establishment with client identity hiding — the entire reason IK was chosen over XX/NN.
type Initiator struct {
	hs *noise.HandshakeState
}

// NewInitiator prepares a client-side handshake against a known relay static public key.
func NewInitiator(local Keypair, remoteStatic [KeyLen]byte) (*Initiator, error) {
	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite: suite,
		Pattern:     noise.HandshakeIK,
		Initiator:   true,
		StaticKeypair: noise.DHKey{
			Private: local.Private[:],
			Public:  local.Public[:],
		},
		PeerStatic: remoteStatic[:],
	})
	if err != nil {
		return nil, err
	}
	return &Initiator{hs: hs}, nil
}

// WriteInit produces the HANDSHAKE_INIT message payload (BOND/1 type 0x01). payload is an
// optional cleartext-after-handshake-encryption message (BOND/1 sends none in message 1;
// nil is fine).
func (i *Initiator) WriteInit(payload []byte) ([]byte, error) {
	msg, _, _, err := i.hs.WriteMessage(nil, payload)
	return msg, err
}

// ReadResponse consumes the relay's HANDSHAKE_RESP (type 0x02) and completes the
// handshake, returning the decrypted payload (e.g. assigned Session Index + cfg_push) and
// the resulting Session.
func (i *Initiator) ReadResponse(msg []byte) ([]byte, *Session, error) {
	payload, cs1, cs2, err := i.hs.ReadMessage(nil, msg)
	if err != nil {
		return nil, nil, err
	}
	if cs1 == nil || cs2 == nil {
		return payload, nil, ErrHandshakeNotComplete
	}
	// Split() convention (see flynn/noise's own tests): cs1 is the initiator->responder
	// cipher, cs2 is responder->initiator. The initiator sends with cs1, receives with cs2.
	return payload, newSession(cs1, cs2), nil
}

// Responder drives the relay side of Noise_IK.
type Responder struct {
	hs *noise.HandshakeState
}

// NewResponder prepares a relay-side handshake. The relay's static keypair is fixed at
// startup and reused across all client sessions (that's what makes it identifiable to
// clients in advance).
func NewResponder(local Keypair) (*Responder, error) {
	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite: suite,
		Pattern:     noise.HandshakeIK,
		Initiator:   false,
		StaticKeypair: noise.DHKey{
			Private: local.Private[:],
			Public:  local.Public[:],
		},
	})
	if err != nil {
		return nil, err
	}
	return &Responder{hs: hs}, nil
}

// ReadInit consumes a client's HANDSHAKE_INIT, returning the decrypted payload and the
// client's static public key (now known, since IK reveals the initiator's identity in
// message 1).
func (r *Responder) ReadInit(msg []byte) (payload []byte, remoteStatic [KeyLen]byte, err error) {
	p, _, _, err := r.hs.ReadMessage(nil, msg)
	if err != nil {
		return nil, remoteStatic, err
	}
	copy(remoteStatic[:], r.hs.PeerStatic())
	return p, remoteStatic, nil
}

// WriteResponse produces HANDSHAKE_RESP and completes the handshake on the relay side.
func (r *Responder) WriteResponse(payload []byte) ([]byte, *Session, error) {
	msg, cs1, cs2, err := r.hs.WriteMessage(nil, payload)
	if err != nil {
		return nil, nil, err
	}
	if cs1 == nil || cs2 == nil {
		return msg, nil, ErrHandshakeNotComplete
	}
	// The responder receives with cs1 (initiator->responder) and sends with cs2
	// (responder->initiator) — the mirror image of the initiator's assignment above.
	return msg, newSession(cs2, cs1), nil
}
