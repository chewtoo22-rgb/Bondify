package proto

import (
	"testing"
)

// Deterministic encapsulation-overhead and MTU-floor invariants used by callers when
// sizing tunnel payloads. These do not prove real ISP/router PMTU behavior.

func TestHeaderOverheadIsSelfConsistent(t *testing.T) {
	want := OuterFixedLen + NonceLen + InnerHeaderLen + AuthTagLen
	if HeaderOverhead != want {
		t.Fatalf("HeaderOverhead=%d, want OuterFixed+Nonce+Inner+Tag=%d", HeaderOverhead, want)
	}
	if OuterPrefixLen != OuterFixedLen+NonceLen {
		t.Fatalf("OuterPrefixLen=%d, want %d", OuterPrefixLen, OuterFixedLen+NonceLen)
	}
	// Document the concrete sizes the rest of the stack relies on.
	if OuterPrefixLen != 20 {
		t.Fatalf("OuterPrefixLen=%d, protocol currently ships 20-byte clear prefix", OuterPrefixLen)
	}
	if HeaderOverhead != 56 {
		t.Fatalf("HeaderOverhead=%d, expected 56 (20+20+16)", HeaderOverhead)
	}
}

func TestPMTUFloorAndEffectivePayloadRoom(t *testing.T) {
	if PMTUFloor < 1200 {
		t.Fatalf("PMTUFloor=%d is below the documented minimum plausible path MTU", PMTUFloor)
	}
	// On a path whose MTU equals PMTUFloor, the largest tunnelled IP payload that still
	// fits after Bondify encapsulation is PMTUFloor - HeaderOverhead.
	room := PMTUFloor - HeaderOverhead
	if room < 1000 {
		t.Fatalf("effective payload room at PMTUFloor is %d; expected >= 1000 for useful tunnelling", room)
	}
	// Near-MTU packet sizes used by callers must remain positive after overhead.
	for _, pathMTU := range []int{PMTUFloor, 1280, 1500, 9000} {
		maxPayload := pathMTU - HeaderOverhead
		if maxPayload <= 0 {
			t.Fatalf("path MTU %d leaves non-positive payload room after overhead %d", pathMTU, HeaderOverhead)
		}
	}
}

func TestMarshalOuterRejectsShortBufferNearMTUFraming(t *testing.T) {
	h := OuterHeader{Type: TypeData, Version: Version, SessionIndex: 1}
	short := make([]byte, OuterPrefixLen-1)
	if err := MarshalOuter(short, h); err == nil {
		t.Fatal("MarshalOuter accepted undersized destination buffer")
	}
	ok := make([]byte, OuterPrefixLen)
	if err := MarshalOuter(ok, h); err != nil {
		t.Fatalf("MarshalOuter: %v", err)
	}
}

func TestUnmarshalOuterTruncatedNearBoundary(t *testing.T) {
	h := OuterHeader{Type: TypeData, Version: Version, SessionIndex: 42}
	buf := make([]byte, OuterPrefixLen)
	if err := MarshalOuter(buf, h); err != nil {
		t.Fatalf("MarshalOuter setup: %v", err)
	}
	got, n, err := UnmarshalOuter(buf)
	if err != nil {
		t.Fatalf("full buffer UnmarshalOuter: %v", err)
	}
	if n != OuterPrefixLen || got.SessionIndex != 42 {
		t.Fatalf("consumed=%d session=%d, want %d / 42", n, got.SessionIndex, OuterPrefixLen)
	}
	_, _, err = UnmarshalOuter(buf[:OuterPrefixLen-1])
	if err == nil {
		t.Fatal("UnmarshalOuter accepted truncated outer prefix")
	}
}
