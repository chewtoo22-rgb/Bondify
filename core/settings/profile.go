package settings

import (
	"errors"
	"fmt"
	"strings"
)

const (
	MaxRelayHostBytes = 253
	MinMTU            = 1280
	MaxMTU            = 9000
	MinKeepaliveSec   = 5
	MaxKeepaliveSec   = 120
)

var ErrInvalidProfile = errors.New("invalid settings profile")

type Profile struct {
	SchemaVersion int
	Mode          Mode
	RelayHost     string
	MTU           int
	KeepaliveSec  int
	AutoReconnect bool
}

// Admit validates and normalizes settings shared by Android and Windows before
// platform code is allowed to apply them to the networking core. It deliberately
// reuses the canonical Mode and SchemaVersion contract from config.go so platform
// settings cannot drift from the modes the core actually implements.
func Admit(in Profile) (Profile, error) {
	if in.SchemaVersion != SchemaVersion {
		return Profile{}, invalid("schema_version")
	}
	if !validProfileMode(in.Mode) {
		return Profile{}, invalid("mode")
	}

	host := strings.TrimSpace(in.RelayHost)
	if host == "" || len(host) > MaxRelayHostBytes || hasControl(host) {
		return Profile{}, invalid("relay_host")
	}
	if strings.ContainsAny(host, "/\\@?#") {
		return Profile{}, invalid("relay_host")
	}
	if in.MTU < MinMTU || in.MTU > MaxMTU {
		return Profile{}, invalid("mtu")
	}
	if in.KeepaliveSec < MinKeepaliveSec || in.KeepaliveSec > MaxKeepaliveSec {
		return Profile{}, invalid("keepalive_sec")
	}

	return Profile{
		SchemaVersion: SchemaVersion,
		Mode:          in.Mode,
		RelayHost:     strings.ToLower(host),
		MTU:           in.MTU,
		KeepaliveSec:  in.KeepaliveSec,
		AutoReconnect: in.AutoReconnect,
	}, nil
}

func validProfileMode(mode Mode) bool {
	switch mode {
	case ModeSpeed, ModeRedundant:
		return true
	case ModeStream, ModeCustom:
		// Reserved in the canonical settings contract until the corresponding
		// networking behavior exists. Never admit them optimistically.
		return false
	default:
		return false
	}
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func invalid(field string) error {
	return fmt.Errorf("%w: %s", ErrInvalidProfile, field)
}
