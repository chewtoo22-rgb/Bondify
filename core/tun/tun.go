// Package tun provides Bondify's cross-platform virtual network interface abstraction.
//
// The actual device implementation is not reinvented here: golang.zx2c4.com/wireguard/tun
// is wireguard-go's own public, cross-platform TUN package (the same code WireGuard itself
// uses) — /dev/net/tun on Linux, wintun on Windows, utun on macOS/iOS, all behind one
// vectorized Read/Write interface. This package just re-exports it under Bondify's own name
// so the rest of the codebase depends on core/tun, not a third-party import path directly,
// and adds the small amount of glue (IP/route configuration) that CreateTUN deliberately
// leaves to the caller.
package tun

import wgtun "golang.zx2c4.com/wireguard/tun"

// Device is a platform TUN device: read/write raw IP packets, no L2 framing.
type Device = wgtun.Device

// Event mirrors wgtun.Event (interface up/down notifications).
type Event = wgtun.Event

// Create opens (creating if necessary) a TUN device with the given name and MTU. On Linux
// this requires CAP_NET_ADMIN. The returned Device has no IP address or routes configured;
// see the platform-specific Configure helpers (linux.go) for that.
func Create(name string, mtu int) (Device, error) {
	return wgtun.CreateTUN(name, mtu)
}

// IOOffset is the number of bytes every caller must reserve at the front of each buffer
// passed to Device.Read/Device.Write. wireguard-go's Linux implementation enables
// IFF_VNET_HDR (virtio-net headers) whenever the kernel supports it, for GRO/GSO batching;
// its Write path hard-fails ("invalid offset") if offset is smaller than that header, and
// Read always writes the packet starting at that same offset regardless of what the caller
// asked for. Bondify doesn't want or need GRO/GSO here (packets are already
// individually-sized after decryption), but the Device interface doesn't expose a way to
// disable it, so every buffer must simply reserve this much headroom. Fixed at the
// virtio_net_hdr struct size (10 bytes); see offload_linux.go in wireguard-go's tun package.
const IOOffset = 10
