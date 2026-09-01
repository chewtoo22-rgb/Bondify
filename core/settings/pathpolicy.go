package settings

import "github.com/chewtoo22-rgb/bondify/core/pathpolicy"

// AdmissionPolicy converts an already admitted runtime configuration into the
// canonical path-admission policy consumed by Android and Windows networking
// adapters. Only explicitly enabled interface IDs are exported, so a platform
// caller cannot silently widen the candidate set after settings validation.
func AdmissionPolicy(effective EffectiveConfig) pathpolicy.Policy {
	enabled := make([]string, 0, len(effective.Base.Interfaces))
	for _, pref := range effective.Base.Interfaces {
		if pref.Enabled {
			enabled = append(enabled, pref.ID)
		}
	}

	return pathpolicy.Policy{
		ExplicitIDs:    enabled,
		AllowMetered:   effective.Runtime.AllowMetered,
		MaxActivePaths: effective.Runtime.MaxActivePaths,
	}
}

// AdmitPathPolicy performs settings admission and policy construction as one
// operation. Platform integrations should prefer this helper over separately
// validating settings and assembling pathpolicy.Policy by hand.
func AdmitPathPolicy(base Config, runtime RuntimeOptions) (EffectiveConfig, pathpolicy.Policy, error) {
	effective, err := AdmitRuntime(base, runtime)
	if err != nil {
		return EffectiveConfig{}, pathpolicy.Policy{}, err
	}
	return effective, AdmissionPolicy(effective), nil
}
