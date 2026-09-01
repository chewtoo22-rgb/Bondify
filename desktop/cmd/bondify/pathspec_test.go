package main

import (
	"strings"
	"testing"
)

func TestParseLocalPathSpecs(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantLen int
		wantErr string
	}{
		{name: "empty uses system path", raw: "", wantLen: 0},
		{name: "single ipv4", raw: "10.60.0.1", wantLen: 1},
		{name: "dual pinned", raw: "10.60.0.1@wlan0,10.61.0.1@wwan0", wantLen: 2},
		{name: "ipv6 canonicalized", raw: "2001:0db8::1@Ethernet 2", wantLen: 1},
		{name: "empty element", raw: "10.60.0.1@wlan0,", wantErr: "is empty"},
		{name: "invalid ip", raw: "not-an-ip@wlan0", wantErr: "invalid bind IP"},
		{name: "empty device", raw: "10.60.0.1@", wantErr: "empty device"},
		{name: "multiple separators", raw: "10.60.0.1@wlan0@oops", wantErr: "more than one @"},
		{name: "duplicate pair", raw: "10.60.0.1@wlan0,10.60.0.1@wlan0", wantErr: "duplicates bind address/device pair"},
		{name: "duplicate address", raw: "10.60.0.1@wlan0,10.60.0.1@wwan0", wantErr: "reuses bind address"},
		{name: "duplicate device", raw: "10.60.0.1@wlan0,10.61.0.1@wlan0", wantErr: "reuses pinned device"},
		{name: "control in device", raw: "10.60.0.1@wlan\n0", wantErr: "control characters"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseLocalPathSpecs(tc.raw)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("parseLocalPathSpecs(%q) error = %v, want substring %q", tc.raw, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseLocalPathSpecs(%q): %v", tc.raw, err)
			}
			if len(got) != tc.wantLen {
				t.Fatalf("parseLocalPathSpecs(%q) len = %d, want %d", tc.raw, len(got), tc.wantLen)
			}
		})
	}
}

func TestParseLocalPathSpecsCanonicalizesIP(t *testing.T) {
	got, err := parseLocalPathSpecs("2001:0db8:0:0:0:0:0:1@Ethernet 2")
	if err != nil {
		t.Fatal(err)
	}
	if got[0].LocalAddr != "2001:db8::1" {
		t.Fatalf("canonical LocalAddr = %q, want 2001:db8::1", got[0].LocalAddr)
	}
	if got[0].Device != "Ethernet 2" {
		t.Fatalf("device = %q, want Ethernet 2", got[0].Device)
	}
}

func TestParseLocalPathSpecsCapsPathCount(t *testing.T) {
	parts := make([]string, maxDesktopPathSpecs+1)
	for i := range parts {
		parts[i] = "10.0.0.1"
	}
	_, err := parseLocalPathSpecs(strings.Join(parts, ","))
	if err == nil || !strings.Contains(err.Error(), "too many local paths") {
		t.Fatalf("error = %v, want path-count rejection", err)
	}
}
