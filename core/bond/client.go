package bond

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"time"

	"github.com/chewtoo22-rgb/bondify/core/crypto"
	"github.com/chewtoo22-rgb/bondify/core/proto"
	"github.com/chewtoo22-rgb/bondify/core/tun"
)

// PathZero is the well-known path ID used until phase 2 introduces PATH_ADD and real
// multipath. Reserved (not reused for a real second path later) purely for log clarity.
const PathZero uint8 = 0

// ClientConfig configures a single-path client tunnel (phase 1).
type ClientConfig struct {
	RelayAddr    string // host:port
	RelayPubKey  [crypto.KeyLen]byte
	ClientKey    crypto.Keypair
	HandshakeTO  time.Duration
	HandshakeTry int
}

// ClientHandshakeResult is what the caller needs to finish OS-level setup (assign the TUN
// device's IP, install routes) before calling ClientTunnel.Run.
type ClientHandshakeResult struct {
	Cfg  HandshakeRespPayload
	Conn *net.UDPConn
}

// DialClient performs the Noise_IK handshake (HANDSHAKE_INIT/HANDSHAKE_RESP) over a fresh
// UDP socket connected to the relay, and returns the resulting tunnel config plus a ready
// ClientTunnel. It does not touch the TUN device's IP/routes — see core/tun's platform
// helpers, invoked by the caller (cmd/bondify) using the returned Cfg, so this package stays
// free of OS-specific plumbing.
func DialClient(ctx context.Context, cfg ClientConfig, dev tun.Device, mtu int) (*ClientTunnel, HandshakeRespPayload, error) {
	raddr, err := net.ResolveUDPAddr("udp", cfg.RelayAddr)
	if err != nil {
		return nil, HandshakeRespPayload{}, fmt.Errorf("bond: resolve relay addr: %w", err)
	}
	conn, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		return nil, HandshakeRespPayload{}, fmt.Errorf("bond: dial relay: %w", err)
	}

	init, err := crypto.NewInitiator(cfg.ClientKey, cfg.RelayPubKey)
	if err != nil {
		_ = conn.Close()
		return nil, HandshakeRespPayload{}, fmt.Errorf("bond: new initiator: %w", err)
	}

	handshakeTO := cfg.HandshakeTO
	if handshakeTO <= 0 {
		handshakeTO = 3 * time.Second
	}
	tries := cfg.HandshakeTry
	if tries <= 0 {
		tries = 3
	}

	initMsg, err := init.WriteInit(nil)
	if err != nil {
		_ = conn.Close()
		return nil, HandshakeRespPayload{}, fmt.Errorf("bond: write handshake init: %w", err)
	}
	initPkt := make([]byte, proto.OuterPrefixLen+len(initMsg))
	if err := proto.MarshalOuter(initPkt, proto.OuterHeader{Type: proto.TypeHandshakeInit, Version: proto.Version}); err != nil {
		_ = conn.Close()
		return nil, HandshakeRespPayload{}, err
	}
	copy(initPkt[proto.OuterPrefixLen:], initMsg)

	respBuf := make([]byte, 2048)
	var respPayload HandshakeRespPayload
	var sess *crypto.Session
	var lastErr error
	for attempt := 0; attempt < tries; attempt++ {
		if _, err := conn.Write(initPkt); err != nil {
			lastErr = fmt.Errorf("bond: send handshake init: %w", err)
			continue
		}
		if err := conn.SetReadDeadline(time.Now().Add(handshakeTO)); err != nil {
			lastErr = fmt.Errorf("bond: set read deadline: %w", err)
			continue
		}
		n, err := conn.Read(respBuf)
		if err != nil {
			lastErr = fmt.Errorf("bond: handshake response timeout: %w", err)
			continue
		}
		oh, consumed, err := proto.UnmarshalOuter(respBuf[:n])
		if err != nil || oh.Type != proto.TypeHandshakeResp {
			lastErr = fmt.Errorf("bond: unexpected handshake response")
			continue
		}
		payload, s, err := init.ReadResponse(respBuf[consumed:n])
		if err != nil {
			lastErr = fmt.Errorf("bond: read handshake response: %w", err)
			continue
		}
		cfgPush, err := UnmarshalHandshakeResp(payload)
		if err != nil {
			lastErr = err
			continue
		}
		respPayload = cfgPush
		sess = s
		lastErr = nil
		break
	}
	_ = conn.SetReadDeadline(time.Time{})
	if lastErr != nil {
		_ = conn.Close()
		return nil, HandshakeRespPayload{}, lastErr
	}

	t := &ClientTunnel{
		conn:         conn,
		dev:          dev,
		sess:         sess,
		sessionIndex: respPayload.SessionIndex,
		mtu:          mtu,
	}
	return t, respPayload, nil
}

// ClientTunnel is the single-path (phase 1) client-side data-plane engine: TUN <-> UDP,
// with BOND/1 DATA framing and AEAD in both directions.
type ClientTunnel struct {
	conn         *net.UDPConn
	dev          tun.Device
	sess         *crypto.Session
	sessionIndex uint32
	mtu          int

	sendGSN uint64
	sendPSN uint32

	Stats Stats
}

// Stats are simple atomic counters, enough for phase 1 visibility; core/bond's diagnostics
// story (per-path sparklines etc.) lands with the scheduler in phase 3+.
type Stats struct {
	TxPackets uint64
	RxPackets uint64
	TxBytes   uint64
	RxBytes   uint64
	RxErrors  uint64
}

// Run pumps packets in both directions until ctx is cancelled or either direction errors.
func (t *ClientTunnel) Run(ctx context.Context) error {
	errCh := make(chan error, 2)
	go func() { errCh <- t.tunToNet(ctx) }()
	go func() { errCh <- t.netToTun(ctx) }()
	select {
	case <-ctx.Done():
		_ = t.conn.Close()
		_ = t.dev.Close()
		<-errCh
		return ctx.Err()
	case err := <-errCh:
		_ = t.conn.Close()
		_ = t.dev.Close()
		return err
	}
}

func (t *ClientTunnel) tunToNet(ctx context.Context) error {
	// Read must be called with a batch sized to Device.BatchSize(): with GRO/GSO enabled
	// (which wireguard-go's Linux TUN turns on whenever the kernel supports it, with no
	// way for a caller to opt out), a single underlying read can coalesce more than one
	// packet, and passing a 1-slot batch overflows that with tun.ErrTooManySegments the
	// moment a real TCP flow (e.g. iperf3) is saturating the link.
	batch := t.dev.BatchSize()
	if batch < 1 {
		batch = 1
	}
	bufs := make([][]byte, batch)
	for i := range bufs {
		bufs[i] = make([]byte, tun.IOOffset+t.mtu+256)
	}
	sizes := make([]int, batch)
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		n, err := t.dev.Read(bufs, sizes, tun.IOOffset)
		if err != nil {
			return fmt.Errorf("bond: tun read: %w", err)
		}
		for i := 0; i < n; i++ {
			payload := bufs[i][tun.IOOffset : tun.IOOffset+sizes[i]]
			gsn := atomic.AddUint64(&t.sendGSN, 1) - 1
			psn := atomic.AddUint32(&t.sendPSN, 1) - 1
			pkt, err := sealPacket(t.sess, proto.TypeData, t.sessionIndex, PathZero, proto.InnerDataHeader{
				GSN:        gsn,
				PSN:        psn,
				PathID:     PathZero,
				PayloadLen: uint16(len(payload)),
			}, payload)
			if err != nil {
				return fmt.Errorf("bond: seal data: %w", err)
			}
			if _, err := t.conn.Write(pkt); err != nil {
				return fmt.Errorf("bond: udp write: %w", err)
			}
			atomic.AddUint64(&t.Stats.TxPackets, 1)
			atomic.AddUint64(&t.Stats.TxBytes, uint64(len(payload)))
		}
	}
}

func (t *ClientTunnel) netToTun(ctx context.Context) error {
	buf := make([]byte, 65536)
	writeBuf := make([]byte, tun.IOOffset+65536)
	writeBufs := make([][]byte, 1)
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		n, err := t.conn.Read(buf)
		if err != nil {
			return fmt.Errorf("bond: udp read: %w", err)
		}
		oh, consumed, err := proto.UnmarshalOuter(buf[:n])
		if err != nil {
			continue // malformed; drop silently per "authenticate first" discipline
		}
		if oh.Type != proto.TypeData {
			continue // PROBE/ACK/CTRL land in phase 2
		}
		inner, payload, err := openPacket(t.sess, oh, PathZero, buf[consumed:n])
		if err != nil {
			atomic.AddUint64(&t.Stats.RxErrors, 1)
			continue
		}
		_ = inner // GSN-ordered delivery arrives with the reorder buffer in phase 2
		n2 := copy(writeBuf[tun.IOOffset:], payload)
		writeBufs[0] = writeBuf[:tun.IOOffset+n2]
		if _, err := t.dev.Write(writeBufs, tun.IOOffset); err != nil {
			return fmt.Errorf("bond: tun write: %w", err)
		}
		atomic.AddUint64(&t.Stats.RxPackets, 1)
		atomic.AddUint64(&t.Stats.RxBytes, uint64(len(payload)))
	}
}
