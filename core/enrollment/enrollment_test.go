package enrollment

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func validRequest() Request {
	return Request{
		Version:   ProtocolVersion,
		Name:      "Matt's S22+",
		Platform:  PlatformAndroid,
		PublicKey: bytes.Repeat([]byte{0x42}, 32),
		Nonce:     bytes.Repeat([]byte{0x24}, MinNonceBytes),
	}
}

func TestRequestValidateAcceptsSupportedDevice(t *testing.T) {
	if err := validRequest().Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestRequestValidateFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Request)
		want error
	}{
		{"protocol", func(r *Request) { r.Version++ }, ErrProtocolVersion},
		{"name", func(r *Request) { r.Name = "bad\nname" }, ErrDeviceName},
		{"platform", func(r *Request) { r.Platform = "ios" }, ErrPlatform},
		{"public key", func(r *Request) { r.PublicKey = r.PublicKey[:31] }, ErrPublicKey},
		{"short nonce", func(r *Request) { r.Nonce = r.Nonce[:MinNonceBytes-1] }, ErrNonce},
		{"long nonce", func(r *Request) { r.Nonce = bytes.Repeat([]byte{1}, MaxNonceBytes+1) }, ErrNonce},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := validRequest()
			tt.edit(&r)
			if err := r.Validate(); !errors.Is(err, tt.want) {
				t.Fatalf("Validate() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestNormalizeDeviceName(t *testing.T) {
	got, err := NormalizeDeviceName("  My\tWindows   PC  ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "My Windows PC" {
		t.Fatalf("NormalizeDeviceName() = %q", got)
	}

	if _, err := NormalizeDeviceName(strings.Repeat("x", MaxDeviceName+1)); !errors.Is(err, ErrDeviceName) {
		t.Fatalf("overlong name error = %v", err)
	}
}

func TestDeviceIDStableAndKeyBound(t *testing.T) {
	keyA := bytes.Repeat([]byte{0x11}, 32)
	keyB := bytes.Repeat([]byte{0x12}, 32)

	a1, err := DeviceID(keyA)
	if err != nil {
		t.Fatal(err)
	}
	a2, _ := DeviceID(append([]byte(nil), keyA...))
	b, _ := DeviceID(keyB)

	if a1 != a2 {
		t.Fatalf("same key produced different IDs: %q != %q", a1, a2)
	}
	if a1 == b {
		t.Fatalf("different keys produced same ID: %q", a1)
	}
	if len(a1) != 32 {
		t.Fatalf("DeviceID length = %d, want 32 hex chars", len(a1))
	}
}

func TestDeviceIDRejectsWrongKeyLength(t *testing.T) {
	if _, err := DeviceID(make([]byte, 31)); !errors.Is(err, ErrPublicKey) {
		t.Fatalf("DeviceID() error = %v", err)
	}
}
