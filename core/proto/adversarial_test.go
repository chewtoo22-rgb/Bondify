package proto

import (
	"bytes"
	"errors"
	"math/rand"
	"testing"
)

// TestWireParsersRejectEveryTruncation exercises the attacker-controlled length boundary
// for every fixed-size BOND/1 header parser. A UDP peer can send any datagram length, so a
// future parser change that slices before checking its minimum size must fail in CI rather
// than becoming a remotely triggerable panic in the relay/client.
func TestWireParsersRejectEveryTruncation(t *testing.T) {
	for n := 0; n < OuterPrefixLen; n++ {
		if _, consumed, err := UnmarshalOuter(make([]byte, n)); !errors.Is(err, ErrShortBuffer) || consumed != 0 {
			t.Fatalf("UnmarshalOuter len=%d: consumed=%d err=%v, want consumed=0 ErrShortBuffer", n, consumed, err)
		}
	}
	for n := 0; n < InnerHeaderLen; n++ {
		if _, consumed, err := UnmarshalInner(make([]byte, n)); !errors.Is(err, ErrShortBuffer) || consumed != 0 {
			t.Fatalf("UnmarshalInner len=%d: consumed=%d err=%v, want consumed=0 ErrShortBuffer", n, consumed, err)
		}
	}
	for n := 0; n < FECHeaderLen; n++ {
		if _, consumed, err := UnmarshalFECHeader(make([]byte, n)); !errors.Is(err, ErrShortBuffer) || consumed != 0 {
			t.Fatalf("UnmarshalFECHeader len=%d: consumed=%d err=%v, want consumed=0 ErrShortBuffer", n, consumed, err)
		}
	}
}

// TestWireParsersAdversarialCorpus runs a deterministic pseudo-random corpus through every
// fixed-header decoder. It deliberately includes buffers much larger than the header so the
// parser contract (consume exactly the header and leave payload framing to the caller) is
// continuously checked too. The fixed seed keeps failures reproducible.
func TestWireParsersAdversarialCorpus(t *testing.T) {
	rng := rand.New(rand.NewSource(0xB01D1F1))
	for i := 0; i < 10000; i++ {
		n := rng.Intn(512)
		buf := make([]byte, n)
		if _, err := rng.Read(buf); err != nil {
			t.Fatalf("generate corpus: %v", err)
		}

		_, outerN, outerErr := UnmarshalOuter(buf)
		if n < OuterPrefixLen {
			if !errors.Is(outerErr, ErrShortBuffer) || outerN != 0 {
				t.Fatalf("outer len=%d: consumed=%d err=%v", n, outerN, outerErr)
			}
		} else if buf[1] != Version {
			if !errors.Is(outerErr, ErrBadVersion) || outerN != 0 {
				t.Fatalf("outer len=%d version=%d: consumed=%d err=%v, want consumed=0 ErrBadVersion", n, buf[1], outerN, outerErr)
			}
		} else if buf[2] != 0 || buf[3] != 0 {
			if !errors.Is(outerErr, ErrReserved) || outerN != 0 {
				t.Fatalf("outer len=%d reserved=%x%x: consumed=%d err=%v, want consumed=0 ErrReserved", n, buf[2], buf[3], outerN, outerErr)
			}
		} else if outerErr != nil || outerN != OuterPrefixLen {
			t.Fatalf("outer len=%d: consumed=%d err=%v, want %d,nil", n, outerN, outerErr, OuterPrefixLen)
		}

		_, innerN, innerErr := UnmarshalInner(buf)
		if n < InnerHeaderLen {
			if !errors.Is(innerErr, ErrShortBuffer) || innerN != 0 {
				t.Fatalf("inner len=%d: consumed=%d err=%v", n, innerN, innerErr)
			}
		} else if buf[13]&reservedFlagsMask != 0 || buf[19] != 0 {
			if !errors.Is(innerErr, ErrReserved) || innerN != 0 {
				t.Fatalf("inner len=%d flags=%#x pad=%#x: consumed=%d err=%v, want consumed=0 ErrReserved", n, buf[13], buf[19], innerN, innerErr)
			}
		} else if innerErr != nil || innerN != InnerHeaderLen {
			t.Fatalf("inner len=%d: consumed=%d err=%v, want %d,nil", n, innerN, innerErr, InnerHeaderLen)
		}

		_, fecN, fecErr := UnmarshalFECHeader(buf)
		if n < FECHeaderLen {
			if !errors.Is(fecErr, ErrShortBuffer) || fecN != 0 {
				t.Fatalf("fec len=%d: consumed=%d err=%v", n, fecN, fecErr)
			}
		} else if fecErr != nil || fecN != FECHeaderLen {
			t.Fatalf("fec len=%d: consumed=%d err=%v, want %d,nil", n, fecN, fecErr, FECHeaderLen)
		}
	}
}

func FuzzOuterHeaderRoundTrip(f *testing.F) {
	f.Add(byte(TypeData), Version, uint32(0x12345678), []byte("0123456789ab"))
	f.Add(byte(TypeProbe), Version, uint32(0), []byte{})
	f.Add(byte(TypeData), byte(0xff), uint32(7), []byte("future"))
	f.Fuzz(func(t *testing.T, typ byte, version byte, session uint32, nonceBytes []byte) {
		var nonce [NonceLen]byte
		copy(nonce[:], nonceBytes)
		want := OuterHeader{Type: Type(typ), Version: version, SessionIndex: session, Nonce: nonce}
		buf := make([]byte, OuterPrefixLen)
		if err := MarshalOuter(buf, want); err != nil {
			t.Fatalf("MarshalOuter: %v", err)
		}
		got, consumed, err := UnmarshalOuter(buf)
		if version != Version {
			if !errors.Is(err, ErrBadVersion) || consumed != 0 {
				t.Fatalf("unsupported version %d: got=%+v consumed=%d err=%v, want consumed=0 ErrBadVersion", version, got, consumed, err)
			}
			if got.Type != Type(typ) || got.Version != version {
				t.Fatalf("unsupported version diagnostics: got type=%d version=%d want type=%d version=%d", got.Type, got.Version, typ, version)
			}
			return
		}
		if err != nil {
			t.Fatalf("UnmarshalOuter: %v", err)
		}
		if consumed != OuterPrefixLen || got != want {
			t.Fatalf("round trip: got=%+v consumed=%d want=%+v consumed=%d", got, consumed, want, OuterPrefixLen)
		}
	})
}

func FuzzInnerHeaderRoundTrip(f *testing.F) {
	f.Add(uint64(1), uint32(2), byte(3), byte(FlagRTX|FlagFECProtected), uint16(1400), uint16(7), byte(4))
	f.Fuzz(func(t *testing.T, gsn uint64, psn uint32, pathID, flags byte, payloadLen, generationID uint16, genIndex byte) {
		// Round-trip fuzzing generates canonical headers. Reserved bits are exercised as
		// adversarial input by TestWireParsersAdversarialCorpus and the focused reserved-
		// field regression tests; allowing them here would intentionally ask MarshalInner
		// to construct an invalid BOND/1 header.
		flags &^= reservedFlagsMask
		want := InnerDataHeader{GSN: gsn, PSN: psn, PathID: pathID, Flags: flags, PayloadLen: payloadLen, GenerationID: generationID, GenIndex: genIndex}
		buf := bytes.Repeat([]byte{0xff}, InnerHeaderLen)
		if err := MarshalInner(buf, want); err != nil {
			t.Fatalf("MarshalInner: %v", err)
		}
		if buf[InnerHeaderLen-1] != 0 {
			t.Fatalf("padding byte not cleared: %x", buf[InnerHeaderLen-1])
		}
		got, consumed, err := UnmarshalInner(buf)
		if err != nil {
			t.Fatalf("UnmarshalInner: %v", err)
		}
		if consumed != InnerHeaderLen || got != want {
			t.Fatalf("round trip: got=%+v consumed=%d want=%+v consumed=%d", got, consumed, want, InnerHeaderLen)
		}
	})
}
