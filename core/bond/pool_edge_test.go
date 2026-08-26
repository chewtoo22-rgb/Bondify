package bond

import (
	"net"
	"strings"
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

func TestIPPoolRejectsCIDRsWithHostBitsSet(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		cidr    string
		network string
	}{
		{cidr: "10.77.0.42/24", network: "10.77.0.0/24"},
		{cidr: "192.0.2.3/30", network: "192.0.2.0/30"},
		{cidr: "172.16.7.255/20", network: "172.16.0.0/20"},
	} {
		tc := tc
		t.Run(tc.cidr, func(t *testing.T) {
			t.Parallel()
			_, err := NewIPPool(tc.cidr)
			if err == nil {
				t.Fatalf("NewIPPool(%q) succeeded; want host-bit rejection", tc.cidr)
			}
			if !strings.Contains(err.Error(), "host bits set") || !strings.Contains(err.Error(), tc.network) {
				t.Fatalf("NewIPPool(%q) error = %q; want host-bit error naming canonical network %s", tc.cidr, err, tc.network)
			}
		})
	}
}

func TestIPPoolAcceptsCanonicalNetworkCIDRs(t *testing.T) {
	t.Parallel()

	for _, cidr := range []string{
		"10.77.0.0/24",
		"192.0.2.0/30",
		"172.16.0.0/20",
	} {
		cidr := cidr
		t.Run(cidr, func(t *testing.T) {
			t.Parallel()
			pool, err := NewIPPool(cidr)
			if err != nil {
				t.Fatalf("NewIPPool(%q): %v", cidr, err)
			}
			if got := pool.network.String(); got != cidr {
				t.Fatalf("pool network = %s; want exact configured network %s", got, cidr)
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
