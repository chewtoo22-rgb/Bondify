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

func GenerateKey() (string, error) {
	kp, err := crypto.GenerateKeypair()
	if err != nil {
		return "", err
	}
	return crypto.EncodeKey(kp.Private), nil
}

func PublicKeyFor(privB64 string) (string, error) {
	priv, err := crypto.DecodeKey(privB64)
	if err != nil {
		return "", fmt.Errorf("mobile: bad private key: %w", err)
	}
	return crypto.EncodeKey(crypto.DerivePublic(priv)), nil
}

type TunnelBuilder struct {
	relayAddr   string
	relayPubKey [crypto.KeyLen]byte
	clientKey   crypto.Keypair
	scheduler   string
	mode        string
	fec         bool

	paths  []bond.PathSpec
	labels []string
}

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

func adoptUDPFd(fd int, label string) (*net.UDPConn, error) {
	f := os.NewFile(uintptr(fd), label)
	if f == nil {
		return nil, fmt.Errorf("mobile: invalid fd %d for path %q", fd, label)
	}
	conn, err := net.FileConn(f)
	_ = f.Close()
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

type Tunnel struct {
	SessionIndexHex string
	TunnelIP        string
	Prefix          int
	GatewayIP       string
	MTU             int
	PathErrors      string

	mu     sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
	t      *bond.ClientTunnel
	done   chan struct{}
	runErr error

	pathRegistry runtimePathRegistry
}

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
			err = nil
		}
		tu.mu.Lock()
		tu.runErr = err
		tu.mu.Unlock()
	}()
	return nil
}

func (tu *Tunnel) AwaitExit() string {
	<-tu.done
	tu.mu.Lock()
	defer tu.mu.Unlock()
	if tu.runErr != nil {
		return tu.runErr.Error()
	}
	return ""
}

func (tu *Tunnel) Close() {
	tu.mu.Lock()
	cancel := tu.cancel
	tu.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

const addPathHandshakeTimeout = 5 * time.Second

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
