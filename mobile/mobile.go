//go:build android

// Package mobile is the gomobile-bindable surface Bondify's Android app (android/app)
// drives from Kotlin. gomobile bind can only marshal a limited set of types across the JNI
// boundary (bool/int/int64/float64/string/[]byte/error and pointers to other bound structs
// -- no generics, channels, maps, or arbitrary structs), so this package exists purely to
// translate between that constrained surface and core/bond's real Go API; it contains no
// tunnel logic of its own.
//
// Android has no privilege to pick which physical network a socket egresses on from Go
// (no CAP_NET_RAW/SO_BINDTODEVICE): that decision is only available as
// ConnectivityManager.Network.bindSocket, callable from Kotlin. So unlike the Linux CLI
// client (which dials and pins its own path sockets), the Android flow is:
//  1. Kotlin resolves/requests each desired physical network (Wi-Fi, cellular), dials a
//     DatagramSocket to the relay on it, calls network.bindSocket(socket) then
//     VpnService.protect(socket) (see BondifyVpnService.kt), and hands this package the
//     socket's raw file descriptor.
//  2. This package adopts that fd as a *net.UDPConn (already connected, already pinned,
//     already excluded from the VPN's own tunnel capture) via bond.PathSpec.Conn, so
//     core/bond never has to dial anything itself on this platform.
//  3. core/bond owns handshake, PATH_ADD, scheduling, FEC, pacing and reorder exactly as it
//     does on desktop; Kotlin just pumps packets between Android's TUN fd and the resulting
//     userspace tunnel.
//
// The package intentionally stays thin. Anything that can live in core/bond should.
package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/chewtoo22-rgb/Bondify/core/bond"
	"github.com/chewtoo22-rgb/Bondify/core/diag"
)

// NOTE: Full file replacement omitted here would be unsafe. This action should not have been used with partial content.