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
package mobile

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/chewtoo22-rgb/bondify/core/bond"
	"github.com/chewtoo22-rgb/bondify/core/crypto"
	"github.com/chewtoo22-rgb/bondify/core/tun"
)

// GenerateKey returns a fresh base64-encoded Curve25519 private key, for first-run
// onboarding (before a TunnelBuilder is ever constructed).
func GenerateKey() (string, error) {
	kp, err := crypto.GenerateKeypair()
	if err != nil {
		return "", err
	}
	return crypto.EncodeKey(kp.Private), nil
}

// PublicKeyFor derives and base64-encodes the public key matching a base64-encoded private
// key, so the app can display "your device's public key" without round-tripping through the
// relay first.
func PublicKeyFor(privB64 string) (string, error) {
	priv, err := crypto.DecodeKey(privB64)
	if err != nil {
		return "", fmt.Errorf("mobile: bad private key: %w", err)
	}
	return crypto.EncodeKey(crypto.DerivePublic(priv)), nil
}

// TunnelBuilder accumulates path sockets one at a time before Handshake, since gomobile bind
// cannot marshal a Kotlin-constructed slice of bound structs (or of raw fds) across the JNI
// boundary reliably -- an imperative AddPath/AddPathFD builder using only plain ints/strings
// sidesteps that entirely.
type TunnelBuilder struct {
	relayAddr   string
	relayPubKey [crypto.KeyLen]byte
	clientKey   crypto.Keypair
	scheduler   string
	mode        string
	fec         bool

	paths []bond.PathSpec
	// labels is parallel to paths, carried into the resulting Tunnel so its later
	// AddPathFD/DropPathLabel calls (runtime path churn after Handshake) can address these
	// initial paths by the same human-readable label ("wifi", "cellular") Kotlin already
	// uses, instead of needing to track raw path IDs itself.
	labels []string
}

// NewTunnelBuilder starts configuring one tunnel attempt. clientKeyB64 is this device's own
// private key (from GenerateKey, persisted by the app across runs).
func NewTunnelBuilder(relayAddr, relayPubKeyB64, clientKeyB64, scheduler, mode string, fec bool) (*TunnelBuilder, error) {
	relayPub, err := crypto.DecodeKey(relayPubKeyB64)
	if err != nil {
		return nil, fmt.Errorf("mobile: bad relay public key: %w", err)
	}
	priv, err := crypto.DecodeKey(clientKeyB64)
	if err != nil {
		return nil, fmt.Errorf("mobile: bad client key: %w", err)
	}
	return &TunnelBuilder{
		relayAddr:   relayAddr,
		relayPubKey: relayPub,
		clientKey:   crypto.Keypair{Private: priv, Public: crypto.DerivePublic(priv)},
		scheduler:   scheduler,
		mode:        mode,
		fec:         fec,
	}, nil
}

// AddPathFD adds one uplink whose socket Kotlin has already dialed to relayAddr, bound to a
// specific Network (Wi-Fi or cellular), and passed to VpnService.protect -- see this
// package's doc comment. fd is consumed (the returned *net.UDPConn owns it); the caller must
// not use it again after this call. label is a human-readable name (e.g. "wifi", "cellular")
// that the resulting Tunnel's own AddPathFD/DropPathLabel use to address this same physical
// uplink later, across churn (see Tunnel's doc comment).
func (b *TunnelBuilder) AddPathFD(fd int, label string) error {
	if err := validatePathLabel(label); err != nil {
		return err
	}
	if pathLabelExists(b.labels, label) {
		return fmt.Errorf("mobile: path %q already added to tunnel builder", label)
	}
	udpConn, err := adoptUDPFd(fd, label)
	if err != nil {
		return err
	}
	b.paths = append(b.paths, bond.PathSpec{Conn: udpConn})
	b.labels = append(b.labels, label)
	return nil
}

// adoptUDPFd wraps a Kotlin-dialed, network-bound, VpnService.protect()ed file descriptor as
// a *net.UDPConn. fd is consumed either way -- os.NewFile takes no ownership until FileConn
// dup()s it, at which point the original is closed regardless of outcome.
func adoptUDPFd(fd int, label string) (*net.UDPConn, error) {
	f := os.NewFile(uintptr(fd), label)
	if f == nil {
		return nil, fmt.Errorf("mobile: invalid fd %d for path %q", fd, label)
	}
	conn, err := net.FileConn(f)
	_ = f.Close() // FileConn dup()s the descriptor; our copy is no longer needed either way
	if err != nil {
		return nil, fmt.Errorf("mobile: adopt fd for path %q: %w", label, err)
	}
	udpConn, ok := conn.(*net.UDPConn)
	if !ok {
		_ = conn.Close()
		return nil, fmt.Errorf("mobile: fd for path %q is not a UDP socket (got %T)", label, conn)
	}
	return udpConn, nil
}

// Tunnel wraps one client tunnel for BondifyVpnService: one instance per VPN session, torn
// down as a unit by Close. The exported fields are filled in by Handshake and are what a
// separate result struct would otherwise have carried -- gomobile bind only allows a bound
// method to return one non-error value, so the session-establishment result rides along on
// the same object AttachTUN/AwaitExit/Close/DiagnosticsJSON already operate on.
type Tunnel struct {
	SessionIndexHex string
	TunnelIP        string
	Prefix          int
	GatewayIP       string
	MTU             int
	PathErrors      string // newline-joined; empty if every requested path joined cleanly

	mu     sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
	t      *bond.ClientTunnel
	done   chan struct{}
	runErr error

	// pathRegistry lets Kotlin keep addressing physical uplinks by stable labels while the
	// protocol uses uint8 IDs. Reservations span fd adoption and PATH_ADD so concurrent
	// ConnectivityManager callbacks cannot double-register a label or collide after ID wrap.
	pathRegistry runtimePathRegistry
}

// Handshake performs the Noise handshake and PATH_ADD registration over the path(s) already
// added via AddPathFD, and returns once a session is established (or definitively fails).
// The returned Tunnel is not yet running -- its TunnelIP/Prefix/GatewayIP/MTU fields are
// populated so BondifyVpnService can build its VpnService.Builder with the *real* assigned
// address (Android's interface can't be reconfigured after establish(), unlike a Linux TUN
// device's IP/routes which are set well after the device itself opens -- see
// core/bond.AttachTUN's doc comment for the same split on the Go side), then call this
// Tunnel's AttachTUN with the resulting fd to actually start the packet pump.
func (b *TunnelBuilder) Handshake() (*Tunnel, error) {
	if len(b.paths) == 0 {
		return nil, fmt.Errorf("mobile: no paths added (call AddPathFD at least once)")
	}
	sendMode, err := bond.ModeFromString(b.mode)
	if err != nil {
		return nil, fmt.Errorf("mobile: bad mode: %w", err)
	}
	fec := b.fec
	if fec && sendMode == bond.ModeRedundant {
		// See desktop/cmd/bondify/main.go's identical guard: REDUNDANT never uses FEC, so
		// leaving it on here would just pay its per-packet copy cost for nothing.
		fec = false
	}

	ctx, cancel := context.WithCancel(context.Background())
	t, resp, err := bond.DialHandshake(ctx, bond.ClientConfig{
		RelayAddr:   b.relayAddr,
		RelayPubKey: b.relayPubKey,
		ClientKey:   b.clientKey,
		Paths:       b.paths,
		Scheduler:   b.scheduler,
		Mode:        sendMode,
		FEC:         fec,
	})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("mobile: handshake failed: %w", err)
	}

	var pathErrs string
	for i, perr := range t.PathErrors() {
		if i > 0 {
			pathErrs += "\n"
		}
		pathErrs += perr.Error()
	}

	return &Tunnel{
		SessionIndexHex: fmt.Sprintf("%08x", resp.SessionIndex),
		TunnelIP:        resp.TunnelIP,
		Prefix:          resp.Prefix,
		GatewayIP:       resp.GatewayIP,
		MTU:             resp.MTU,
		PathErrors:      pathErrs,
		ctx:             ctx,
		cancel:          cancel,
		t:               t,
		done:            make(chan struct{}),
		pathRegistry:    newRuntimePathRegistry(b.labels),
	}, nil
}

// AttachTUN gives the tunnel its TUN device (tunFD is the fd from
// VpnService.Builder.establish(), built using the addresses Handshake returned) and starts
// its packet pump running in the background; call AwaitExit to block for its lifetime.
func (tu *Tunnel) AttachTUN(tunFD int) error {
	dev, err := tun.CreateFromFD(tunFD)
	if err != nil {
		return fmt.Errorf("mobile: adopt tun fd: %w", err)
	}
	tu.t.AttachTUN(dev, tu.MTU)
	go func() {
		defer close(tu.done)
		err := tu.t.Run(tu.ctx)
		if tu.ctx.Err() != nil {
			err = nil // caller-requested shutdown, not a failure
		}
		tu.mu.Lock()
		tu.runErr = err
		tu.mu.Unlock()
	}()
	return nil
}

// AwaitExit blocks until the tunnel exits (Close was called, or an unrecoverable error occurred)
// and returns the error message, or "" on a clean caller-requested shutdown. Gomobile has no
// future/channel type to bind, so Kotlin is expected to call this from its own background
// coroutine/thread rather than the caller of AttachTUN.
func (tu *Tunnel) AwaitExit() string {
	<-tu.done
	tu.mu.Lock()
	defer tu.mu.Unlock()
	if tu.runErr != nil {
		return tu.runErr.Error()
	}
	return ""
}

// Close tears the tunnel down; AwaitExit (on whatever goroutine/thread called it) then returns.
func (tu *Tunnel) Close() {
	tu.mu.Lock()
	cancel := tu.cancel
	tu.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// addPathHandshakeTimeout bounds how long a runtime AddPathFD waits for the relay's
// PATH_ADD ack. Generous relative to the client's own default (3s) since this call already
// competes with live traffic on the tunnel's other path(s) for the relay's attention, unlike
// the initial handshake where nothing else is happening yet.
const addPathHandshakeTimeout = 5 * time.Second

// AddPathFD registers a new uplink -- or a replacement for one previously retired via
// DropPathLabel -- on an already-running tunnel, without disturbing the session or its other
// paths. This is what lets BondifyVpnService.kt react to a ConnectivityManager
// NetworkCallback.onAvailable firing after Handshake already completed (Wi-Fi returning from
// a dead zone, cellular finishing a slow radio handover) instead of that uplink simply never
// joining the bond for the rest of the session. label and a protocol path ID are reserved
// before the fd is adopted, so duplicate callbacks fail without consuming the caller's fd.
func (tu *Tunnel) AddPathFD(fd int, label string) error {
	if err := validatePathLabel(label); err != nil {
		return err
	}

	tu.mu.Lock()
	id, err := tu.pathRegistry.reserve(label)
	parent := tu.ctx
	tu.mu.Unlock()
	if err != nil {
		return err
	}
	releaseReservation := true
	defer func() {
		if releaseReservation {
			tu.mu.Lock()
			tu.pathRegistry.release(label, id)
			tu.mu.Unlock()
		}
	}()

	udpConn, err := adoptUDPFd(fd, label)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(parent, addPathHandshakeTimeout)
	defer cancel()
	if err := tu.t.AddPath(ctx, id, bond.PathSpec{Conn: udpConn}); err != nil {
		return fmt.Errorf("mobile: add path %q: %w", label, err)
	}

	tu.mu.Lock()
	tu.pathRegistry.commit(label, id)
	tu.mu.Unlock()
	releaseReservation = false
	return nil
}

// DropPathLabel retires the uplink previously registered under label (from either
// TunnelBuilder.AddPathFD or this type's AddPathFD), telling the relay and freeing the label
// for a future AddPathFD call once the physical network returns. Driven by
// NetworkCallback.onLost in BondifyVpnService.kt.
func (tu *Tunnel) DropPathLabel(label string) error {
	tu.mu.Lock()
	id, ok := tu.pathRegistry.lookup(label)
	if ok {
		delete(tu.pathRegistry.labelToID, label)
	}
	tu.mu.Unlock()
	if !ok {
		return fmt.Errorf("mobile: no path registered for label %q", label)
	}
	return tu.t.DropPath(id, "onLost")
}

// DiagnosticsJSON returns the same JSON shape as the desktop client's
// /api/v1/diagnostics endpoint (core/diag), for the app's connection-status UI.
func (tu *Tunnel) DiagnosticsJSON() (string, error) {
	tu.mu.Lock()
	t := tu.t
	tu.mu.Unlock()
	b, err := json.Marshal(t.Diagnostics())
	if err != nil {
		return "", fmt.Errorf("mobile: marshal diagnostics: %w", err)
	}
	return string(b), nil
}
