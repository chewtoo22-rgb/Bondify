package nat

import "testing"

func TestValidateInputs(t *testing.T) {
	valid := []struct {
		cidr string
		iface string
	}{
		{"100.64.0.0/10", "eth0"},
		{"fd00::/64", "wlan0"},
	}
	for _, tc := range valid {
		if err := validateInputs(tc.cidr, tc.iface); err != nil {
			t.Fatalf("expected valid input %q/%q: %v", tc.cidr, tc.iface, err)
		}
	}

	invalid := []struct {
		name  string
		cidr  string
		iface string
	}{
		{"malformed CIDR", "100.64.0.0", "eth0"},
		{"shell metacharacter", "100.64.0.0/10", "eth0;touch"},
		{"path separator", "100.64.0.0/10", "../eth0"},
		{"overlong interface", "100.64.0.0/10", "1234567890123456"},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateInputs(tc.cidr, tc.iface); err == nil {
				t.Fatalf("expected rejection for %q/%q", tc.cidr, tc.iface)
			}
		})
	}
}
