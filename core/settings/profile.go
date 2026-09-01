package settings

import (
	"errors"
	"fmt"
	"strings"
)

const (
	SchemaVersion     = 1
	MaxRelayHostBytes = 253
	MinMTU            = 1280
	MaxMTU            = 9000
	MinKeepaliveSec   = 5
	MaxKeepaliveSec   = 120
)

var ErrInvalidProfile = errors.New("invalid settings profile")

type Mode string

const (
	ModeSpeed     Mode = "speed"
	ModeRedundant Mode = "redundant"
	ModeStream    Mode = "stream"
	ModeCustom    Mode = "custom"
)

type Profile struct {
	SchemaVersion int
	Mode          Mode
	RelayHost     string
	MTU           int
	KeepaliveSec  int
	AutoReconnect bool
}

// Admit validates and normalizes settings shared by Android and Windows before
// platform code is allowed to apply them to the networking core.
func Admit(in Profile) (Profile, error) {
	if in.SchemaVersion != SchemaVersion {
		return Profile{}, invalid("schema_version")
	}
	if !validMode(in.Mode) {
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

func validMode(mode Mode) bool {
	switch mode {
	case ModeSpeed, ModeRedundant, ModeStream, ModeCustom:
		return true
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
