package settings

import "testing"

func TestAdmitRuntimeNormalizesBaseAndPreservesBoundedOptions(t *testing.T) {
	got, err := AdmitRuntime(Config{
		SchemaVersion: SchemaVersion,
		Mode:          ModeSpeed,
		Interfaces: []InterfacePreference{
			{ID: " wifi ", Enabled: true},
			{ID: "ethernet", Enabled: true},
		},
	}, RuntimeOptions{AllowMetered: true, MaxActivePaths: 2})
	if err != nil {
		t.Fatalf("AdmitRuntime() error = %v", err)
	}
	if got.Runtime.MaxActivePaths != 2 || !got.Runtime.AllowMetered {
		t.Fatalf("unexpected runtime options: %+v", got.Runtime)
	}
	if got.Base.Interfaces[0].ID != "ethernet" || got.Base.Interfaces[1].ID != "wifi" {
		t.Fatalf("base config was not normalized: %+v", got.Base.Interfaces)
	}
}

func TestAdmitRuntimeRejectsZeroActivePaths(t *testing.T) {
	_, err := AdmitRuntime(validRuntimeBase(), RuntimeOptions{MaxActivePaths: 0})
	if err == nil {
		t.Fatal("expected zero active paths to fail closed")
	}
}

func TestAdmitRuntimeRejectsMorePathsThanEnabled(t *testing.T) {
	_, err := AdmitRuntime(validRuntimeBase(), RuntimeOptions{MaxActivePaths: 2})
	if err == nil {
		t.Fatal("expected enabled-interface bound failure")
	}
}

func TestAdmitRuntimeRejectsInvalidBaseBeforeRuntimeOptions(t *testing.T) {
	base := validRuntimeBase()
	base.SchemaVersion++
	_, err := AdmitRuntime(base, RuntimeOptions{AllowMetered: true, MaxActivePaths: 1})
	if err == nil {
		t.Fatal("expected base schema drift to fail closed")
	}
}

func TestAdmitRuntimeDoesNotReenableDisabledInterfaces(t *testing.T) {
	base := Config{
		SchemaVersion: SchemaVersion,
		Mode:          ModeRedundant,
		Interfaces: []InterfacePreference{
			{ID: "wifi", Enabled: true},
			{ID: "cellular", Enabled: false},
		},
	}
	got, err := AdmitRuntime(base, RuntimeOptions{AllowMetered: true, MaxActivePaths: 1})
	if err != nil {
		t.Fatalf("AdmitRuntime() error = %v", err)
	}
	for _, pref := range got.Base.Interfaces {
		if pref.ID == "cellular" && pref.Enabled {
			t.Fatal("runtime admission unexpectedly re-enabled disabled interface")
		}
	}
}

func validRuntimeBase() Config {
	return Config{
		SchemaVersion: SchemaVersion,
		Mode:          ModeSpeed,
		Interfaces:    []InterfacePreference{{ID: "wifi", Enabled: true}},
	}
}
