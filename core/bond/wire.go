// Package bond ties core/proto, core/crypto, and core/tun together into the session/path
// engine described in PROTOCOL.md §5. Phase 1 implements a single always-path-0 tunnel;
// the GSN/PSN counters and per-path structure are already in place so phase 2 (multipath,
// state machine, reordering) extends this file set rather than rewriting it.
package bond

import (
	"fmt"

	"github.com/fxamacker/cbor/v2"
)

// wireDecMode is deliberately stricter than fxamacker/cbor's permissive defaults for
// attacker-controlled protocol payloads. Duplicate map keys are ambiguous (the default
// decoder may keep either the first or last value depending on destination type), while
// indefinite-length items and semantic tags are not part of BOND/1's fixed control/config
// schema. Rejecting all three keeps authenticated wire parsing deterministic and fail-closed.
var wireDecMode = func() cbor.DecMode {
	dm, err := cbor.DecOptions{
		DupMapKey:  cbor.DupMapKeyEnforcedAPF,
		IndefLength: cbor.IndefLengthForbidden,
		TagsMd:     cbor.TagsForbidden,
	}.DecMode()
	if err != nil {
		panic(fmt.Sprintf("bond: create strict CBOR decoder: %v", err))
	}
	return dm
}()

// HandshakeRespPayload is carried as the encrypted payload of the Noise IK response
// message (HANDSHAKE_RESP) — see PROTOCOL.md §5's establishment diagram: "HANDSHAKE_RESP
// (session idx, cfg)". Bundling cfg_push into the handshake response itself (rather than a
// separate CTRL round trip) saves a full RTT before the tunnel is usable.
type HandshakeRespPayload struct {
	SessionIndex uint32   `cbor:"si"`
	TunnelIP     string   `cbor:"ip"`     // client's assigned tunnel IP, e.g. "10.77.0.2"
	Prefix       int      `cbor:"prefix"` // tunnel subnet prefix length, e.g. 24
	GatewayIP    string   `cbor:"gw"`     // relay's own tunnel IP, e.g. "10.77.0.1"
	DNS          []string `cbor:"dns"`
	MTU          int      `cbor:"mtu"`
	KeepaliveSec int      `cbor:"ka"`
}

func (p HandshakeRespPayload) Marshal() ([]byte, error) {
	b, err := cbor.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("bond: marshal cfg_push: %w", err)
	}
	return b, nil
}

func UnmarshalHandshakeResp(b []byte) (HandshakeRespPayload, error) {
	var p HandshakeRespPayload
	if err := wireDecMode.Unmarshal(b, &p); err != nil {
		return p, fmt.Errorf("bond: unmarshal cfg_push: %w", err)
	}
	return p, nil
}
