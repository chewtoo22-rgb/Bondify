package proto

import (
	"bytes"
	"testing"
)

func TestOuterRoundTrip(t *testing.T) {
	h := OuterHeader{
		Type:         TypeData,
		Version:      Version,
		SessionIndex: 0xDEADBEEF,
	}
	for i := range h.Nonce {
		h.Nonce[i] = byte(i + 1)
	}
	buf := make([]byte, OuterPrefixLen)
	if err := MarshalOuter(buf, h); err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, n, err := UnmarshalOuter(buf)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if n != OuterPrefixLen {
		t.Fatalf("consumed %d, want %d", n, OuterPrefixLen)
	}
	if got.Type != h.Type || got.Version != h.Version || got.SessionIndex != h.SessionIndex {
		t.Fatalf("round trip mismatch: got %+v want %+v", got, h)
	}
	if !bytes.Equal(got.Nonce[:], h.Nonce[:]) {
		t.Fatalf("nonce mismatch: got %x want %x", got.Nonce, h.Nonce)
	}
}

func TestOuterShortBuffer(t *testing.T) {
	if _, _, err := UnmarshalOuter(make([]byte, 5)); err != ErrShortBuffer {
		t.Fatalf("expected ErrShortBuffer, got %v", err)
	}
	if err := MarshalOuter(make([]byte, 5), OuterHeader{}); err != ErrShortBuffer {
		t.Fatalf("expected ErrShortBuffer, got %v", err)
	}
}

func TestInnerRoundTrip(t *testing.T) {
	h := InnerDataHeader{
		GSN:          0x0102030405060708,
		PSN:          0xAABBCCDD,
		PathID:       7,
		Flags:        FlagBULK | FlagFECProtected,
		PayloadLen:   1400,
		GenerationID: 42,
		GenIndex:     3,
	}
	buf := make([]byte, InnerHeaderLen)
	if err := MarshalInner(buf, h); err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, n, err := UnmarshalInner(buf)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if n != InnerHeaderLen {
		t.Fatalf("consumed %d, want %d", n, InnerHeaderLen)
	}
	if got != h {
		t.Fatalf("round trip mismatch: got %+v want %+v", got, h)
	}
}

func TestFlags(t *testing.T) {
	f := FlagBULK | FlagPUSH
	if !HasFlag(f, FlagBULK) || !HasFlag(f, FlagPUSH) {
		t.Fatal("expected flags set")
	}
	if HasFlag(f, FlagDUP) || HasFlag(f, FlagRTX) {
		t.Fatal("unexpected flag set")
	}
}

func TestHeaderOverheadConstant(t *testing.T) {
	// outer prefix(20) + inner(20) + tag(16) = 56.
	if HeaderOverhead != 56 {
		t.Fatalf("HeaderOverhead = %d, want 56", HeaderOverhead)
	}
}
