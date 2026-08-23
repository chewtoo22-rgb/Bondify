package main

import "fmt"

const (
	minTunnelMTU = 576
	maxTunnelMTU = 65535
)

// selectTunnelMTU makes an authenticated relay cfg_push authoritative when it includes an
// MTU, while retaining the CLI value as a compatibility fallback for an older relay that
// sends zero. Keeping this decision pure makes the platform-independent policy testable
// without creating a privileged TUN device.
func selectTunnelMTU(relayMTU, fallback int) (int, error) {
	mtu := relayMTU
	if mtu == 0 {
		mtu = fallback
	}
	if mtu < minTunnelMTU || mtu > maxTunnelMTU {
		return 0, fmt.Errorf("tunnel MTU %d outside supported range %d..%d", mtu, minTunnelMTU, maxTunnelMTU)
	}
	return mtu, nil
}
