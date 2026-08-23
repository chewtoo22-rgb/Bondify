package main

import (
	"strings"
	"testing"

	"github.com/chewtoo22-rgb/bondify/core/bond"
)

func validCfgPush() bond.HandshakeRespPayload {
	return bond.HandshakeRespPayload{
		SessionIndex: 1,
		TunnelIP:     "10.77.0.2",
		Prefix:       24,
		GatewayIP:    "10.77.0.1",
		MTU:          1280,
	}
}

func TestEffectiveTunnelMTUPrefersRelayPush(t *testing.T) {
	cfg := validCfgPush()
	got, err := effectiveTunnelMTU(cfg, 1408)
	if err != nil {
		t.Fatalf("effectiveTunnelMTU: %v", err)
	}
	if got != 1280 {
		t.Fatalf("got MTU %d, want relay-pushed 1280", got)
	}
}

func TestEffectiveTunnelMTULegacyZeroFallsBack(t *testing.T) {
	cfg := validCfgPush()
	cfg.MTU = 0
	got, err := effectiveTunnelMTU(cfg, 1408)
	if err != nil {
		t.Fatalf("effectiveTunnelMTU: %v", err)
	}
	if got != 1408 {
		t.Fatalf("got MTU %d, want fallback 1408", got)
	}
}

func TestEffectiveTunnelMTURejectsBadSemantics(t *testing.T) {
	tests := []struct {
		name string
		edit func(*bond.HandshakeRespPayload)
		want string
	}{
		{"bad tunnel ip", func(c *bond.HandshakeRespPayload) { c.TunnelIP = "not-an-ip" }, "tunnel IPv4"},
		{"ipv6 tunnel ip", func(c *bond.HandshakeRespPayload) { c.TunnelIP = "2001:db8::2" }, "tunnel IPv4"},
		{"bad prefix", func(c *bond.HandshakeRespPayload) { c.Prefix = 33 }, "prefix"},
		{"bad gateway", func(c *bond.HandshakeRespPayload) { c.GatewayIP = "bad" }, "gateway IPv4"},
		{"mtu too small", func(c *bond.HandshakeRespPayload) { c.MTU = 575 }, "invalid tunnel MTU"},
		{"mtu too large", func(c *bond.HandshakeRespPayload) { c.MTU = 65536 }, "invalid tunnel MTU"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validCfgPush()
			tt.edit(&cfg)
			_, err := effectiveTunnelMTU(cfg, 1408)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}
