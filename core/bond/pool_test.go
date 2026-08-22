package bond

import (
	"net"
	"testing"
)

func TestIPPoolDoesNotAllocateBroadcast(t *testing.T) {
	p, err := NewIPPool("10.77.0.0/30")
	if err != nil {
		t.Fatal(err)
	}

	ip, err := p.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ip.String(), "10.77.0.2"; got != want {
		t.Fatalf("first client address = %s, want %s", got, want)
	}
	if _, err := p.Allocate(); err == nil {
		t.Fatal("expected /30 pool to exhaust before allocating broadcast address 10.77.0.3")
	}
}

func TestIPPoolReleaseReuseAndDuplicateSafety(t *testing.T) {
	p, err := NewIPPool("10.77.0.0/29")
	if err != nil {
		t.Fatal(err)
	}

	first, err := p.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.Allocate()
	if err != nil {
		t.Fatal(err)
	}

	p.Release(first)
	p.Release(first) // duplicate must be ignored
	p.Release(net.ParseIP("10.77.0.1")) // gateway was never allocated
	p.Release(net.ParseIP("10.77.0.7")) // broadcast was never allocated
	p.Release(net.ParseIP("192.0.2.10")) // foreign address

	reused, err := p.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	if !reused.Equal(first) {
		t.Fatalf("released address reused as %s, want %s", reused, first)
	}

	next, err := p.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	if next.Equal(first) {
		t.Fatalf("duplicate release caused address %s to be leased twice", first)
	}
	if next.Equal(second) {
		t.Fatalf("allocator returned already-live address %s", second)
	}
}

func TestIPPoolReleaseOnlyAllocatedAddress(t *testing.T) {
	p, err := NewIPPool("10.88.0.0/30")
	if err != nil {
		t.Fatal(err)
	}

	p.Release(net.ParseIP("10.88.0.2")) // valid host, but never allocated
	ip, err := p.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ip.String(), "10.88.0.2"; got != want {
		t.Fatalf("first allocation = %s, want %s", got, want)
	}
	if _, err := p.Allocate(); err == nil {
		t.Fatal("unallocated Release unexpectedly created an extra lease")
	}
}
