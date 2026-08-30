package main

import (
	"fmt"
	"net"

	"github.com/chewtoo22-rgb/bondify/core/bond"
)

const (
	minTunnelMTU = 576
	maxTunnelMTU = 65535
)

// effectiveTunnelMTU validates the authenticated relay cfg_push fields the desktop client
// needs before creating/configuring its platform TUN. New relays always push MTU; zero is
// retained only as a compatibility signal for older peers and falls back to the operator's
// configured expectation.
func effectiveTunnelMTU(cfg bond.HandshakeRespPayload, fallback int) (int, error) {
	tunnelIP := net.ParseIP(cfg.TunnelIP)
	if tunnelIP == nil || tunnelIP.To4() == nil {
		return 0, fmt.Errorf("invalid relay-pushed tunnel IPv4 address %q", cfg.TunnelIP)
	}
	if cfg.Prefix < 0 || cfg.Prefix > 32 {
		return 0, fmt.Errorf("invalid relay-pushed IPv4 prefix %d", cfg.Prefix)
	}
	gatewayIP := net.ParseIP(cfg.GatewayIP)
	if gatewayIP == nil || gatewayIP.To4() == nil {
		return 0, fmt.Errorf("invalid relay-pushed gateway IPv4 address %q", cfg.GatewayIP)
	}

	mask := net.CIDRMask(cfg.Prefix, 32)
	tunnel4 := tunnelIP.To4()
	gateway4 := gatewayIP.To4()
	if !tunnel4.Mask(mask).Equal(gateway4.Mask(mask)) {
		return 0, fmt.Errorf("relay-pushed tunnel and gateway IPv4 addresses are outside the same /%d subnet", cfg.Prefix)
	}
	if tunnel4.Equal(gateway4) {
		return 0, fmt.Errorf("relay-pushed tunnel and gateway IPv4 addresses must be distinct")
	}
	if cfg.Prefix <= 30 {
		network := tunnel4.Mask(mask)
		broadcast := make(net.IP, net.IPv4len)
		for i := range network {
			broadcast[i] = network[i] | ^mask[i]
		}
		if tunnel4.Equal(network) || tunnel4.Equal(broadcast) {
			return 0, fmt.Errorf("relay-pushed tunnel IPv4 address %q is not a usable host in /%d", cfg.TunnelIP, cfg.Prefix)
		}
		if gateway4.Equal(network) || gateway4.Equal(broadcast) {
			return 0, fmt.Errorf("relay-pushed gateway IPv4 address %q is not a usable host in /%d", cfg.GatewayIP, cfg.Prefix)
		}
	}

	mtu := cfg.MTU
	if mtu == 0 {
		mtu = fallback
	}
	if mtu < minTunnelMTU || mtu > maxTunnelMTU {
		return 0, fmt.Errorf("invalid tunnel MTU %d (must be %d..%d)", mtu, minTunnelMTU, maxTunnelMTU)
	}
	return mtu, nil
}
