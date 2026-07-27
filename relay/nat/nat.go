// Package nat configures the relay host's kernel to forward and masquerade traffic from
// the tunnel subnet out through a chosen egress interface. PROTOCOL.md/ARCHITECTURE.md
// call for nftables where available, falling back to userspace NAT; phase 1 uses iptables
// (present on effectively every target distribution, including inside containers/CI where
// nftables' nf_tables kernel module may not be loaded) — nftables is tracked as a phase 9
// polish item, not a phase 1 blocker.
package nat

import (
	"fmt"
	"os"
	"os/exec"
)

// EnableForwarding sets net.ipv4.ip_forward=1, required for the relay to route decrypted
// tunnel packets out to the internet and back.
func EnableForwarding() error {
	return os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1\n"), 0644)
}

// Masquerade adds an iptables MASQUERADE rule so packets from tunnelCIDR egressing via
// egressIface get the relay's public source address, and returns a func that removes the
// same rule (best-effort cleanup on shutdown).
func Masquerade(tunnelCIDR, egressIface string) (cleanup func(), err error) {
	add := []string{"-t", "nat", "-A", "POSTROUTING", "-s", tunnelCIDR, "-o", egressIface, "-j", "MASQUERADE"}
	del := []string{"-t", "nat", "-D", "POSTROUTING", "-s", tunnelCIDR, "-o", egressIface, "-j", "MASQUERADE"}
	if out, err := exec.Command("iptables", add...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("nat: iptables masquerade: %w: %s", err, out)
	}
	return func() { exec.Command("iptables", del...).Run() }, nil
}
