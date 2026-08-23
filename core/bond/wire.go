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

// Validate rejects an authenticated-but-semantically-invalid relay configuration before a
// platform layer uses it to create an interface, install routes, or configure DNS. Noise
// authenticates who sent cfg_push; it does not make impossible interface values safe to
// hand to Linux, Windows, or Android APIs.
//
// MTU zero is retained as a compatibility sentinel for older relays/clients that negotiate
// no MTU and use a local fallback. Any explicit MTU must be large enough for an IPv4 tunnel
// and fit the protocol's uint16 payload-length field.
func (p HandshakeRespPayload) Validate() error {
	if p.SessionIndex == 0 {
		return fmt.Errorf("bond: cfg_push session index must be non-zero")
	}
	if p.Prefix < 1 || p.Prefix > 30 {
		return fmt.Errorf("bond: cfg_push IPv4 prefix %d outside supported range 1..30", p.Prefix)
	}
	tunnelIP := net.ParseIP(p.TunnelIP)
	if tunnelIP == nil || tunnelIP.To4() == nil {
		return fmt.Errorf("bond: cfg_push tunnel ip %q is not IPv4", p.TunnelIP)
	}
	gatewayIP := net.ParseIP(p.GatewayIP)
	if gatewayIP == nil || gatewayIP.To4() == nil {
		return fmt.Errorf("bond: cfg_push gateway ip %q is not IPv4", p.GatewayIP)
	}
	if tunnelIP.Equal(gatewayIP) {
		return fmt.Errorf("bond: cfg_push tunnel ip and gateway must differ")
	}
	mask := net.CIDRMask(p.Prefix, 32)
	network := &net.IPNet{IP: tunnelIP.Mask(mask), Mask: mask}
	if !network.Contains(gatewayIP) {
		return fmt.Errorf("bond: cfg_push tunnel ip %s/%d and gateway %s are not in the same subnet", p.TunnelIP, p.Prefix, p.GatewayIP)
	}
	if p.MTU != 0 && (p.MTU < 576 || p.MTU > 65535) {
		return fmt.Errorf("bond: cfg_push mtu %d outside supported range 576..65535", p.MTU)
	}
	if p.KeepaliveSec < 0 {
		return fmt.Errorf("bond: cfg_push keepalive must be >= 0")
	}
	for _, server := range p.DNS {
		if net.ParseIP(server) == nil {
			return fmt.Errorf("bond: cfg_push dns server %q is not an IP address", server)
		}
	}
	return nil
}

func UnmarshalHandshakeResp(b []byte) (HandshakeRespPayload, error) {
	var p HandshakeRespPayload
	if err := cbor.Unmarshal(b, &p); err != nil {
		return p, fmt.Errorf("bond: unmarshal cfg_push: %w", err)
	}
	if err := p.Validate(); err != nil {
		return p, err
	}
	return p, nil
}
