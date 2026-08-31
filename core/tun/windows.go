//go:build windows

package tun

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// ipUnicastIF and ipv6UnicastIF are the Winsock interface-pinning options. Both have the
// stable Windows SDK value 31, but they deliberately use different levels and byte order:
// IP_UNICAST_IF takes a network-order IPv4 interface index while IPV6_UNICAST_IF takes the
// interface index in host order.
const (
	ipUnicastIF   = 31
	ipv6UnicastIF = 31
)

// htonl reverses idx's byte order. IP_UNICAST_IF is the one common sockopt Microsoft
// documents as wanting its DWORD argument in network (big-endian) byte order rather than
// host order -- see "IP_UNICAST_IF socket option" in the Windows SDK docs (IPv6's
// IPV6_UNICAST_IF is the opposite: host order). A plain byte reversal is sufficient because
// every architecture Windows currently ships on (x86, amd64, arm64) is little-endian.
func htonl(idx uint32) uint32 {
	return (idx>>24)&0xff | (idx>>8)&0xff00 | (idx<<8)&0xff0000 | (idx<<24)&0xff000000
}

// unicastInterfaceSockopt returns the protocol level, option, and correctly encoded
// interface index for the relay address family. Keeping this decision explicit prevents an
// IPv6 relay socket from accidentally receiving the IPv4 IP_UNICAST_IF option, which would
// leave a Windows multipath socket unpinned or make the dial fail entirely.
func unicastInterfaceSockopt(raddr *net.UDPAddr, index uint32) (level, option, value int, err error) {
	if raddr == nil || raddr.IP == nil {
		return 0, 0, 0, fmt.Errorf("tun: relay address has no IP family")
	}
	if raddr.IP.To4() != nil {
		return windows.IPPROTO_IP, ipUnicastIF, int(htonl(index)), nil
	}
	if raddr.IP.To16() != nil {
		return windows.IPPROTO_IPV6, ipv6UnicastIF, int(index), nil
	}
	return 0, 0, 0, fmt.Errorf("tun: unsupported relay IP %q", raddr.IP.String())
}

// EgressDevice reports the adapter Windows' routing table currently uses to reach dst,
// mirroring core/tun/linux.go's EgressDevice (`ip route get`) via the Win32
// GetBestInterfaceEx API instead of shelling out -- no equivalent single-purpose CLI command
// exists on Windows that's as parse-stable as `ip route get`.
func EgressDevice(dst net.IP) (string, error) {
	idx, err := bestInterfaceIndex(dst)
	if err != nil {
		return "", err
	}
	ifi, err := net.InterfaceByIndex(int(idx))
	if err != nil {
		return "", fmt.Errorf("tun: interface for index %d: %w", idx, err)
	}
	return ifi.Name, nil
}

func bestInterfaceIndex(dst net.IP) (uint32, error) {
	var sa windows.Sockaddr
	if ip4 := dst.To4(); ip4 != nil {
		var addr [4]byte
		copy(addr[:], ip4)
		sa = &windows.SockaddrInet4{Addr: addr}
	} else {
		var addr [16]byte
		copy(addr[:], dst.To16())
		sa = &windows.SockaddrInet6{Addr: addr}
	}
	var idx uint32
	if err := windows.GetBestInterfaceEx(sa, &idx); err != nil {
		return 0, fmt.Errorf("tun: GetBestInterfaceEx: %w", err)
	}
	return idx, nil
}

// AddHostRoute pins dst to egress via the named adapter, ahead of any tunnel-wide route --
// the same protect-loop guard as Linux's AddHostRoute (ARCHITECTURE.md §3.1), implemented
// with `netsh` instead of `ip route add` for the same "auditable, no extra routing
// dependency for a one-shot call" reason core/tun/linux.go gives for shelling out.
func AddHostRoute(dstIP net.IP, viaDev string) error {
	ifi, err := net.InterfaceByName(viaDev)
	if err != nil {
		return fmt.Errorf("tun: interface %q: %w", viaDev, err)
	}
	if err := run("netsh", "interface", "ip", "add", "route", dstIP.String()+"/32", fmt.Sprint(ifi.Index)); err != nil {
		return fmt.Errorf("tun: add route: %w", err)
	}
	return nil
}

// RouteFor snapshots Windows' current best route, including the next hop required to
// recreate it as a more-specific split-tunnel bypass after the default route changes.
func RouteFor(dst net.IP) (PhysicalRoute, error) {
	script := fmt.Sprintf(
		"$r = Find-NetRoute -RemoteIPAddress '%s' | "+
			"Where-Object { $_.PSObject.Properties.Name -contains 'DestinationPrefix' } | "+
			"Select-Object -First 1; "+
			"if ($null -eq $r) { throw 'no route' }; "+
			"Write-Output $r.InterfaceIndex; Write-Output $r.NextHop",
		dst.String(),
	)
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
	if err != nil {
		return PhysicalRoute{}, fmt.Errorf("tun: Find-NetRoute %s: %w: %s", dst, err, out)
	}
	lines := strings.Fields(string(out))
	if len(lines) < 1 {
		return PhysicalRoute{}, fmt.Errorf("tun: empty Find-NetRoute result for %s", dst)
	}
	idx, err := strconv.Atoi(lines[0])
	if err != nil || idx < 1 {
		return PhysicalRoute{}, fmt.Errorf("tun: invalid Find-NetRoute interface %q", lines[0])
	}
	route := PhysicalRoute{InterfaceIndex: idx}
	if ifi, err := net.InterfaceByIndex(idx); err == nil {
		route.Device = ifi.Name
	}
	if len(lines) > 1 {
		if gateway := net.ParseIP(lines[1]); gateway != nil && !gateway.IsUnspecified() {
			route.Gateway = gateway
		}
	}
	return route, nil
}

// AddBypassRoute mirrors Linux's route cloning using Windows' NetTCPIP cmdlets.
func AddBypassRoute(cidr string, route PhysicalRoute) (added bool, err error) {
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil || !prefix.Addr().Is4() {
		return false, fmt.Errorf("tun: invalid IPv4 bypass CIDR %q", cidr)
	}
	cidr = prefix.Masked().String()
	if route.InterfaceIndex < 1 {
		return false, fmt.Errorf("tun: bypass route %s has no interface index", cidr)
	}
	nextHop := ""
	if gateway := route.Gateway.To4(); gateway != nil {
		nextHop = fmt.Sprintf(" -NextHop '%s'", gateway.String())
	}
	script := fmt.Sprintf(
		"$existing = Get-NetRoute -DestinationPrefix '%s' -ErrorAction SilentlyContinue; "+
			"if ($null -ne $existing) { Write-Output 'existing'; exit 0 }; "+
			"New-NetRoute -DestinationPrefix '%s' -InterfaceIndex %d%s "+
			"-PolicyStore ActiveStore -ErrorAction Stop | Out-Null; Write-Output 'added'",
		cidr, cidr, route.InterfaceIndex, nextHop,
	)
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("tun: add bypass route %s: %w: %s", cidr, err, out)
	}
	return strings.Contains(string(out), "added"), nil
}

func canonicalIPv4Prefix(cidr string) (string, error) {
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil || !prefix.Addr().Is4() {
		return "", fmt.Errorf("tun: invalid IPv4 route CIDR %q", cidr)
	}
	return prefix.Masked().String(), nil
}

// DelRoute removes a previously-added IPv4 route/CIDR. Validate and canonicalize the
// destination before embedding it in the PowerShell command so malformed/user-controlled
// input cannot become command text.
func DelRoute(cidr string) error {
	canonical, err := canonicalIPv4Prefix(cidr)
	if err != nil {
		return err
	}
	if err := run("powershell", "-NoProfile", "-NonInteractive", "-Command",
		"Remove-NetRoute -DestinationPrefix '"+canonical+"' -Confirm:$false"); err != nil {
		return fmt.Errorf("tun: delete route: %w", err)
	}
	return nil
}

// Configure assigns localCIDR to the named adapter and installs routes through it, mirroring
// core/tun/linux.go's Configure. wireguard-go's Windows tun.Device only creates the wintun
// adapter (CreateTUN); IP/route configuration is deliberately left to the caller on every
// platform, same division of responsibility as Linux, just via `netsh` instead of `ip`.
//
// ifName is expected to be the same name CreateTUN was called with. wintun adapters normally
// keep the requested name, but Windows can rename one out from under a caller if an adapter
// by that name already exists in a conflicting state; this is not handled here.
func Configure(ifName string, localCIDR string, routes []string) error {
	ip, ipnet, err := net.ParseCIDR(localCIDR)
	if err != nil {
		return fmt.Errorf("tun: parse %q: %w", localCIDR, err)
	}
	mask := net.IP(ipnet.Mask)
	if err := run("netsh", "interface", "ip", "set", "address",
		"name="+ifName, "static", ip.String(), mask.String()); err != nil {
		return fmt.Errorf("tun: set address: %w", err)
	}
	for _, r := range routes {
		if err := run("netsh", "interface", "ip", "add", "route", r, ifName); err != nil {
			return fmt.Errorf("tun: route add %s: %w", r, err)
		}
	}
	return nil
}

// DialUDPViaDevice dials a UDP socket bound to laddr (may be nil) and, if device is
// non-empty, pins it to that adapter using the address-family-appropriate Winsock option.
// Windows uses IP_UNICAST_IF for IPv4 and IPV6_UNICAST_IF for IPv6; using the IPv4 option
// unconditionally breaks explicit multipath egress binding for IPv6 relays.
func DialUDPViaDevice(ctx context.Context, laddr *net.UDPAddr, raddr *net.UDPAddr, device string) (*net.UDPConn, error) {
	d := net.Dialer{}
	if laddr != nil {
		d.LocalAddr = laddr
	}
	if device != "" {
		ifi, err := net.InterfaceByName(device)
		if err != nil {
			return nil, fmt.Errorf("tun: interface %q: %w", device, err)
		}
		level, option, value, err := unicastInterfaceSockopt(raddr, uint32(ifi.Index))
		if err != nil {
			return nil, err
		}
		d.Control = func(_, _ string, c syscall.RawConn) error {
			var opErr error
			if err := c.Control(func(fd uintptr) {
				opErr = windows.SetsockoptInt(windows.Handle(fd), level, option, value)
			}); err != nil {
				return err
			}
			return opErr
		}
	}
	conn, err := d.DialContext(ctx, "udp", raddr.String())
	if err != nil {
		return nil, fmt.Errorf("tun: dial udp via device %q: %w", device, err)
	}
	udpConn, ok := conn.(*net.UDPConn)
	if !ok {
		_ = conn.Close()
		return nil, fmt.Errorf("tun: dial udp: unexpected conn type %T", conn)
	}
	return udpConn, nil
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v: %w: %s", name, args, err, out)
	}
	return nil
}
