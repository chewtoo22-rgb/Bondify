package mobile

import (
	"strings"
	"testing"
)

func TestValidatePathLabelAcceptsExpectedNetworkLabels(t *testing.T) {
	for _, label := range []string{"wifi", "cellular", "usb-ethernet", "Ethernet 2"} {
		if err := validatePathLabel(label); err != nil {
			t.Fatalf("validatePathLabel(%q) = %v; want nil", label, err)
		}
	}
}

func TestValidatePathLabelRejectsAmbiguousOrHostileLabels(t *testing.T) {
	cases := []string{
		"",
		" wifi",
		"wifi ",
		"wifi\ncellular",
		"wifi\x00cellular",
		strings.Repeat("x", maxPathLabelBytes+1),
		string([]byte{0xff, 0xfe}),
	}
	for _, label := range cases {
		if err := validatePathLabel(label); err == nil {
			t.Fatalf("validatePathLabel(%q) unexpectedly succeeded", label)
		}
	}
}

func TestPathLabelExistsUsesExactIdentity(t *testing.T) {
	labels := []string{"wifi", "cellular"}
	if !pathLabelExists(labels, "wifi") {
		t.Fatal("existing wifi label was not found")
	}
	if pathLabelExists(labels, "WiFi") {
		t.Fatal("label identity unexpectedly became case-insensitive")
	}
}
