package bond

import (
	"net"
	"testing"
)

func TestDestIPv4ExtractsDestination(t *testing.T) {
	pkt := make([]byte, 20)
	pkt[0] = 0x45 // IPv4, 20-byte header
	copy(pkt[16:20], net.IPv4(10, 77, 0, 42).To4())

	got, ok := destIPv4(pkt)
	if !ok {
		t.Fatal("destIPv4 rejected valid IPv4 packet")
	}
	if want := net.IPv4(10, 77, 0, 42); !got.Equal(want) {
		t.Fatalf("destIPv4 = %v, want %v", got, want)
	}
}

func TestDestIPv4RejectsNonIPv4AndTruncatedPackets(t *testing.T) {
	tests := []struct {
		name string
		pkt  []byte
	}{
		{name: "empty", pkt: nil},
		{name: "truncated IPv4", pkt: []byte{0x45, 0, 0, 0}},
		{name: "IPv6", pkt: append([]byte{0x60}, make([]byte, 39)...)},
		{name: "unknown version", pkt: append([]byte{0x70}, make([]byte, 19)...)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, ok := destIPv4(tt.pkt); ok || got != nil {
				t.Fatalf("destIPv4(%x) = (%v, %v), want (nil, false)", tt.pkt, got, ok)
			}
		})
	}
}
