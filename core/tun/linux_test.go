//go:build linux

package tun

import "testing"

func TestParseRouteGetWithGateway(t *testing.T) {
	got, err := parseRouteGet("203.0.113.7 via 192.0.2.1 dev eth0 src 192.0.2.10 uid 1000\n")
	if err != nil {
		t.Fatal(err)
	}
	if got.Device != "eth0" || got.Gateway.String() != "192.0.2.1" {
		t.Fatalf("route = %+v, want eth0 via 192.0.2.1", got)
	}
}

func TestParseRouteGetDirectAndLocal(t *testing.T) {
	for _, tc := range []struct {
		out, device string
	}{
		{"192.168.1.4 dev wlan0 src 192.168.1.2\n", "wlan0"},
		{"local 127.0.0.1 dev lo src 127.0.0.1\n", "lo"},
	} {
		got, err := parseRouteGet(tc.out)
		if err != nil {
			t.Fatal(err)
		}
		if got.Device != tc.device || got.Gateway != nil {
			t.Fatalf("route = %+v, want direct device %s", got, tc.device)
		}
	}
}

func TestParseRouteGetRejectsMissingDevice(t *testing.T) {
	if _, err := parseRouteGet("unreachable 203.0.113.0/24\n"); err == nil {
		t.Fatal("route without a device was accepted")
	}
}
