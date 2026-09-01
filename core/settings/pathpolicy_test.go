package settings

import (
	"reflect"
	"testing"

	"github.com/chewtoo22-rgb/Bondify/core/pathpolicy"
)

func TestAdmitPathPolicyCarriesOnlyEnabledInterfaces(t *testing.T) {
	base := Config{
		SchemaVersion: SchemaVersion,
		Mode:          ModeSpeed,
		Interfaces: []InterfacePreference{
			{ID: " wifi ", Enabled: true},
			{ID: "cell", Enabled: false},
			{ID: "ethernet", Enabled: true},
		},
	}
	runtime := RuntimeOptions{AllowMetered: true, MaxActivePaths: 2}

	effective, policy, err := AdmitPathPolicy(base, runtime)
	if err != nil {
		t.Fatalf("AdmitPathPolicy() error = %v", err)
	}
	if got, want := policy.ExplicitIDs, []string{"ethernet", "wifi"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ExplicitIDs = %v, want %v", got, want)
	}
	if !policy.AllowMetered {
		t.Fatal("AllowMetered = false, want true")
	}
	if policy.MaxActivePaths != 2 {
		t.Fatalf("MaxActivePaths = %d, want 2", policy.MaxActivePaths)
	}
	if effective.Base.Interfaces[0].ID != "cell" {
		t.Fatalf("effective config was not normalized: %+v", effective.Base.Interfaces)
	}
}

func TestAdmitPathPolicyFeedsCoreAdmissionWithoutWidening(t *testing.T) {
	base := Config{
		SchemaVersion: SchemaVersion,
		Mode:          ModeSpeed,
		Interfaces: []InterfacePreference{
			{ID: "wifi", Enabled: true},
			{ID: "cell", Enabled: false},
			{ID: "ethernet", Enabled: true},
		},
	}
	_, policy, err := AdmitPathPolicy(base, RuntimeOptions{AllowMetered: false, MaxActivePaths: 1})
	if err != nil {
		t.Fatalf("AdmitPathPolicy() error = %v", err)
	}

	got, err := pathpolicy.Admit([]pathpolicy.ObservedInterface{
		{ID: "wifi", Up: true, HasRoute: true},
		{ID: "cell", Up: true, HasRoute: true},
		{ID: "ethernet", Up: true, HasRoute: true},
	}, policy)
	if err != nil {
		t.Fatalf("pathpolicy.Admit() error = %v", err)
	}
	want := []string{"ethernet"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("eligible paths = %v, want %v", got, want)
	}
}

func TestAdmitPathPolicyRejectsImpossibleRuntimeBeforePolicyCreation(t *testing.T) {
	base := Config{
		SchemaVersion: SchemaVersion,
		Mode:          ModeSpeed,
		Interfaces: []InterfacePreference{
			{ID: "wifi", Enabled: true},
			{ID: "cell", Enabled: false},
		},
	}

	effective, policy, err := AdmitPathPolicy(base, RuntimeOptions{MaxActivePaths: 2})
	if err == nil {
		t.Fatal("AdmitPathPolicy() error = nil, want impossible active-path limit rejection")
	}
	if !reflect.DeepEqual(effective, EffectiveConfig{}) {
		t.Fatalf("effective = %+v, want zero value on failure", effective)
	}
	if !reflect.DeepEqual(policy, pathpolicy.Policy{}) {
		t.Fatalf("policy = %+v, want zero value on failure", policy)
	}
}
