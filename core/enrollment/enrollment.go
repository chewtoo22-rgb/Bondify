package enrollment

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"unicode"
)

const (
	ProtocolVersion = 1
	MinNonceBytes   = 16
	MaxNonceBytes   = 64
	MaxDeviceName   = 64
)

var (
	ErrProtocolVersion = errors.New("unsupported enrollment protocol version")
	ErrDeviceName      = errors.New("invalid device name")
	ErrPlatform        = errors.New("unsupported device platform")
	ErrPublicKey       = errors.New("device public key must be 32 bytes")
	ErrNonce           = errors.New("enrollment nonce length out of bounds")
)

type Platform string

const (
	PlatformAndroid Platform = "android"
	PlatformWindows Platform = "windows"
	PlatformLinux   Platform = "linux"
)

type Request struct {
	Version   int
	Name      string
	Platform  Platform
	PublicKey []byte
	Nonce     []byte
}

func (r Request) Validate() error {
	if r.Version != ProtocolVersion {
		return ErrProtocolVersion
	}
	if _, err := NormalizeDeviceName(r.Name); err != nil {
		return err
	}
	if !r.Platform.Valid() {
		return ErrPlatform
	}
	if len(r.PublicKey) != 32 {
		return ErrPublicKey
	}
	if len(r.Nonce) < MinNonceBytes || len(r.Nonce) > MaxNonceBytes {
		return ErrNonce
	}
	return nil
}

func (p Platform) Valid() bool {
	switch p {
	case PlatformAndroid, PlatformWindows, PlatformLinux:
		return true
	default:
		return false
	}
}

func NormalizeDeviceName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", ErrDeviceName
	}

	var b strings.Builder
	b.Grow(len(name))
	lastSpace := false
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", ErrDeviceName
		}
		if unicode.IsSpace(r) {
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
			continue
		}
		b.WriteRune(r)
		lastSpace = false
	}

	normalized := strings.TrimSpace(b.String())
	if normalized == "" || len([]rune(normalized)) > MaxDeviceName {
		return "", ErrDeviceName
	}
	return normalized, nil
}

// DeviceID returns a stable, non-secret identifier derived only from the
// device public key. It is safe to persist and compare, but it is not an
// authentication credential.
func DeviceID(publicKey []byte) (string, error) {
	if len(publicKey) != 32 {
		return "", ErrPublicKey
	}
	sum := sha256.Sum256(publicKey)
	return hex.EncodeToString(sum[:16]), nil
}
