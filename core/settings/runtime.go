package settings

import "fmt"

// RuntimeOptions contains cross-platform settings that affect path admission but do
// not identify endpoints, addresses, credentials, SSIDs, or other sensitive state.
type RuntimeOptions struct {
	AllowMetered   bool `json:"allow_metered"`
	MaxActivePaths int  `json:"max_active_paths"`
}

// EffectiveConfig is the fully admitted settings payload Android and Windows may
// hand to their runtime adapters. Base is normalized through the existing Config
// contract before runtime options are considered.
type EffectiveConfig struct {
	Base    Config         `json:"base"`
	Runtime RuntimeOptions `json:"runtime"`
}

// AdmitRuntime fails closed if a platform requests more simultaneously active paths
// than the normalized settings actually enable. This keeps UI/platform preferences
// from expanding the eligible path set behind the core settings contract.
func AdmitRuntime(base Config, runtime RuntimeOptions) (EffectiveConfig, error) {
	normalized, err := Normalize(base)
	if err != nil {
		return EffectiveConfig{}, err
	}

	enabled := 0
	for _, pref := range normalized.Interfaces {
		if pref.Enabled {
			enabled++
		}
	}
	if runtime.MaxActivePaths < 1 {
		return EffectiveConfig{}, fmt.Errorf("settings: max active paths must be positive")
	}
	if runtime.MaxActivePaths > enabled {
		return EffectiveConfig{}, fmt.Errorf("settings: max active paths %d exceeds %d enabled interfaces", runtime.MaxActivePaths, enabled)
	}

	return EffectiveConfig{
		Base: normalized,
		Runtime: RuntimeOptions{
			AllowMetered:   runtime.AllowMetered,
			MaxActivePaths: runtime.MaxActivePaths,
		},
	}, nil
}
