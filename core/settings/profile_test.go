package settings

import (
	"errors"
	"strings"
	"testing"
)

func validProfile() Profile {
	return Profile{SchemaVersion: SchemaVersion, Mode: ModeSpeed, RelayHost: " Relay.Example.COM ", MTU: 1400, KeepaliveSec: 20, AutoReconnect: true}
}

func TestAdmitNormalizesHost(t *testing.T) {
	got, err := Admit(validProfile())
	if err != nil { t.Fatal(err) }
	if got.RelayHost != "relay.example.com" { t.Fatalf("host=%q", got.RelayHost) }
	if got.Mode != ModeSpeed || !got.AutoReconnect { t.Fatalf("unexpected admitted profile: %+v", got) }
}

func TestAdmitAllModes(t *testing.T) {
	for _, mode := range []Mode{ModeSpeed, ModeRedundant, ModeStream, ModeCustom} {
		p := validProfile(); p.Mode = mode
		if _, err := Admit(p); err != nil { t.Fatalf("mode %q: %v", mode, err) }
	}
}

func TestAdmitRejectsSchemaDrift(t *testing.T) {
	p := validProfile(); p.SchemaVersion++
	assertInvalid(t, p)
}

func TestAdmitRejectsUnknownMode(t *testing.T) {
	p := validProfile(); p.Mode = "turbo-ish"
	assertInvalid(t, p)
}

func TestAdmitRejectsUnsafeRelayHosts(t *testing.T) {
	for _, host := range []string{"", "relay.example.com/path", "user@relay", "relay?x=1", "relay#frag", "relay\nname", strings.Repeat("a", MaxRelayHostBytes+1)} {
		p := validProfile(); p.RelayHost = host
		assertInvalid(t, p)
	}
}

func TestAdmitRejectsMTUOutsideBounds(t *testing.T) {
	for _, mtu := range []int{MinMTU - 1, MaxMTU + 1} {
		p := validProfile(); p.MTU = mtu
		assertInvalid(t, p)
	}
}

func TestAdmitAcceptsMTUBoundaries(t *testing.T) {
	for _, mtu := range []int{MinMTU, MaxMTU} {
		p := validProfile(); p.MTU = mtu
		if _, err := Admit(p); err != nil { t.Fatalf("mtu %d: %v", mtu, err) }
	}
}

func TestAdmitRejectsKeepaliveOutsideBounds(t *testing.T) {
	for _, secs := range []int{MinKeepaliveSec - 1, MaxKeepaliveSec + 1} {
		p := validProfile(); p.KeepaliveSec = secs
		assertInvalid(t, p)
	}
}

func TestAdmitAcceptsKeepaliveBoundaries(t *testing.T) {
	for _, secs := range []int{MinKeepaliveSec, MaxKeepaliveSec} {
		p := validProfile(); p.KeepaliveSec = secs
		if _, err := Admit(p); err != nil { t.Fatalf("keepalive %d: %v", secs, err) }
	}
}

func assertInvalid(t *testing.T, p Profile) {
	t.Helper()
	if _, err := Admit(p); !errors.Is(err, ErrInvalidProfile) { t.Fatalf("expected ErrInvalidProfile, got %v", err) }
}
