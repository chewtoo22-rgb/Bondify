package bond

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chewtoo22-rgb/bondify/core/crypto"
	"github.com/chewtoo22-rgb/bondify/core/proto"
	"github.com/chewtoo22-rgb/bondify/core/reorder"
	"github.com/chewtoo22-rgb/bondify/core/sched"
	"github.com/chewtoo22-rgb/bondify/core/tun"
)

// PathSpec configures one client uplink.
type PathSpec struct {
	LocalAddr string // local bind address; "" lets the OS choose the source
	// Device pins the socket to a specific physical interface via SO_BINDTODEVICE,
	// overriding destination-based routing. Required once more than one path needs to
	// reach the *same* relay address: plain source-IP binding does not reliably control
	// egress interface in that case (see core/tun/linux.go's DialUDPViaDevice doc for
	// why). Leave empty for a single-path setup or when each uplink already has its own
	// distinct default route.
	Device string
}

// ClientConfig configures a (possibly multi-path) client tunnel.
type ClientConfig struct {
	RelayAddr   string // host:port
	RelayPubKey [crypto.KeyLen]byte
	ClientKey   crypto.Keypair
	// Paths is one entry per uplink. The first entry is used for path 0 (the Noise
	// handshake path); each additional entry opens its own UDP socket and registers via
	// PATH_ADD. Pass a single zero-value PathSpec for the phase-1-equivalent single-path
	// behavior (system-chosen source address, no device pinning).
	Paths        []PathSpec
	HandshakeTO  time.Duration
	HandshakeTry int
}

// DialClient performs the Noise_IK handshake on path 0, then registers any additional
// paths from cfg.Paths via PATH_ADD, and returns a ready multi-path ClientTunnel. It does
// not touch the TUN device's IP/routes -- see core/tun's platform helpers, invoked by the
// caller (cmd/bondify) using the returned Cfg.
func DialClient(ctx context.Context, cfg ClientConfig, dev tun.Device, mtu int) (*ClientTunnel, HandshakeRespPayload, error) {
	raddr, err := net.ResolveUDPAddr("udp", cfg.RelayAddr)
	if err != nil {
		return nil, HandshakeRespPayload{}, fmt.Errorf("bond: resolve relay addr: %w", err)
	}

	paths := cfg.Paths
	if len(paths) == 0 {
		paths = []PathSpec{{}}
	}

	conn0, err := dialPath(ctx, paths[0], raddr)
	if err != nil {
		return nil, HandshakeRespPayload{}, err
	}

	init, err := crypto.NewInitiator(cfg.ClientKey, cfg.RelayPubKey)
	if err != nil {
		_ = conn0.Close()
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
		_ = conn0.Close()
		return nil, HandshakeRespPayload{}, fmt.Errorf("bond: write handshake init: %w", err)
	}
	initPkt := make([]byte, proto.OuterPrefixLen+len(initMsg))
	if err := proto.MarshalOuter(initPkt, proto.OuterHeader{Type: proto.TypeHandshakeInit, Version: proto.Version}); err != nil {
		_ = conn0.Close()
		return nil, HandshakeRespPayload{}, err
	}
	copy(initPkt[proto.OuterPrefixLen:], initMsg)

	respBuf := make([]byte, 2048)
	var respPayload HandshakeRespPayload
	var sess *crypto.Session
	var lastErr error
	for attempt := 0; attempt < tries; attempt++ {
		if _, err := conn0.Write(initPkt); err != nil {
			lastErr = fmt.Errorf("bond: send handshake init: %w", err)
			continue
		}
		if err := conn0.SetReadDeadline(time.Now().Add(handshakeTO)); err != nil {
			lastErr = fmt.Errorf("bond: set read deadline: %w", err)
			continue
		}
		n, err := conn0.Read(respBuf)
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
	_ = conn0.SetReadDeadline(time.Time{})
	if lastErr != nil {
		_ = conn0.Close()
		return nil, HandshakeRespPayload{}, lastErr
	}

	path0 := NewPath(0, conn0)
	path0.SetActive() // handshake completion is path 0's implicit ack

	t := &ClientTunnel{
		relayAddr:    raddr,
		dev:          dev,
		sess:         sess,
		sessionIndex: respPayload.SessionIndex,
		tunnelIP:     respPayload.TunnelIP,
		startedAt:    time.Now(),
		mtu:          mtu,
		sched:        sched.NewRoundRobin(),
		reorderBuf:   reorder.New(reorder.DefaultDeadlineMin, 0),
		paths:        []*Path{path0},
	}

	for i := 1; i < len(paths); i++ {
		if err := t.addPath(ctx, uint8(i), paths[i], handshakeTO, tries); err != nil {
			// A path that fails to join doesn't sink the whole tunnel -- log-worthy but
			// non-fatal; the caller still gets a working (degraded-capacity) tunnel.
			// cmd/bondify surfaces this via the returned error slice in a future pass;
			// for now the session simply proceeds with fewer paths than requested.
			t.pathErrs = append(t.pathErrs, fmt.Errorf("path %d (%s): %w", i, paths[i].LocalAddr, err))
		}
	}

	return t, respPayload, nil
}

func dialPath(ctx context.Context, spec PathSpec, raddr *net.UDPAddr) (*net.UDPConn, error) {
	var laddr *net.UDPAddr
	if spec.LocalAddr != "" {
		ip := net.ParseIP(spec.LocalAddr)
		if ip == nil {
			return nil, fmt.Errorf("bond: bad local address %q", spec.LocalAddr)
		}
		laddr = &net.UDPAddr{IP: ip}
	}
	conn, err := tun.DialUDPViaDevice(ctx, laddr, raddr, spec.Device)
	if err != nil {
		return nil, fmt.Errorf("bond: dial path (local=%q device=%q): %w", spec.LocalAddr, spec.Device, err)
	}
	return conn, nil
}

// addPath opens a new UDP socket for spec and registers it via PATH_ADD.
func (t *ClientTunnel) addPath(ctx context.Context, id uint8, spec PathSpec, timeout time.Duration, tries int) error {
	conn, err := dialPath(ctx, spec, t.relayAddr)
	if err != nil {
		return err
	}
	p := NewPath(id, conn)

	payload, err := marshalCBOR(PathAddPayload{PathID: id})
	if err != nil {
		_ = conn.Close()
		return err
	}
	pkt, err := sealControl(t.sess, proto.TypePathAdd, t.sessionIndex, id, payload)
	if err != nil {
		_ = conn.Close()
		return err
	}

	respBuf := make([]byte, 2048)
	var lastErr error
	acked := false
	for attempt := 0; attempt < tries; attempt++ {
		if _, err := conn.Write(pkt); err != nil {
			lastErr = err
			continue
		}
		if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
			lastErr = err
			continue
		}
		n, err := conn.Read(respBuf)
		if err != nil {
			lastErr = fmt.Errorf("path_add ack timeout: %w", err)
			continue
		}
		oh, consumed, err := proto.UnmarshalOuter(respBuf[:n])
		if err != nil || oh.Type != proto.TypeCtrl {
			lastErr = fmt.Errorf("unexpected path_add response")
			continue
		}
		ctrlPayload, err := openControl(t.sess, oh, id, respBuf[consumed:n])
		if err != nil {
			lastErr = fmt.Errorf("open path_add ack: %w", err)
			continue
		}
		var ack CtrlPathAddAck
		if err := unmarshalCBOR(ctrlPayload, &ack); err != nil || ack.Kind != "path_add_ack" || ack.PathID != id {
			lastErr = fmt.Errorf("malformed path_add ack")
			continue
		}
		acked = true
		lastErr = nil
		break
	}
	_ = conn.SetReadDeadline(time.Time{})
	if !acked {
		_ = conn.Close()
		return lastErr
	}

	p.SetActive()
	t.pathsMu.Lock()
	t.paths = append(t.paths, p)
	t.pathsMu.Unlock()
	return nil
}

// ClientTunnel is the client-side multi-path data-plane engine.
type ClientTunnel struct {
	relayAddr    *net.UDPAddr
	dev          tun.Device
	sess         *crypto.Session
	sessionIndex uint32
	tunnelIP     string
	startedAt    time.Time
	mtu          int

	sched      sched.Scheduler
	reorderBuf *reorder.Buffer

	pathsMu sync.RWMutex
	paths   []*Path

	pathErrs []error

	sendGSN atomic.Uint64

	Stats Stats
}

// Stats are simple atomic counters. Per-path breakdowns live on Path.Stats; these are the
// tunnel-wide totals for a quick top-level view.
type Stats struct {
	TxPackets uint64
	RxPackets uint64
	TxBytes   uint64
	RxBytes   uint64
	RxErrors  uint64
}

// Paths returns a snapshot of the current path set.
func (t *ClientTunnel) Paths() []*Path {
	t.pathsMu.RLock()
	defer t.pathsMu.RUnlock()
	out := make([]*Path, len(t.paths))
	copy(out, t.paths)
	return out
}

// PathErrors returns any errors encountered adding paths during DialClient. A non-empty
// result means the tunnel is running with fewer paths than requested, not that it's down.
func (t *ClientTunnel) PathErrors() []error { return t.pathErrs }

func (t *ClientTunnel) schedPaths() []sched.Path {
	paths := t.Paths()
	out := make([]sched.Path, len(paths))
	for i, p := range paths {
		out[i] = p
	}
	return out
}

// Run pumps packets across all paths until ctx is cancelled or a fatal error occurs.
func (t *ClientTunnel) Run(ctx context.Context) error {
	paths := t.Paths()
	errCh := make(chan error, len(paths)+2)

	go func() { errCh <- t.tunToNet(ctx) }()
	go func() { errCh <- t.drainReorderToTun(ctx) }()
	for _, p := range paths {
		p := p
		go func() { errCh <- t.pathReadLoop(ctx, p) }()
		go t.probeLoop(ctx, p)
	}

	select {
	case <-ctx.Done():
		t.closeAll()
		<-errCh
		return ctx.Err()
	case err := <-errCh:
		t.closeAll()
		return err
	}
}

func (t *ClientTunnel) closeAll() {
	_ = t.dev.Close()
	for _, p := range t.Paths() {
		_ = p.conn.Close()
	}
}

func (t *ClientTunnel) tunToNet(ctx context.Context) error {
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
			path := t.sched.Next(t.schedPaths())
			if path == nil {
				continue // no eligible path right now; drop (queueing is a later refinement)
			}
			p := path.(*Path)
			gsn := t.sendGSN.Add(1) - 1
			psn := p.NextSendPSN()
			pkt, err := sealPacket(t.sess, proto.TypeData, t.sessionIndex, p.id, proto.InnerDataHeader{
				GSN:        gsn,
				PSN:        psn,
				PathID:     p.id,
				PayloadLen: uint16(len(payload)),
			}, payload)
			if err != nil {
				return fmt.Errorf("bond: seal data: %w", err)
			}
			if _, err := p.conn.Write(pkt); err != nil {
				continue // this path's socket errored; let its read loop notice and report
			}
			atomic.AddUint64(&t.Stats.TxPackets, 1)
			atomic.AddUint64(&t.Stats.TxBytes, uint64(len(payload)))
			atomic.AddUint64(&p.Stats.TxPackets, 1)
			atomic.AddUint64(&p.Stats.TxBytes, uint64(len(payload)))
		}
	}
}

func (t *ClientTunnel) pathReadLoop(ctx context.Context, p *Path) error {
	buf := make([]byte, 65536)
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		n, err := p.conn.Read(buf)
		if err != nil {
			return fmt.Errorf("bond: udp read (path %d): %w", p.id, err)
		}
		oh, consumed, err := proto.UnmarshalOuter(buf[:n])
		if err != nil {
			continue
		}
		switch oh.Type {
		case proto.TypeData:
			inner, payload, err := openPacket(t.sess, oh, p.id, buf[consumed:n])
			if err != nil {
				atomic.AddUint64(&t.Stats.RxErrors, 1)
				continue
			}
			p.RecordRecv()
			atomic.AddUint64(&t.Stats.RxPackets, 1)
			atomic.AddUint64(&t.Stats.RxBytes, uint64(len(payload)))
			atomic.AddUint64(&p.Stats.RxPackets, 1)
			atomic.AddUint64(&p.Stats.RxBytes, uint64(len(payload)))
			cp := append([]byte(nil), payload...)
			t.reorderBuf.Push(reorder.Packet{GSN: inner.GSN, Payload: cp, Push: proto.HasFlag(inner.Flags, proto.FlagPUSH)})
		case proto.TypeProbeAck:
			payload, err := openControl(t.sess, oh, p.id, buf[consumed:n])
			if err != nil {
				continue
			}
			var ack ProbeAckPayload
			if err := unmarshalCBOR(payload, &ack); err != nil {
				continue
			}
			p.HandleProbeAck(ack, time.Now())
		case proto.TypeProbe:
			payload, err := openControl(t.sess, oh, p.id, buf[consumed:n])
			if err != nil {
				continue
			}
			var probe ProbePayload
			if err := unmarshalCBOR(payload, &probe); err != nil {
				continue
			}
			recvPSN := p.RecordRecv()
			ackPayload, err := marshalCBOR(ProbeAckPayload{SentAtUnixNano: probe.SentAtUnixNano, SentPSN: probe.SentPSN, RecvPSN: recvPSN})
			if err != nil {
				continue
			}
			ackPkt, err := sealControl(t.sess, proto.TypeProbeAck, t.sessionIndex, p.id, ackPayload)
			if err != nil {
				continue
			}
			_, _ = p.conn.Write(ackPkt)
		default:
			// PATH_ADD/PATH_DROP/CTRL kinds not yet meaningful post-setup on the client
			// side in phase 2.
		}
	}
}

func (t *ClientTunnel) drainReorderToTun(ctx context.Context) error {
	writeBuf := make([]byte, tun.IOOffset+65536)
	writeBufs := make([][]byte, 1)
	for {
		select {
		case <-ctx.Done():
			return nil
		case pkt := <-t.reorderBuf.Out():
			n := copy(writeBuf[tun.IOOffset:], pkt.Payload)
			writeBufs[0] = writeBuf[:tun.IOOffset+n]
			if _, err := t.dev.Write(writeBufs, tun.IOOffset); err != nil {
				return fmt.Errorf("bond: tun write: %w", err)
			}
		}
	}
}

// probeLoop sends PROBE on p every ProbeInterval, backing off to ProbeIdleInterval after
// ProbeIdleAfter of no traffic, and reports missed ACKs to the path's state machine.
func (t *ClientTunnel) probeLoop(ctx context.Context, p *Path) {
	interval := ProbeInterval
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var lastSentAt time.Time
	var lastAckSeen int64
	first := true

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if p.State() == sched.StateDead {
			return
		}

		if !first {
			// If no PROBE_ACK arrived since we sent the last probe, that's a miss.
			if p.lastProbeAckAt.Load() == lastAckSeen {
				p.HandleProbeMissed()
			}
		}
		first = false

		probe := p.BuildProbe()
		payload, err := marshalCBOR(probe)
		if err == nil {
			pkt, err := sealControl(t.sess, proto.TypeProbe, t.sessionIndex, p.id, payload)
			if err == nil {
				_, _ = p.conn.Write(pkt)
			}
		}
		lastSentAt = time.Now()
		lastAckSeen = p.lastProbeAckAt.Load()

		idle := time.Since(lastSentAt) > ProbeIdleAfter
		want := ProbeInterval
		if idle {
			want = ProbeIdleInterval
		}
		if want != interval {
			interval = want
			ticker.Reset(interval)
		}
	}
}
