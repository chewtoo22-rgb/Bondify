//go:build android

package tun

import wgtun "golang.zx2c4.com/wireguard/tun"

// CreateFromFD adopts an already-open TUN file descriptor as a Device, instead of opening
// one by name. Android's VpnService.Builder.establish() is the only way an app can get a
// TUN interface at all (opening /dev/net/tun directly requires a privilege apps don't have)
// and it hands back a ParcelFileDescriptor, not a device name -- so Create (which shells out
// to open-by-name) doesn't apply here. The interface's MTU is whatever
// VpnService.Builder.setMtu() configured before establish(); unlike CreateTUNFromFile, this
// doesn't re-set it, matching that Android model (the OS decides the MTU when the interface
// is built, not the app afterward).
func CreateFromFD(fd int) (Device, error) {
	dev, _, err := wgtun.CreateUnmonitoredTUNFromFD(fd)
	return dev, err
}
