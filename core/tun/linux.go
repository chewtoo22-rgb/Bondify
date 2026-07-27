//go:build linux

package tun

import (
	"bufio"
	"fmt"
	"net"
	"os/exec"
	"strings"
)

// ConfigureLinux brings a TUN interface up with the given local address (CIDR form, e.g.
// "10.77.0.2/24") and, if addRoutes is non-empty, routes those CIDRs through it. It shells
// out to the `ip` command rather than reimplementing rtnetlink, deliberately: this is
// exactly what wg-quick itself does, it's auditable in a `strace`, and it avoids pulling in
// a netlink dependency for what is a handful of one-shot setup calls, not a hot path.
func ConfigureLinux(ifName string, localCIDR string, routes []string) error {
	if err := run("ip", "link", "set", "dev", ifName, "up"); err != nil {
		return fmt.Errorf("tun: link up: %w", err)
	}
	if err := run("ip", "addr", "add", localCIDR, "dev", ifName); err != nil {
		return fmt.Errorf("tun: addr add: %w", err)
	}
	for _, r := range routes {
		if err := run("ip", "route", "add", r, "dev", ifName); err != nil {
			return fmt.Errorf("tun: route add %s: %w", r, err)
		}
	}
	return nil
}

// AddHostRoute pins destination host IP to egress via the given device (not the tunnel).
// This is the Linux CLI-client equivalent of Android's VpnService.protect(fd): without it,
// once a default route via the tunnel is installed, the client's own UDP socket traffic to
// the relay would be routed back into the tunnel it's trying to carry, livelocking with
// zero throughput. See PROTOCOL.md / ARCHITECTURE.md §3.1 "the routing loop".
func AddHostRoute(dstIP net.IP, viaDev string) error {
	return run("ip", "route", "add", dstIP.String()+"/32", "dev", viaDev)
}

// DelRoute removes a previously-added route/CIDR.
func DelRoute(cidr string) error {
	return run("ip", "route", "del", cidr)
}

// EgressDevice reports which interface the kernel's current routing table would use to
// reach dst, before any HYDRA route is installed. Used to pin the client's uplink UDP
// socket path (via AddHostRoute) to the real physical interface, so that once a
// tunnel-wide default route is added, the relay-bound UDP traffic itself does not get
// routed back into the tunnel it's carrying — the routing-loop bug described in
// ARCHITECTURE.md §3.1.
func EgressDevice(dst net.IP) (string, error) {
	out, err := exec.Command("ip", "route", "get", dst.String()).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("tun: ip route get %s: %w: %s", dst, err, out)
	}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		for i, f := range fields {
			if f == "dev" && i+1 < len(fields) {
				return fields[i+1], nil
			}
		}
	}
	return "", fmt.Errorf("tun: could not parse egress device from: %s", out)
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v: %w: %s", name, args, err, out)
	}
	return nil
}
