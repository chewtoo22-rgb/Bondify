package bond

import (
	"net"
	"testing"
)

func TestUDPAddrEqualTreatsIPv4MappedAddressAsSameEndpoint(t *testing.T) {
	plain := &net.UDPAddr{IP: net.ParseIP("192.0.2.10"), Port: 51820}
	mapped := &net.UDPAddr{IP: net.ParseIP("::ffff:192.0.2.10"), Port: 51820}

	if !udpAddrEqual(plain, mapped) {
		t.Fatal("equivalent IPv4 and IPv4-mapped IPv6 representations should not trigger a NAT rebind")
	}
}

func TestUDPAddrEqualDetectsPortRebind(t *testing.T) {
	before := &net.UDPAddr{IP: net.ParseIP("198.51.100.7"), Port: 51820}
	after := &net.UDPAddr{IP: net.ParseIP("198.51.100.7"), Port: 62000}

	if udpAddrEqual(before, after) {
		t.Fatal("same IP with a different UDP source port must be treated as a NAT rebind")
	}
}

func TestUDPAddrEqualDetectsAddressRebind(t *testing.T) {
	before := &net.UDPAddr{IP: net.ParseIP("203.0.113.4"), Port: 51820}
	after := &net.UDPAddr{IP: net.ParseIP("203.0.113.5"), Port: 51820}

	if udpAddrEqual(before, after) {
		t.Fatal("different source IPs must be treated as distinct UDP endpoints")
	}
}
