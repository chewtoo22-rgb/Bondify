package crypto

import (
	"encoding/base64"
	"fmt"

	"golang.org/x/crypto/curve25519"
)

// DerivePublic computes the X25519 public key for a given private scalar. Used when
// loading a persisted private key from disk (only the private half is ever stored) rather
// than generating a fresh keypair.
func DerivePublic(priv [KeyLen]byte) [KeyLen]byte {
	var pub [KeyLen]byte
	curve25519.ScalarBaseMult(&pub, &priv)
	return pub
}

// EncodeKey renders a 32-byte key as standard base64, matching WireGuard's own key
// display convention so anything that already knows how to read a WireGuard key (QR
// generators, config templates) works unmodified.
func EncodeKey(k [KeyLen]byte) string {
	return base64.StdEncoding.EncodeToString(k[:])
}

// DecodeKey parses a base64-encoded 32-byte key.
func DecodeKey(s string) ([KeyLen]byte, error) {
	var out [KeyLen]byte
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return out, fmt.Errorf("crypto: decode key: %w", err)
	}
	if len(b) != KeyLen {
		return out, fmt.Errorf("crypto: key must be %d bytes, got %d", KeyLen, len(b))
	}
	copy(out[:], b)
	return out, nil
}
