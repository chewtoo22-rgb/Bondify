package proto

import (
	"errors"
	"testing"
)

func TestUnmarshalOuterRejectsUnsupportedVersion(t *testing.T) {
	buf := make([]byte, OuterPrefixLen)
	if err := MarshalOuter(buf, OuterHeader{Type: TypeHandshakeInit, Version: Version}); err != nil {
		t.Fatalf("marshal: %v", err)
	}
	buf[1] = Version + 1

	got, consumed, err := UnmarshalOuter(buf)
	if !errors.Is(err, ErrBadVersion) {
		t.Fatalf("error = %v, want ErrBadVersion", err)
	}
	if consumed != 0 {
		t.Fatalf("consumed = %d, want 0 on rejected framing", consumed)
	}
	if got.Version != Version+1 {
		t.Fatalf("reported version = %d, want %d", got.Version, Version+1)
	}
}
