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
	if ip := net.ParseIP(cfg.TunnelIP); ip == nil || ip.To4() == nil {
		return 0, fmt.Errorf("invalid relay-pushed tunnel IPv4 address %q", cfg.TunnelIP)
	}
	if cfg.Prefix < 0 || cfg.Prefix > 32 {
		return 0, fmt.Errorf("invalid relay-pushed IPv4 prefix %d", cfg.Prefix)
	}
	if ip := net.ParseIP(cfg.GatewayIP); ip == nil || ip.To4() == nil {
		return 0, fmt.Errorf("invalid relay-pushed gateway IPv4 address %q", cfg.GatewayIP)
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
