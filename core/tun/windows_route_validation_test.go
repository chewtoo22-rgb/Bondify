//go:build windows

package tun

import "testing"

func TestCanonicalIPv4Prefix(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "canonical host bits", input: "192.0.2.99/24", want: "192.0.2.0/24"},
		{name: "host route", input: "198.51.100.7/32", want: "198.51.100.7/32"},
		{name: "default", input: "0.0.0.0/0", want: "0.0.0.0/0"},
		{name: "ipv6 rejected", input: "2001:db8::/64", wantErr: true},
		{name: "blank rejected", input: "", wantErr: true},
		{name: "powershell separator rejected", input: "10.0.0.0/8; Remove-Item C:\\*", wantErr: true},
		{name: "quote injection rejected", input: "10.0.0.0/8' -Confirm:$false; Write-Output pwned", wantErr: true},
		{name: "trailing garbage rejected", input: "10.0.0.0/8 garbage", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := canonicalIPv4Prefix(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("canonicalIPv4Prefix(%q) unexpectedly succeeded: %q", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("canonicalIPv4Prefix(%q): %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("canonicalIPv4Prefix(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestDelRouteRejectsMalformedInputBeforePowerShell(t *testing.T) {
	// If validation ever regresses, these inputs would reach the PowerShell command text.
	// DelRoute must reject them before attempting any process execution.
	inputs := []string{
		"not-a-prefix",
		"10.0.0.0/8; Write-Output injected",
		"10.0.0.0/8' | Remove-Item C:\\*",
		"2001:db8::/64",
	}
	for _, input := range inputs {
		if err := DelRoute(input); err == nil {
			t.Fatalf("DelRoute(%q) unexpectedly succeeded", input)
		}
	}
}
