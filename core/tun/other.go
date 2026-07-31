//go:build !linux && !windows

package tun

import (
	"context"
	"fmt"
	"net"
)

// DialUDPViaDevice on non-Linux platforms falls back to plain source-address binding
// (device is ignored). Real device-pinned multipath egress on Windows needs
// IP_UNICAST_IF (ARCHITECTURE.md §3.2) and on Android needs Network.bindSocket() from
// Kotlin -- both are phase 5/6 platform work, not yet implemented. This keeps core/
// cross-compiling for every CI target in the meantime; it is not used by any shipped
// non-Linux binary today (desktop/cmd/bondify and relay/cmd/bondify-relay only build for
// linux until their platform-specific work lands).
func DialUDPViaDevice(ctx context.Context, laddr *net.UDPAddr, raddr *net.UDPAddr, device string) (*net.UDPConn, error) {
	_ = device
	d := net.Dialer{}
	if laddr != nil {
		d.LocalAddr = laddr
	}
	conn, err := d.DialContext(ctx, "udp", raddr.String())
	if err != nil {
		return nil, fmt.Errorf("tun: dial udp: %w", err)
	}
	udpConn, ok := conn.(*net.UDPConn)
	if !ok {
		_ = conn.Close()
		return nil, fmt.Errorf("tun: dial udp: unexpected conn type %T", conn)
	}
	return udpConn, nil
}
