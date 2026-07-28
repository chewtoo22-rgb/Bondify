package bond

import (
	"fmt"

	"github.com/chewtoo22-rgb/bondify/core/crypto"
	"github.com/chewtoo22-rgb/bondify/core/proto"
)

// sealPacket builds a full BOND/1 wire packet for any post-handshake type (DATA, PROBE,
// ACK, ...): outer prefix in the clear, AEAD ciphertext of (inner header + payload). The
// outer prefix's fixed fields (Type/Version/Reserved/SessionIndex) are passed as
// additional authenticated data so tampering with them, even though they're unencrypted,
// is detected — the same pattern WireGuard and TLS both use for their own headers.
func sealPacket(sess *crypto.Session, typ proto.Type, sessionIndex uint32, pathID uint8, inner proto.InnerDataHeader, payload []byte) ([]byte, error) {
	innerBuf := make([]byte, proto.InnerHeaderLen+len(payload))
	if err := proto.MarshalInner(innerBuf, inner); err != nil {
		return nil, err
	}
	copy(innerBuf[proto.InnerHeaderLen:], payload)

	adBuf := make([]byte, proto.OuterFixedLen)
	adBuf[0] = byte(typ)
	adBuf[1] = proto.Version
	be32(adBuf[4:8], sessionIndex)

	ciphertext, nonce, err := sess.Seal(pathID, innerBuf, adBuf)
	if err != nil {
		return nil, fmt.Errorf("bond: seal: %w", err)
	}

	out := make([]byte, proto.OuterPrefixLen+len(ciphertext))
	if err := proto.MarshalOuter(out, proto.OuterHeader{
		Type:         typ,
		Version:      proto.Version,
		SessionIndex: sessionIndex,
		Nonce:        nonce,
	}); err != nil {
		return nil, err
	}
	copy(out[proto.OuterPrefixLen:], ciphertext)
	return out, nil
}

// openPacket authenticates and decrypts a post-handshake BOND/1 packet, returning the
// inner header and the trailing payload. Per PROTOCOL.md's "authenticate first, parse
// second" rule, callers get nothing back on failure — not even the outer header's Type —
// beyond what was already parsed in the clear to route to this function in the first place.
func openPacket(sess *crypto.Session, oh proto.OuterHeader, pathID uint8, ciphertext []byte) (proto.InnerDataHeader, []byte, error) {
	adBuf := make([]byte, proto.OuterFixedLen)
	adBuf[0] = byte(oh.Type)
	adBuf[1] = oh.Version
	be32(adBuf[4:8], oh.SessionIndex)

	plaintext, err := sess.Open(pathID, ciphertext, adBuf, oh.Nonce)
	if err != nil {
		return proto.InnerDataHeader{}, nil, err
	}
	inner, n, err := proto.UnmarshalInner(plaintext)
	if err != nil {
		return proto.InnerDataHeader{}, nil, err
	}
	return inner, plaintext[n:], nil
}

func be32(dst []byte, v uint32) {
	dst[0] = byte(v >> 24)
	dst[1] = byte(v >> 16)
	dst[2] = byte(v >> 8)
	dst[3] = byte(v)
}
