package settings

import (
	"strings"
	"testing"
)

func TestAdmitNormalizesDeterministically(t *testing.T) {
	got, err := Admit(Config{
		Schema:              SchemaVersion,
		Mode:                ModeCustom,
		AllowMetered:        true,
		PreferredInterfaces: []string{" wlan0 ", "ETH0"},
		ActivePaths:         2,
		FECPercent:          20,
	})
	if err != nil {
		t.Fatalf("Admit() error = %v", err)
	}
	if got.Schema != SchemaVersion || got.Mode != ModeCustom || !got.AllowMetered || got.ActivePaths != 2 || got.FECPercent != 20 {
		t.Fatalf("unexpected admitted config: %+v", got)
	}
	if len(got.PreferredInterfaces) != 2 || got.PreferredInterfaces[0] != "ETH0" || got.PreferredInterfaces[1] != "wlan0" {
		t.Fatalf("preferred interfaces not normalized deterministically: %#v", got.PreferredInterfaces)
	}
}

func TestAdmitRejectsUnknownSchema(t *testing.T) {
	assertRejected(t, Config{Schema: 2, Mode: ModeSpeed, ActivePaths: 1})
}

func TestAdmitRejectsUnknownMode(t *testing.T) {
	assertRejected(t, Config{Schema: SchemaVersion, Mode: Mode("TURBO"), ActivePaths: 1})
}

func TestAdmitRejectsInvalidActivePathBounds(t *testing.T) {
	assertRejected(t, Config{Schema: SchemaVersion, Mode: ModeSpeed, ActivePaths: 0})
	assertRejected(t, Config{Schema: SchemaVersion, Mode: ModeSpeed, ActivePaths: MaxActivePaths + 1})
}

func TestAdmitRejectsFECOutsideCustomMode(t *testing.T) {
	assertRejected(t, Config{Schema: SchemaVersion, Mode: ModeStream, ActivePaths: 2, FECPercent: 10})
}

func TestAdmitRejectsFECBounds(t *testing.T) {
	assertRejected(t, Config{Schema: SchemaVersion, Mode: ModeCustom, ActivePaths: 2, FECPercent: -1})
	assertRejected(t, Config{Schema: SchemaVersion, Mode: ModeCustom, ActivePaths: 2, FECPercent: MaxFECPercent + 1})
}

func TestAdmitRejectsDuplicateInterfacesAfterNormalization(t *testing.T) {
	assertRejected(t, Config{
		Schema: SchemaVersion, Mode: ModeSpeed, ActivePaths: 2,
		PreferredInterfaces: []string{"WiFi", " wifi "},
	})
}

func TestAdmitRejectsMalformedInterfaceIDs(t *testing.T) {
	assertRejected(t, Config{Schema: SchemaVersion, Mode: ModeSpeed, ActivePaths: 1, PreferredInterfaces: []string{"\n"}})
	assertRejected(t, Config{Schema: SchemaVersion, Mode: ModeSpeed, ActivePaths: 1, PreferredInterfaces: []string{"eth0\u0000x"}})
	assertRejected(t, Config{Schema: SchemaVersion, Mode: ModeSpeed, ActivePaths: 1, PreferredInterfaces: []string{strings.Repeat("x", MaxInterfaceIDRunes+1)}})
}

func TestAdmitRejectsTooManyPreferredInterfaces(t *testing.T) {
	ids := make([]string, MaxPreferredPaths+1)
	for i := range ids {
		ids[i] = "if" + strings.Repeat("x", i+1)
	}
	assertRejected(t, Config{Schema: SchemaVersion, Mode: ModeSpeed, ActivePaths: 1, PreferredInterfaces: ids})
}

func assertRejected(t *testing.T, cfg Config) {
	t.Helper()
	if _, err := Admit(cfg); err == nil {
		t.Fatalf("Admit(%+v) unexpectedly succeeded", cfg)
	}
}
