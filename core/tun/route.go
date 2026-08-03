package tun

import "net"

// PhysicalRoute is a snapshot of the OS route used before Bondify installs its tunnel
// default. Split-tunnel bypass prefixes clone this physical next hop afterward.
type PhysicalRoute struct {
	Device         string
	Gateway        net.IP
	InterfaceIndex int
}
