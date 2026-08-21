package pairbond

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
)

var ErrRevoked = errors.New("pairbond: peer proxy revoked")

// ProxyConfig configures the peer-side ciphertext forwarder.
// AllowedHostIP must be the LAN IP learned during explicit pairing; forwarding
// fails closed when it is absent. The first datagram from that IP latches the
// host's UDP source port for the lifetime of the proxy, preventing another LAN
// endpoint from taking over the data path after it becomes active.
type ProxyConfig struct {
	ListenAddr    string
	RelayAddr     string
	AllowedHostIP net.IP
	// WANLocalAddr optionally pins the relay-facing UDP socket to a peer uplink
	// source address. Production peers normally leave this empty and let their OS
	// routing choose the WAN; deterministic netns gates use it to select a shaped
	// peer uplink without weakening the ciphertext-only forwarding model.
	WANLocalAddr string
}

// PeerProxy forwards opaque Bondify UDP packets between one paired LAN host and
// the relay. It never parses, decrypts, or re-encrypts Bondify payloads.
type PeerProxy struct {
	lan *net.UDPConn
	wan *net.UDPConn

	allowedHostIP net.IP

	hostMu   sync.RWMutex
	hostAddr *net.UDPAddr

	revoked   atomic.Bool
	closeOnce sync.Once
	done      chan struct{}
}

func NewPeerProxy(cfg ProxyConfig) (*PeerProxy, error) {
	if cfg.AllowedHostIP == nil || cfg.AllowedHostIP.IsUnspecified() {
		return nil, fmt.Errorf("pairbond: allowed host IP is required")
	}
	laddr, err := net.ResolveUDPAddr("udp", cfg.ListenAddr)
	if err != nil {
		return nil, fmt.Errorf("pairbond: resolve listen address: %w", err)
	}
	lan, err := net.ListenUDP("udp", laddr)
	if err != nil {
		return nil, fmt.Errorf("pairbond: listen: %w", err)
	}
	raddr, err := net.ResolveUDPAddr("udp", cfg.RelayAddr)
	if err != nil {
		_ = lan.Close()
		return nil, fmt.Errorf("pairbond: resolve relay address: %w", err)
	}
	var wanLocal *net.UDPAddr
	if cfg.WANLocalAddr != "" {
		ip := net.ParseIP(cfg.WANLocalAddr)
		if ip == nil {
			_ = lan.Close()
			return nil, fmt.Errorf("pairbond: invalid WAN local address %q", cfg.WANLocalAddr)
		}
		wanLocal = &net.UDPAddr{IP: ip}
	}
	wan, err := net.DialUDP("udp", wanLocal, raddr)
	if err != nil {
		_ = lan.Close()
		return nil, fmt.Errorf("pairbond: dial relay: %w", err)
	}
	return &PeerProxy{
		lan:           lan,
		wan:           wan,
		allowedHostIP: append(net.IP(nil), cfg.AllowedHostIP...),
		done:          make(chan struct{}),
	}, nil
}

func (p *PeerProxy) LocalAddr() net.Addr { return p.lan.LocalAddr() }

func (p *PeerProxy) Revoke() error {
	p.revoked.Store(true)
	return p.close()
}

func (p *PeerProxy) Close() error { return p.close() }

func (p *PeerProxy) close() error {
	var first error
	p.closeOnce.Do(func() {
		close(p.done)
		if err := p.lan.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			first = err
		}
		if err := p.wan.Close(); err != nil && !errors.Is(err, net.ErrClosed) && first == nil {
			first = err
		}
	})
	return first
}

// Serve runs the bidirectional forwarding loop until context cancellation,
// explicit revoke, Close, or an unrecoverable socket error.
func (p *PeerProxy) Serve(ctx context.Context) error {
	errCh := make(chan error, 2)
	go func() { errCh <- p.hostToRelay() }()
	go func() { errCh <- p.relayToHost() }()
	go func() {
		select {
		case <-ctx.Done():
			_ = p.close()
		case <-p.done:
		}
	}()

	err := <-errCh
	_ = p.close()
	if p.revoked.Load() {
		return ErrRevoked
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func (p *PeerProxy) hostToRelay() error {
	buf := make([]byte, 64*1024)
	for {
		n, addr, err := p.lan.ReadFromUDP(buf)
		if err != nil {
			return err
		}
		if !addr.IP.Equal(p.allowedHostIP) || !p.acceptHost(addr) {
			continue
		}
		if _, err := p.wan.Write(buf[:n]); err != nil {
			return err
		}
	}
}

func (p *PeerProxy) relayToHost() error {
	buf := make([]byte, 64*1024)
	for {
		n, err := p.wan.Read(buf)
		if err != nil {
			return err
		}
		host := p.hostSnapshot()
		if host == nil {
			continue
		}
		if _, err := p.lan.WriteToUDP(buf[:n], host); err != nil {
			return err
		}
	}
}

func (p *PeerProxy) acceptHost(addr *net.UDPAddr) bool {
	p.hostMu.Lock()
	defer p.hostMu.Unlock()
	if p.hostAddr == nil {
		p.hostAddr = cloneUDPAddr(addr)
		return true
	}
	return p.hostAddr.Port == addr.Port && p.hostAddr.IP.Equal(addr.IP)
}

func (p *PeerProxy) hostSnapshot() *net.UDPAddr {
	p.hostMu.RLock()
	defer p.hostMu.RUnlock()
	return cloneUDPAddr(p.hostAddr)
}

func cloneUDPAddr(addr *net.UDPAddr) *net.UDPAddr {
	if addr == nil {
		return nil
	}
	out := *addr
	out.IP = append(net.IP(nil), addr.IP...)
	return &out
}

// DialPeerPath returns a connected UDP socket suitable for bond.PathSpec.Conn.
func DialPeerPath(ctx context.Context, peerAddr string) (*net.UDPConn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "udp", peerAddr)
	if err != nil {
		return nil, fmt.Errorf("pairbond: dial peer path: %w", err)
	}
	udp, ok := conn.(*net.UDPConn)
	if !ok {
		_ = conn.Close()
		return nil, fmt.Errorf("pairbond: peer path is not UDP")
	}
	return udp, nil
}
