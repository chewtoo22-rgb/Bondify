package settings

import (
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeCanonicalizesInterfaces(t *testing.T) {
	got, err := Normalize(Config{
		SchemaVersion: SchemaVersion,
		Mode:          ModeSpeed,
		Interfaces: []InterfacePreference{
			{ID: " wifi-primary ", Enabled: true},
			{ID: "cellular", Enabled: false},
		},
	})
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	want := Config{
		SchemaVersion: SchemaVersion,
		Mode:          ModeSpeed,
		Interfaces: []InterfacePreference{
			{ID: "cellular", Enabled: false},
			{ID: "wifi-primary", Enabled: true},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Normalize() = %#v, want %#v", got, want)
	}
}

func TestNormalizeAcceptsImplementedModes(t *testing.T) {
	for _, mode := range []Mode{ModeSpeed, ModeRedundant} {
		t.Run(string(mode), func(t *testing.T) {
			_, err := Normalize(Config{SchemaVersion: SchemaVersion, Mode: mode, Interfaces: []InterfacePreference{{ID: "wan0", Enabled: true}}})
			if err != nil {
				t.Fatalf("Normalize() error = %v", err)
			}
		})
	}
}

func TestNormalizeRejectsReservedAndUnknownModes(t *testing.T) {
	for _, mode := range []Mode{ModeStream, ModeCustom, Mode("warp")} {
		t.Run(string(mode), func(t *testing.T) {
			_, err := Normalize(Config{SchemaVersion: SchemaVersion, Mode: mode, Interfaces: []InterfacePreference{{ID: "wan0", Enabled: true}}})
			if err == nil {
				t.Fatal("Normalize() expected error")
			}
		})
	}
}

func TestNormalizeRejectsDuplicateAfterTrimming(t *testing.T) {
	_, err := Normalize(Config{
		SchemaVersion: SchemaVersion,
		Mode:          ModeSpeed,
		Interfaces: []InterfacePreference{
			{ID: "wifi", Enabled: true},
			{ID: " wifi ", Enabled: true},
		},
	})
	if err == nil {
		t.Fatal("Normalize() expected duplicate error")
	}
}

func TestNormalizeRejectsAllDisabled(t *testing.T) {
	_, err := Normalize(Config{SchemaVersion: SchemaVersion, Mode: ModeSpeed, Interfaces: []InterfacePreference{{ID: "wifi", Enabled: false}}})
	if err == nil {
		t.Fatal("Normalize() expected all-disabled error")
	}
}

func TestNormalizeRejectsMalformedIDs(t *testing.T) {
	cases := []string{"", "   ", "wifi\nsecret", strings.Repeat("x", MaxInterfaceIDRunes+1)}
	for _, id := range cases {
		_, err := Normalize(Config{SchemaVersion: SchemaVersion, Mode: ModeSpeed, Interfaces: []InterfacePreference{{ID: id, Enabled: true}}})
		if err == nil {
			t.Fatalf("Normalize(%q) expected error", id)
		}
	}
}

func TestNormalizeRejectsTooManyInterfaces(t *testing.T) {
	prefs := make([]InterfacePreference, MaxInterfaces+1)
	for i := range prefs {
		prefs[i] = InterfacePreference{ID: string(rune('a' + i)), Enabled: true}
	}
	_, err := Normalize(Config{SchemaVersion: SchemaVersion, Mode: ModeSpeed, Interfaces: prefs})
	if err == nil {
		t.Fatal("Normalize() expected interface-count error")
	}
}

func TestNormalizeRejectsSchemaDrift(t *testing.T) {
	_, err := Normalize(Config{SchemaVersion: SchemaVersion + 1, Mode: ModeSpeed, Interfaces: []InterfacePreference{{ID: "wan0", Enabled: true}}})
	if err == nil {
		t.Fatal("Normalize() expected schema error")
	}
}
