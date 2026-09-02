package settings

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

const (
	SchemaVersion       = 1
	MaxInterfaces       = 8
	MaxInterfaceIDRunes = 64
)

// Mode is the user-facing connection policy. STREAM and CUSTOM remain reserved until
// the corresponding core modes are implemented; validation rejects them rather than
// silently mapping them to SPEED.
type Mode string

const (
	ModeSpeed     Mode = "speed"
	ModeRedundant Mode = "redundant"
	ModeStream    Mode = "stream"
	ModeCustom    Mode = "custom"
)

// InterfacePreference is a stable, platform-provided interface selector plus whether
// the user wants Bondify to consider it for a session. It deliberately carries no IP,
// endpoint, SSID, token, or other network-secret material.
type InterfacePreference struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
}

// Config is the portable settings contract consumed by Android/Windows integration.
type Config struct {
	SchemaVersion int                   `json:"schema_version"`
	Mode          Mode                  `json:"mode"`
	Interfaces    []InterfacePreference `json:"interfaces"`
}

// Normalize validates and canonicalizes a config. It fails closed on unsupported
// schema/modes, malformed IDs, duplicate selectors, excessive interfaces, or a config
// that disables every interface. Interface order is canonicalized so persistence and
// diagnostics are deterministic across platforms.
func Normalize(in Config) (Config, error) {
	if in.SchemaVersion != SchemaVersion {
		return Config{}, fmt.Errorf("settings: unsupported schema version %d", in.SchemaVersion)
	}

	switch in.Mode {
	case ModeSpeed, ModeRedundant:
		// implemented modes
	case ModeStream, ModeCustom:
		return Config{}, fmt.Errorf("settings: mode %q is reserved but not implemented", in.Mode)
	default:
		return Config{}, fmt.Errorf("settings: unknown mode %q", in.Mode)
	}

	if len(in.Interfaces) == 0 {
		return Config{}, fmt.Errorf("settings: at least one interface is required")
	}
	if len(in.Interfaces) > MaxInterfaces {
		return Config{}, fmt.Errorf("settings: interface count %d exceeds %d", len(in.Interfaces), MaxInterfaces)
	}

	out := Config{SchemaVersion: SchemaVersion, Mode: in.Mode, Interfaces: make([]InterfacePreference, 0, len(in.Interfaces))}
	seen := make(map[string]struct{}, len(in.Interfaces))
	enabled := 0
	for _, pref := range in.Interfaces {
		id, err := normalizeInterfaceID(pref.ID)
		if err != nil {
			return Config{}, err
		}
		if _, ok := seen[id]; ok {
			return Config{}, fmt.Errorf("settings: duplicate interface %q", id)
		}
		seen[id] = struct{}{}
		if pref.Enabled {
			enabled++
		}
		out.Interfaces = append(out.Interfaces, InterfacePreference{ID: id, Enabled: pref.Enabled})
	}
	if enabled == 0 {
		return Config{}, fmt.Errorf("settings: at least one interface must be enabled")
	}

	sort.Slice(out.Interfaces, func(i, j int) bool { return out.Interfaces[i].ID < out.Interfaces[j].ID })
	return out, nil
}

func normalizeInterfaceID(raw string) (string, error) {
	id := strings.TrimSpace(raw)
	if id == "" {
		return "", fmt.Errorf("settings: interface ID is empty")
	}
	if n := len([]rune(id)); n > MaxInterfaceIDRunes {
		return "", fmt.Errorf("settings: interface ID exceeds %d runes", MaxInterfaceIDRunes)
	}
	for _, r := range id {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("settings: interface ID contains control characters")
		}
		if r == '/' || r == '\\' {
			return "", fmt.Errorf("settings: interface ID contains path separators")
		}
	}
	if id == "." || id == ".." {
		return "", fmt.Errorf("settings: interface ID is reserved")
	}
	return id, nil
}
