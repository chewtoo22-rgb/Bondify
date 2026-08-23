package proto

import (
	"errors"
	"testing"
)

func TestUnmarshalOuterRejectsReservedBytes(t *testing.T) {
	base := make([]byte, OuterPrefixLen)
	base[1] = Version

	for _, tc := range []struct {
		name string
		idx  int
	}{
		{name: "reserved-high", idx: 2},
		{name: "reserved-low", idx: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf := append([]byte(nil), base...)
			buf[tc.idx] = 1
			if _, consumed, err := UnmarshalOuter(buf); !errors.Is(err, ErrReserved) || consumed != 0 {
				t.Fatalf("UnmarshalOuter reserved byte %d: consumed=%d err=%v, want consumed=0 ErrReserved", tc.idx, consumed, err)
			}
		})
	}
}

func TestInnerRejectsReservedFlagsAndPadding(t *testing.T) {
	valid := InnerDataHeader{Flags: FlagDUP | FlagPUSH}
	buf := make([]byte, InnerHeaderLen)
	if err := MarshalInner(buf, valid); err != nil {
		t.Fatalf("MarshalInner valid header: %v", err)
	}

	for _, reservedFlag := range []byte{0x40, 0x80, 0xc0} {
		bad := append([]byte(nil), buf...)
		bad[13] |= reservedFlag
		if _, consumed, err := UnmarshalInner(bad); !errors.Is(err, ErrReserved) || consumed != 0 {
			t.Fatalf("UnmarshalInner reserved flags %02x: consumed=%d err=%v, want consumed=0 ErrReserved", reservedFlag, consumed, err)
		}
	}

	badPad := append([]byte(nil), buf...)
	badPad[19] = 1
	if _, consumed, err := UnmarshalInner(badPad); !errors.Is(err, ErrReserved) || consumed != 0 {
		t.Fatalf("UnmarshalInner non-zero padding: consumed=%d err=%v, want consumed=0 ErrReserved", consumed, err)
	}
}

func TestMarshalInnerRejectsReservedFlags(t *testing.T) {
	for _, flags := range []uint8{0x40, 0x80, 0xc0, FlagBULK | 0x40} {
		if err := MarshalInner(make([]byte, InnerHeaderLen), InnerDataHeader{Flags: flags}); !errors.Is(err, ErrReserved) {
			t.Fatalf("MarshalInner flags=%02x: err=%v, want ErrReserved", flags, err)
		}
	}
}

func TestMarshalOuterAndInnerClearReservedStorage(t *testing.T) {
	outer := make([]byte, OuterPrefixLen)
	for i := range outer {
		outer[i] = 0xff
	}
	if err := MarshalOuter(outer, OuterHeader{Version: Version}); err != nil {
		t.Fatalf("MarshalOuter: %v", err)
	}
	if outer[2] != 0 || outer[3] != 0 {
		t.Fatalf("MarshalOuter reserved bytes = %02x%02x, want 0000", outer[2], outer[3])
	}

	inner := make([]byte, InnerHeaderLen)
	for i := range inner {
		inner[i] = 0xff
	}
	if err := MarshalInner(inner, InnerDataHeader{}); err != nil {
		t.Fatalf("MarshalInner: %v", err)
	}
	if inner[19] != 0 {
		t.Fatalf("MarshalInner padding = %02x, want 00", inner[19])
	}
}
