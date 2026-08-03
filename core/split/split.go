// Package split builds the destination-CIDR bypass plan used by the desktop client's
// route-based split tunnel.
//
// Bondify currently configures an IPv4 TUN address and IPv4 default route, so bypass rules
// are deliberately IPv4-only. Rejecting IPv6 instead of accepting a rule the OS path would
// not apply keeps operator intent honest.
package split

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

var defaultBypass = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
}

// Defaults returns the curated local/private/link-local/CGNAT bypass set. More-specific
// routes explicitly sent through Bondify still win by normal longest-prefix routing.
func Defaults() []netip.Prefix {
	return append([]netip.Prefix(nil), defaultBypass...)
}

// Resolve combines the curated defaults (when enabled) with a comma-separated custom
// list, canonicalizes network addresses, removes duplicates, and returns stable ordering.
func Resolve(includeDefaults bool, customCSV string) ([]netip.Prefix, error) {
	var prefixes []netip.Prefix
	if includeDefaults {
		prefixes = Defaults()
	}
	for _, raw := range strings.Split(customCSV, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return nil, fmt.Errorf("split: parse bypass CIDR %q: %w", raw, err)
		}
		if !prefix.Addr().Is4() {
			return nil, fmt.Errorf("split: bypass CIDR %q is IPv6; the desktop tunnel is IPv4-only", raw)
		}
		prefixes = append(prefixes, prefix.Masked())
	}

	seen := make(map[netip.Prefix]struct{}, len(prefixes))
	out := make([]netip.Prefix, 0, len(prefixes))
	for _, prefix := range prefixes {
		prefix = prefix.Masked()
		if _, ok := seen[prefix]; ok {
			continue
		}
		seen[prefix] = struct{}{}
		out = append(out, prefix)
	}
	sort.Slice(out, func(i, j int) bool {
		if cmp := out[i].Addr().Compare(out[j].Addr()); cmp != 0 {
			return cmp < 0
		}
		return out[i].Bits() < out[j].Bits()
	})
	return out, nil
}

// ProbeAddress returns an address inside prefix suitable for asking the OS which physical
// route currently reaches it. A high address avoids biasing broad private prefixes toward
// the common first-host gateway or a directly connected subnet; existing more-specific
// routes still win after the broader bypass is installed.
func ProbeAddress(prefix netip.Prefix) (netip.Addr, error) {
	if !prefix.IsValid() || !prefix.Addr().Is4() {
		return netip.Addr{}, fmt.Errorf("split: invalid IPv4 prefix %q", prefix)
	}
	prefix = prefix.Masked()
	if prefix.Bits() == 32 {
		return prefix.Addr(), nil
	}
	raw := prefix.Addr().As4()
	value := binary.BigEndian.Uint32(raw[:])
	hostBits := uint(32 - prefix.Bits())
	hostMask := ^uint32(0)
	if hostBits < 32 {
		hostMask = uint32(1)<<hostBits - 1
	}
	value |= hostMask
	if hostBits > 1 {
		value-- // avoid the traditional broadcast address where one exists
	}
	binary.BigEndian.PutUint32(raw[:], value)
	return netip.AddrFrom4(raw), nil
}
