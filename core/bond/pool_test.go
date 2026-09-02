package bond

import (
	"net"
	"testing"
)

func TestIPPoolReleaseIgnoresUnknownAndDuplicateAddresses(t *testing.T) {
	p, err := NewIPPool("10.10.0.0/30")
	if err != nil {
		t.Fatal(err)
	}
	first, err := p.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	p.Release(net.ParseIP("10.10.0.1"))
	p.Release(net.ParseIP("10.10.0.3"))
	p.Release(first)
	p.Release(first)
	reused, err := p.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	if !reused.Equal(first) {
		t.Fatalf("reused %s, want %s", reused, first)
	}
	second, err := p.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	if second.Equal(first) {
		t.Fatalf("second allocation reused %s", first)
	}
	if _, err := p.Allocate(); err == nil {
		t.Fatal("expected pool to be exhausted after allocating both owned addresses")
	}
}

func TestIPPoolReleasesOnlyOwnedAddresses(t *testing.T) {
	p, err := NewIPPool("192.168.44.0/29")
	if err != nil {
		t.Fatal(err)
	}
	p.Release(net.ParseIP("192.168.44.7"))
	p.Release(net.ParseIP("192.168.44.1"))
	got, err := p.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(net.ParseIP("192.168.44.2")) {
		t.Fatalf("got %s, want first alloc 192.168.44.2", got)
	}
} 
