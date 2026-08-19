package split

import (
	"net/netip"
	"slices"
	"testing"
)

func TestResolveDefaultsAndCustomRules(t *testing.T) {
	got, err := Resolve(true, " 203.0.113.7/24,10.0.0.0/8,198.51.100.4/32 ")
	if err != nil {
		t.Fatal(err)
	}
	wantCustom := []netip.Prefix{
		netip.MustParsePrefix("198.51.100.4/32"),
		netip.MustParsePrefix("203.0.113.0/24"),
	}
	for _, want := range wantCustom {
		if !slices.Contains(got, want) {
			t.Fatalf("Resolve result %v does not contain %s", got, want)
		}
	}
	if gotCount := countPrefix(got, netip.MustParsePrefix("10.0.0.0/8")); gotCount != 1 {
		t.Fatalf("deduplicated 10/8 count = %d, want 1", gotCount)
	}
}

func TestResolveCanDisableDefaults(t *testing.T) {
	got, err := Resolve(false, "203.0.113.0/24")
	if err != nil {
		t.Fatal(err)
	}
	want := []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")}
	if !slices.Equal(got, want) {
		t.Fatalf("Resolve(false) = %v, want %v", got, want)
	}
}

func TestResolveRejectsInvalidAndIPv6Rules(t *testing.T) {
	for _, input := range []string{"not-a-cidr", "2001:db8::/32"} {
		if _, err := Resolve(false, input); err == nil {
			t.Fatalf("Resolve(%q) accepted unsupported input", input)
		}
	}
}

func TestProbeAddress(t *testing.T) {
	for _, tc := range []struct {
		prefix string
		want   string
	}{
		{"10.0.0.0/8", "10.255.255.254"},
		{"192.0.2.0/31", "192.0.2.1"},
		{"198.51.100.8/32", "198.51.100.8"},
	} {
		got, err := ProbeAddress(netip.MustParsePrefix(tc.prefix))
		if err != nil {
			t.Fatal(err)
		}
		if got.String() != tc.want {
			t.Fatalf("ProbeAddress(%s) = %s, want %s", tc.prefix, got, tc.want)
		}
	}
}

func countPrefix(prefixes []netip.Prefix, want netip.Prefix) int {
	n := 0
	for _, prefix := range prefixes {
		if prefix == want {
			n++
		}
	}
	return n
}
