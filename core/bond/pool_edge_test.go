package bond

import (
	"net"
	"testing"
)

func TestIPPoolRejectsIPv6AndTooSmallSubnets(t *testing.T) {
	t.Parallel()

	for _, cidr := range []string{
		"2001:db8::/64",
		"10.77.0.0/31",
		"10.77.0.1/32",
	} {
		cidr := cidr
		t.Run(cidr, func(t *testing.T) {
			t.Parallel()
			if _, err := NewIPPool(cidr); err == nil {
				t.Fatalf("NewIPPool(%q) succeeded; want rejection", cidr)
			}
		})
	}
}

func TestIPPoolSlash30HasExactlyOneReusableClientLease(t *testing.T) {
	t.Parallel()

	pool, err := NewIPPool("10.77.0.0/30")
	if err != nil {
		t.Fatalf("NewIPPool: %v", err)
	}
	if got, want := pool.Gateway().String(), "10.77.0.1"; got != want {
		t.Fatalf("gateway = %s; want %s", got, want)
	}
	if got, want := pool.Prefix(), 30; got != want {
		t.Fatalf("prefix = %d; want %d", got, want)
	}

	lease, err := pool.Allocate()
	if err != nil {
		t.Fatalf("first Allocate: %v", err)
	}
	if got, want := lease.String(), "10.77.0.2"; got != want {
		t.Fatalf("lease = %s; want %s", got, want)
	}
	if _, err := pool.Allocate(); err == nil {
		t.Fatal("second Allocate succeeded; /30 should expose exactly one client lease")
	}

	pool.Release(net.ParseIP("10.77.0.2"))
	reused, err := pool.Allocate()
	if err != nil {
		t.Fatalf("Allocate after Release: %v", err)
	}
	if !reused.Equal(net.ParseIP("10.77.0.2")) {
		t.Fatalf("reused lease = %s; want 10.77.0.2", reused)
	}
}
