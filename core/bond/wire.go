// Package bond ties core/proto, core/crypto, and core/tun together into the session/path
// engine described in PROTOCOL.md §5. Phase 1 implements a single always-path-0 tunnel;
// the GSN/PSN counters and per-path structure are already in place so phase 2 (multipath,
// state machine, reordering) extends this file set rather than rewriting it.
package bond

import (
	"fmt"
	"net"

	"github.com/fxamacker/cbor/v2"
)

// wireDecMode is deliberately stricter than fxamacker/cbor's permissive defaults for
// attacker-controlled protocol payloads. Duplicate map keys are ambiguous (the default
// decoder may keep either the first or last value depending on destination type), while
// indefinite-length items and semantic tags are not part of BOND/1's fixed control/config
// schema. Rejecting all three keeps authenticated wire parsing deterministic and fail-closed.
var wireDecMode = func() cbor.DecMode {
	dm, err := cbor.DecOptions{
		DupMapKey:   cbor.DupMapKeyEnforcedAPF,
		IndefLength: cbor.IndefLengthForbidden,
		TagsMd:      cbor.TagsForbidden,
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
	if err := validateHandshakeResp(p); err != nil {
		return p, fmt.Errorf("bond: invalid cfg_push: %w", err)
	}
	return p, nil
}

// validateHandshakeResp treats the authenticated relay response as untrusted configuration.
// Noise authenticates who sent these values; it does not make malformed or accidentally
// misconfigured values safe to feed into TUN setup and buffer sizing. Keep validation here,
// at the wire boundary, so every caller fails closed before constructing a ClientTunnel.
func validateHandshakeResp(p HandshakeRespPayload) error {
	if p.SessionIndex == 0 {
		return fmt.Errorf("session index must be non-zero")
	}
	if p.Prefix < 1 || p.Prefix > 32 {
		return fmt.Errorf("IPv4 prefix %d out of range", p.Prefix)
	}
	tunnelIP := net.ParseIP(p.TunnelIP)
	if tunnelIP == nil || tunnelIP.To4() == nil {
		return fmt.Errorf("tunnel ip %q is not IPv4", p.TunnelIP)
	}
	gatewayIP := net.ParseIP(p.GatewayIP)
	if gatewayIP == nil || gatewayIP.To4() == nil {
		return fmt.Errorf("gateway ip %q is not IPv4", p.GatewayIP)
	}
	if tunnelIP.Equal(gatewayIP) {
		return fmt.Errorf("tunnel ip must differ from gateway")
	}
	mask := net.CIDRMask(p.Prefix, 32)
	if !tunnelIP.To4().Mask(mask).Equal(gatewayIP.To4().Mask(mask)) {
		return fmt.Errorf("tunnel ip and gateway are not in the same /%d subnet", p.Prefix)
	}
	// IPv4 hosts are required to handle 576-byte datagrams; values below that are not a
	// useful tunnel MTU, while values above the IPv4 maximum can cause oversized buffers
	// and impossible packets. PMTU discovery may safely choose anything within this range.
	if p.MTU < 576 || p.MTU > 65535 {
		return fmt.Errorf("mtu %d out of range", p.MTU)
	}
	if p.KeepaliveSec < 0 {
		return fmt.Errorf("keepalive must be >= 0")
	}
	return nil
}
