//go:build windows

package tun

import (
	"net"
	"testing"

	"golang.org/x/sys/windows"
)

func TestUnicastInterfaceSockoptIPv4UsesNetworkOrder(t *testing.T) {
	level, opt, value, err := unicastInterfaceSockopt(&net.UDPAddr{IP: net.ParseIP("192.0.2.10")}, 0x01020304)
	if err != nil {
		t.Fatalf("unicastInterfaceSockopt: %v", err)
	}
	if level != windows.IPPROTO_IP || opt != ipUnicastIF {
		t.Fatalf("IPv4 sockopt = level %d opt %d; want %d/%d", level, opt, windows.IPPROTO_IP, ipUnicastIF)
	}
	if uint32(value) != htonl(0x01020304) {
		t.Fatalf("IPv4 interface value = %#x; want network-order %#x", uint32(value), htonl(0x01020304))
	}
}

func TestUnicastInterfaceSockoptIPv6UsesHostOrder(t *testing.T) {
	level, opt, value, err := unicastInterfaceSockopt(&net.UDPAddr{IP: net.ParseIP("2001:db8::10")}, 0x01020304)
	if err != nil {
		t.Fatalf("unicastInterfaceSockopt: %v", err)
	}
	if level != windows.IPPROTO_IPV6 || opt != ipv6UnicastIF {
		t.Fatalf("IPv6 sockopt = level %d opt %d; want %d/%d", level, opt, windows.IPPROTO_IPV6, ipv6UnicastIF)
	}
	if uint32(value) != 0x01020304 {
		t.Fatalf("IPv6 interface value = %#x; want host-order %#x", uint32(value), uint32(0x01020304))
	}
}

func TestUnicastInterfaceSockoptRejectsUnspecifiedFamily(t *testing.T) {
	if _, _, _, err := unicastInterfaceSockopt(&net.UDPAddr{}, 7); err == nil {
		t.Fatal("expected unspecified relay address to be rejected")
	}
}
