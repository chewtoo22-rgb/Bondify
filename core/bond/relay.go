package bond

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"

	"github.com/chewtoo22-rgb/bondify/core/crypto"
	"github.com/chewtoo22-rgb/bondify/core/proto"
	"github.com/chewtoo22-rgb/bondify/core/tun"
)

// RelayConfig configures the relay's single UDP listener and shared TUN device.
type RelayConfig struct {
	ListenAddr string // host:port, e.g. ":51820"
	RelayKey   crypto.Keypair
	PoolCIDR   string   // e.g. "10.77.0.0/24"
	DNS        []string // pushed to clients via cfg_push
	MTU        int
	KeepAlive  int
}

type relaySession struct {
	sessionIndex uint32
	sess         *crypto.Session
	tunnelIP     net.IP
	clientAddr   atomic.Pointer[net.UDPAddr] // updated on NAT rebinding (phase 1: no rate limit yet)

	sendGSN uint64
	sendPSN uint32
}

// Relay is the phase-1 relay: one UDP socket demultiplexing by Session Index, one shared
// TUN device for all sessions (the kernel's own routing/NAT handles getting decrypted
// packets to the real internet and back — see relay/README for the netns test rig that
// exercises this).
type Relay struct {
	cfg  RelayConfig
	conn *net.UDPConn
	dev  tun.Device
	pool *IPPool

	mu          sync.RWMutex
	byIndex     map[uint32]*relaySession
	byTunnelIP  map[string]*relaySession
	byClientKey map[[crypto.KeyLen]byte]*relaySession

	Stats Stats
}

func NewRelay(cfg RelayConfig, dev tun.Device) (*Relay, error) {
	pool, err := NewIPPool(cfg.PoolCIDR)
	if err != nil {
		return nil, err
	}
	addr, err := net.ResolveUDPAddr("udp", cfg.ListenAddr)
	if err != nil {
		return nil, fmt.Errorf("bond: resolve listen addr: %w", err)
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("bond: listen udp: %w", err)
	}
	return &Relay{
		cfg:         cfg,
		conn:        conn,
		dev:         dev,
		pool:        pool,
		byIndex:     make(map[uint32]*relaySession),
		byTunnelIP:  make(map[string]*relaySession),
		byClientKey: make(map[[crypto.KeyLen]byte]*relaySession),
	}, nil
}

// GatewayIP is the relay's own address inside the tunnel subnet.
func (r *Relay) GatewayIP() net.IP { return r.pool.Gateway() }

// Serve runs the UDP receive loop and the TUN receive loop until either errors. Intended
// to be called from two goroutines by the caller, or wrapped; kept as two blocking methods
// so cmd/hydra-relay controls lifecycle/shutdown explicitly.
func (r *Relay) ServeUDP() error {
	buf := make([]byte, 65536)
	for {
		n, src, err := r.conn.ReadFromUDP(buf)
		if err != nil {
			return fmt.Errorf("bond: relay udp read: %w", err)
		}
		r.handleUDP(buf[:n], src)
	}
}

func (r *Relay) ServeTUN() error {
	// See the identical comment in ClientTunnel.tunToNet: batch size must match
	// Device.BatchSize() or GRO-coalesced reads overflow with ErrTooManySegments under
	// real (non-trivial) throughput.
	batch := r.dev.BatchSize()
	if batch < 1 {
		batch = 1
	}
	bufs := make([][]byte, batch)
	for i := range bufs {
		bufs[i] = make([]byte, tun.IOOffset+r.cfg.MTU+256)
	}
	sizes := make([]int, batch)
	for {
		n, err := r.dev.Read(bufs, sizes, tun.IOOffset)
		if err != nil {
			return fmt.Errorf("bond: relay tun read: %w", err)
		}
		for i := 0; i < n; i++ {
			pkt := bufs[i][tun.IOOffset : tun.IOOffset+sizes[i]]
			dst, ok := destIPv4(pkt)
			if !ok {
				continue
			}
			r.mu.RLock()
			sess := r.byTunnelIP[dst.String()]
			r.mu.RUnlock()
			if sess == nil {
				continue // no session owns this destination; drop
			}
			addr := sess.clientAddr.Load()
			if addr == nil {
				continue // session mid-handshake or path not yet known
			}
			gsn := atomic.AddUint64(&sess.sendGSN, 1) - 1
			psn := atomic.AddUint32(&sess.sendPSN, 1) - 1
			out, err := sealPacket(sess.sess, proto.TypeData, sess.sessionIndex, PathZero, proto.InnerDataHeader{
				GSN: gsn, PSN: psn, PathID: PathZero, PayloadLen: uint16(len(pkt)),
			}, pkt)
			if err != nil {
				log.Printf("bond: relay seal error: %v", err)
				continue
			}
			if _, err := r.conn.WriteToUDP(out, addr); err != nil {
				log.Printf("bond: relay udp write error: %v", err)
				continue
			}
			atomic.AddUint64(&r.Stats.TxPackets, 1)
			atomic.AddUint64(&r.Stats.TxBytes, uint64(len(pkt)))
		}
	}
}

func (r *Relay) handleUDP(buf []byte, src *net.UDPAddr) {
	oh, consumed, err := proto.UnmarshalOuter(buf)
	if err != nil {
		return
	}
	switch oh.Type {
	case proto.TypeHandshakeInit:
		r.handleHandshakeInit(buf[consumed:], src)
	case proto.TypeData:
		r.handleData(oh, buf[consumed:], src)
	default:
		// PROBE/ACK/CTRL/PATH_ADD land in phase 2.
	}
}

func (r *Relay) handleHandshakeInit(msg []byte, src *net.UDPAddr) {
	responder, err := crypto.NewResponder(r.cfg.RelayKey)
	if err != nil {
		log.Printf("bond: new responder: %v", err)
		return
	}
	_, clientKey, err := responder.ReadInit(msg)
	if err != nil {
		// Authentication/parse failure: silently drop, never respond, never log at a
		// rate an attacker could use as an oracle (PROTOCOL.md §3).
		return
	}

	r.mu.Lock()
	existing := r.byClientKey[clientKey]
	r.mu.Unlock()

	var tunnelIP net.IP
	var sessionIndex uint32
	if existing != nil {
		// Reconnecting client (e.g. after restart): reuse its previous tunnel IP so
		// in-flight routes/NAT state elsewhere don't need to churn.
		tunnelIP = existing.tunnelIP
		sessionIndex = existing.sessionIndex
	} else {
		ip, err := r.pool.Allocate()
		if err != nil {
			log.Printf("bond: relay ip pool exhausted: %v", err)
			return
		}
		tunnelIP = ip
		sessionIndex = r.newSessionIndex()
	}

	payload, err := HandshakeRespPayload{
		SessionIndex: sessionIndex,
		TunnelIP:     tunnelIP.String(),
		Prefix:       r.pool.Prefix(),
		GatewayIP:    r.pool.Gateway().String(),
		DNS:          r.cfg.DNS,
		MTU:          r.cfg.MTU,
		KeepaliveSec: r.cfg.KeepAlive,
	}.Marshal()
	if err != nil {
		log.Printf("bond: marshal cfg_push: %v", err)
		return
	}

	respMsg, sess, err := responder.WriteResponse(payload)
	if err != nil {
		log.Printf("bond: write handshake response: %v", err)
		return
	}

	rs := &relaySession{sessionIndex: sessionIndex, sess: sess, tunnelIP: tunnelIP}
	rs.clientAddr.Store(src)

	r.mu.Lock()
	r.byIndex[sessionIndex] = rs
	r.byTunnelIP[tunnelIP.String()] = rs
	r.byClientKey[clientKey] = rs
	r.mu.Unlock()

	out := make([]byte, proto.OuterPrefixLen+len(respMsg))
	if err := proto.MarshalOuter(out, proto.OuterHeader{Type: proto.TypeHandshakeResp, Version: proto.Version, SessionIndex: sessionIndex}); err != nil {
		log.Printf("bond: marshal handshake resp outer: %v", err)
		return
	}
	copy(out[proto.OuterPrefixLen:], respMsg)
	if _, err := r.conn.WriteToUDP(out, src); err != nil {
		log.Printf("bond: send handshake response: %v", err)
	}
	log.Printf("bond: session %08x established, tunnel ip %s, client %s", sessionIndex, tunnelIP, src)
}

func (r *Relay) handleData(oh proto.OuterHeader, ciphertext []byte, src *net.UDPAddr) {
	r.mu.RLock()
	sess := r.byIndex[oh.SessionIndex]
	r.mu.RUnlock()
	if sess == nil {
		return
	}
	inner, payload, err := openPacket(sess.sess, oh, PathZero, ciphertext)
	if err != nil {
		atomic.AddUint64(&r.Stats.RxErrors, 1)
		return
	}
	_ = inner

	// NAT rebinding (PROTOCOL.md §5): a known session, valid AEAD, new source address is
	// simply a path that moved. Phase 1 has one path, so this always applies; phase 2
	// adds per-path address tracking and the 1/s rate limit called for in the spec.
	if cur := sess.clientAddr.Load(); cur == nil || !udpAddrEqual(cur, src) {
		sess.clientAddr.Store(src)
	}

	writeBuf := make([]byte, tun.IOOffset+len(payload))
	copy(writeBuf[tun.IOOffset:], payload)
	if _, err := r.dev.Write([][]byte{writeBuf}, tun.IOOffset); err != nil {
		log.Printf("bond: relay tun write error: %v", err)
		return
	}
	atomic.AddUint64(&r.Stats.RxPackets, 1)
	atomic.AddUint64(&r.Stats.RxBytes, uint64(len(payload)))
}

func (r *Relay) newSessionIndex() uint32 {
	for {
		var b [4]byte
		_, _ = rand.Read(b[:])
		idx := binary.BigEndian.Uint32(b[:])
		if idx == 0 {
			continue
		}
		r.mu.RLock()
		_, exists := r.byIndex[idx]
		r.mu.RUnlock()
		if !exists {
			return idx
		}
	}
}

func udpAddrEqual(a, b *net.UDPAddr) bool {
	return a.IP.Equal(b.IP) && a.Port == b.Port
}

// destIPv4 extracts the destination address from a raw IPv4 packet's header (bytes 16-19).
// Packets that aren't IPv4 (e.g. IPv6, not yet supported by the pool/dispatch above) are
// rejected with ok=false; IPv6 dual-stack is tracked as phase 2+ work per ARCHITECTURE.md.
func destIPv4(pkt []byte) (net.IP, bool) {
	if len(pkt) < 20 || pkt[0]>>4 != 4 {
		return nil, false
	}
	return net.IP(pkt[16:20]), true
}
