package settings

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

const (
	SchemaVersion       = 1
	MaxPreferredPaths   = 16
	MaxInterfaceIDRunes = 64
	MaxActivePaths      = 8
	MaxFECPercent       = 50
)

type Mode string

const (
	ModeSpeed     Mode = "SPEED"
	ModeRedundant Mode = "REDUNDANT"
	ModeStream    Mode = "STREAM"
	ModeCustom    Mode = "CUSTOM"
)

type Config struct {
	Schema              int
	Mode                Mode
	AllowMetered        bool
	PreferredInterfaces []string
	ActivePaths         int
	FECPercent          int
}

func Admit(in Config) (Config, error) {
	if in.Schema != SchemaVersion {
		return Config{}, fmt.Errorf("unsupported settings schema %d", in.Schema)
	}
	if !validMode(in.Mode) {
		return Config{}, fmt.Errorf("unsupported bond mode %q", in.Mode)
	}
	if in.ActivePaths < 1 || in.ActivePaths > MaxActivePaths {
		return Config{}, fmt.Errorf("active paths must be between 1 and %d", MaxActivePaths)
	}
	if in.FECPercent < 0 || in.FECPercent > MaxFECPercent {
		return Config{}, fmt.Errorf("fec percent must be between 0 and %d", MaxFECPercent)
	}
	if in.Mode != ModeCustom && in.FECPercent != 0 {
		return Config{}, errors.New("fec percent is only configurable in CUSTOM mode")
	}
	if len(in.PreferredInterfaces) > MaxPreferredPaths {
		return Config{}, fmt.Errorf("preferred interface count exceeds %d", MaxPreferredPaths)
	}

	seen := make(map[string]struct{}, len(in.PreferredInterfaces))
	preferred := make([]string, 0, len(in.PreferredInterfaces))
	for _, raw := range in.PreferredInterfaces {
		id := strings.TrimSpace(raw)
		if err := validateInterfaceID(id); err != nil {
			return Config{}, err
		}
		key := strings.ToLower(id)
		if _, ok := seen[key]; ok {
			return Config{}, fmt.Errorf("duplicate preferred interface %q", id)
		}
		seen[key] = struct{}{}
		preferred = append(preferred, id)
	}
	sort.Slice(preferred, func(i, j int) bool {
		return strings.ToLower(preferred[i]) < strings.ToLower(preferred[j])
	})

	return Config{
		Schema:              SchemaVersion,
		Mode:                in.Mode,
		AllowMetered:        in.AllowMetered,
		PreferredInterfaces: preferred,
		ActivePaths:         in.ActivePaths,
		FECPercent:          in.FECPercent,
	}, nil
}

func validMode(mode Mode) bool {
	switch mode {
	case ModeSpeed, ModeRedundant, ModeStream, ModeCustom:
		return true
	default:
		return false
	}
}

func validateInterfaceID(id string) error {
	if id == "" {
		return errors.New("preferred interface id must not be blank")
	}
	if len([]rune(id)) > MaxInterfaceIDRunes {
		return fmt.Errorf("preferred interface id exceeds %d runes", MaxInterfaceIDRunes)
	}
	for _, r := range id {
		if unicode.IsControl(r) {
			return errors.New("preferred interface id contains control characters")
		}
	}
	return nil
}
