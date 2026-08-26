package bond

import (
	"net"
	"testing"
)

func TestIPPoolRejectsIPv4MappedIPv6ReleaseAlias(t *testing.T) {
	pool, err := NewIPPool("10.77.0.0/29")
	if err != nil {
		t.Fatal(err)
	}

	lease, err := pool.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := lease.String(), "10.77.0.2"; got != want {
		t.Fatalf("lease=%s, want %s", got, want)
	}

	// net.ParseIP returns the 16-byte IPv4-mapped form for this literal. Release must not
	// treat that alternate address-family representation as authority to free the live
	// canonical IPv4 lease.
	mapped := net.ParseIP("::ffff:10.77.0.2")
	if mapped == nil || mapped.To4() == nil {
		t.Fatal("test setup did not create an IPv4-mapped IPv6 address")
	}
	pool.Release(mapped)

	next, err := pool.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := next.String(), "10.77.0.3"; got != want {
		t.Fatalf("mapped alias released live lease: next=%s, want %s", got, want)
	}

	// The exact representation returned by Allocate remains releasable and reusable.
	pool.Release(lease)
	reused, err := pool.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	if !reused.Equal(lease) {
		t.Fatalf("canonical release reused=%s, want %s", reused, lease)
	}
}

func TestIPPoolRejectsSixteenByteIPv4ReleaseRepresentation(t *testing.T) {
	pool, err := NewIPPool("10.77.0.0/29")
	if err != nil {
		t.Fatal(err)
	}

	lease, err := pool.Allocate()
	if err != nil {
		t.Fatal(err)
	}

	// A 16-byte representation is deliberately not accepted even when To4() succeeds.
	// Lease ownership is represented canonically by the 4-byte values returned by Allocate.
	alias := net.ParseIP(lease.String())
	if alias == nil || len(alias) == net.IPv4len || alias.To4() == nil {
		t.Fatal("test setup did not create a 16-byte IPv4 representation")
	}
	pool.Release(alias)

	next, err := pool.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	if next.Equal(lease) {
		t.Fatalf("alternate representation released live lease %s", lease)
	}
}
