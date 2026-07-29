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
	// distinct default route. Ignored if Conn is set.
	Device string
	// Conn, if non-nil, is used directly as this path's UDP socket instead of dialing one
	// internally (LocalAddr/Device are then ignored). Needed on platforms where Go itself
	// has no privilege to pin a socket to a specific physical network: Android exposes that
	// only as ConnectivityManager.Network.bindSocket, callable from Kotlin/Java, not
	// SO_BINDTODEVICE from Go. The platform layer there dials, network-binds, and
	// VpnService.protect()s the socket itself and hands the connected result in here (see
	// mobile/mobile.go and android/app's BondifyVpnService.kt).
	Conn *net.UDPConn
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
	// Scheduler selects the scheduling tier (core/sched.New's names: "round-robin",
	// "weighted-goodput", "min-rtt-cwnd", "hol-aware"). Empty defaults to round-robin.
	Scheduler string
	// Mode selects how outgoing packets are sent: ModeSpeed (default, one path per
	// packet, optionally FEC-protected) or ModeRedundant (duplicated onto DupFactor
	// distinct paths; FEC is not applied in this mode -- see ARCHITECTURE.md §2.3/README's
	// "seven laws" on REDUNDANT's data-usage tradeoff).
	Mode Mode
	// FEC enables adaptive Reed-Solomon forward error correction (ARCHITECTURE.md §2.3)
	// on outgoing SPEED-mode traffic. Redundancy scales with observed loss and is zero at
	// zero loss, so leaving this on costs nothing on a clean path.
	FEC bool
}

// DialClient performs the Noise_IK handshake on path 0, then registers any additional
// paths from cfg.Paths via PATH_ADD, and returns a ready multi-path ClientTunnel. It does
// not touch the TUN device's IP/routes -- see core/tun's platform helpers, invoked by the
// caller (cmd/bondify) using the returned Cfg.
//
// dev/mtu are accepted here purely for convenience (most callers have both ready up front)
// and just get forwarded to AttachTUN before returning -- see its doc comment for why a
// platform that can't build its TUN interface until it already knows the relay-assigned
// tunnel IP (Android's VpnService.Builder is fixed at construction time) needs the
// handshake-then-attach split DialHandshake+AttachTUN expose instead of calling this.
func DialClient(ctx context.Context, cfg ClientConfig, dev tun.Device, mtu int) (*ClientTunnel, HandshakeRespPayload, error) {
	t, resp, err := DialHandshake(ctx, cfg)
	if err != nil {
		return nil, resp, err
	}
	t.AttachTUN(dev, mtu)
	return t, resp, nil
}

// DialHandshake does everything DialClient does except attach a TUN device: the Noise_IK
// handshake on path 0, PATH_ADD for any additional cfg.Paths, and full session setup. The
// returned ClientTunnel is not yet ready for Run -- call AttachTUN first. Split out for
// platforms where the TUN interface can't be built until the relay-assigned tunnel IP
// (returned here in HandshakeRespPayload.TunnelIP) is already known; see AttachTUN's doc
// comment.
func DialHandshake(ctx context.Context, cfg ClientConfig) (*ClientTunnel, HandshakeRespPayload, error) {
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

	scheduler, err := sched.New(cfg.Scheduler)
	if err != nil {
		_ = conn0.Close()
		return nil, HandshakeRespPayload{}, fmt.Errorf("bond: %w", err)
	}

	path0 := NewPath(0, conn0)
	path0.SetActive() // handshake completion is path 0's implicit ack

	t := &ClientTunnel{
		relayAddr:    raddr,
		sess:         sess,
		sessionIndex: respPayload.SessionIndex,
		tunnelIP:     respPayload.TunnelIP,
		startedAt:    time.Now(),
		sched:        scheduler,
		reorderBuf:   reorder.New(reorder.DefaultDeadlineMin, 0),
		ack:          newACKState(),
		rtx:          newRetransmitQueue(),
		paths:        []*Path{path0},
		mode:         cfg.Mode,
	}
	if cfg.FEC {
		t.fecSend = newFECSender(t.fecLossEstimate, t.sendFECParity)
		t.fecRecv = newFECGenBuffer()
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

// AttachTUN finishes setting up a ClientTunnel returned by DialHandshake, giving it the TUN
// device and MTU that Run's packet pump needs. Must be called exactly once, before Run --
// DialClient does this immediately for callers that already have both ready; a caller that
// needs the handshake's result (e.g. the relay-assigned tunnel IP) before it can even build
// its TUN device -- Android's VpnService.Builder is fixed at construction and can't be
// reconfigured afterward, unlike a Linux TUN interface's IP/routes which are set well after
// the device itself is opened -- calls DialHandshake and this separately instead.
func (t *ClientTunnel) AttachTUN(dev tun.Device, mtu int) {
	t.dev = dev
	t.mtu = mtu
}

func dialPath(ctx context.Context, spec PathSpec, raddr *net.UDPAddr) (*net.UDPConn, error) {
	if spec.Conn != nil {
		return spec.Conn, nil
	}
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
	view := make([]sched.Path, len(t.paths))
	for i, path := range t.paths {
		view[i] = path
	}
	t.schedPathView = view
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
	ack        *ackState
	rtx        *retransmitQueue
	ackSendMu  sync.Mutex

	mode    Mode
	fecSend *fecSender    // nil when FEC is disabled
	fecRecv *fecGenBuffer // nil when FEC is disabled

	pathsMu sync.RWMutex
	paths   []*Path
	// schedPathView is immutable and replaced whenever a path is added, avoiding two
	// slice allocations for every packet sent through the scheduler.
	schedPathView []sched.Path

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
	TxAcks    uint64
	RxAcks    uint64
	TxRetries uint64
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
	t.pathsMu.RLock()
	defer t.pathsMu.RUnlock()
	return t.schedPathView
}

// Run pumps packets across all paths until ctx is cancelled or a fatal error occurs.
func (t *ClientTunnel) Run(ctx context.Context) error {
	paths := t.Paths()
	errCh := make(chan error, len(paths)+2)

	go func() { errCh <- t.tunToNet(ctx) }()
	go func() { errCh <- t.drainReorderToTun(ctx) }()
	go t.maintenanceLoop(ctx)
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
			if t.mode == ModeRedundant {
				t.sendRedundant(payload)
			} else {
				t.sendSpeed(payload)
			}
		}
	}
}

// sendSpeed sends payload on the scheduler's chosen single path, stamping FEC generation
// info and FlagFECProtected when FEC is enabled.
func (t *ClientTunnel) sendSpeed(payload []byte) {
	path := t.sched.Next(t.schedPaths(), len(payload))
	if path == nil {
		return // no eligible path right now; drop (queueing is a later refinement)
	}
	p := path.(*Path)
	gsn := t.sendGSN.Add(1) - 1
	psn := p.NextSendPSN()

	header := proto.InnerDataHeader{
		GSN:        gsn,
		PSN:        psn,
		PathID:     p.id,
		PayloadLen: uint16(len(payload)),
	}
	var fecGenID uint16
	var fecGenIndex int
	var fecInner []byte
	if t.fecSend != nil {
		fecGenID, fecGenIndex = t.fecSend.NextSlot()
		header.GenerationID = fecGenID
		header.GenIndex = uint8(fecGenIndex)
		header.Flags |= proto.FlagFECProtected
		buf := make([]byte, proto.InnerHeaderLen+len(payload))
		if err := proto.MarshalInner(buf, header); err == nil {
			copy(buf[proto.InnerHeaderLen:], payload)
			fecInner = buf
		}
	}

	pkt, err := sealPacket(t.sess, proto.TypeData, t.sessionIndex, p.id, header, payload)
	if err != nil {
		return
	}
	if _, err := p.conn.Write(pkt); err != nil {
		return // this path's socket errored; let its read loop notice and report
	}
	atomic.AddUint64(&t.Stats.TxPackets, 1)
	atomic.AddUint64(&t.Stats.TxBytes, uint64(len(payload)))
	atomic.AddUint64(&p.Stats.TxPackets, 1)
	atomic.AddUint64(&p.Stats.TxBytes, uint64(len(payload)))
	t.rtx.Track(gsn, payload, header.Flags, time.Now())

	// Record (and, on the fec.K-th packet, close and emit parity for) this generation only
	// after the packet itself is actually on the wire. Doing it earlier let the Kth
	// packet's own parity reach the receiver before the Kth packet itself did, making the
	// receiver treat an in-flight-but-not-yet-arrived packet as "missing" and spend one of
	// the generation's real reconstruction slots recovering it needlessly -- robbing
	// capacity from an actual loss elsewhere in the same generation. Found for real: the
	// phase 4 gate measured far higher residual loss than FEC's own math predicted for its
	// configured redundancy, until this fix.
	if fecInner != nil {
		t.fecSend.Record(fecGenID, fecGenIndex, fecInner)
	}
}

// sendRedundant duplicates payload, under one shared GSN, onto up to DupFactor distinct
// healthy paths (PROTOCOL.md's DUP_FACTOR). The reorder buffer's existing
// GSN<nextExpected check dedups the extra copies at the receiver for free -- see
// PROTOCOL.md §3's note that replay protection doubles as REDUNDANT-mode dedup.
func (t *ClientTunnel) sendRedundant(payload []byte) {
	paths := selectRedundantPaths(t.Paths(), DupFactor)
	if len(paths) == 0 {
		return
	}
	gsn := t.sendGSN.Add(1) - 1
	sent := false
	for i, p := range paths {
		psn := p.NextSendPSN()
		var flags uint8
		if i > 0 {
			flags |= proto.FlagDUP
		}
		pkt, err := sealPacket(t.sess, proto.TypeData, t.sessionIndex, p.id, proto.InnerDataHeader{
			GSN:        gsn,
			PSN:        psn,
			PathID:     p.id,
			Flags:      flags,
			PayloadLen: uint16(len(payload)),
		}, payload)
		if err != nil {
			continue
		}
		if _, err := p.conn.Write(pkt); err != nil {
			continue
		}
		atomic.AddUint64(&p.Stats.TxPackets, 1)
		atomic.AddUint64(&p.Stats.TxBytes, uint64(len(payload)))
		sent = true
	}
	if !sent {
		return
	}
	// Tunnel-wide Stats count the logical packet once, not once per duplicate: it
	// represents one user packet sent, matching how Stats.RxPackets counts an arrival
	// once regardless of how many duplicate copies the receiver's dedup discarded.
	atomic.AddUint64(&t.Stats.TxPackets, 1)
	atomic.AddUint64(&t.Stats.TxBytes, uint64(len(payload)))
	t.rtx.Track(gsn, payload, 0, time.Now())
}

// fecLossEstimate is the redundancy input for FEC generations closing on this tunnel: the
// maximum Loss() among ACTIVE+BOND paths. Max, not average or min, because SPEED mode's
// scheduler may route a generation's packets across several paths, and the generation
// needs enough parity to survive whichever of those paths is worst right now.
func (t *ClientTunnel) fecLossEstimate() float64 {
	var maxLoss float64
	for _, p := range t.Paths() {
		if p.State() != sched.StateActive || p.Role() != sched.RoleBond {
			continue
		}
		if l := p.Loss(); l > maxLoss {
			maxLoss = l
		}
	}
	return maxLoss
}

// sendFECParity seals and sends one parity shard on the healthiest currently ACTIVE+BOND
// path (ARCHITECTURE.md §2.3: "Parity goes on the least loaded path, never the lossy
// one"). A generation with no eligible path to carry parity simply loses that
// generation's FEC protection -- the data packets it protects were already sent on their
// own regardless, so this never blocks the data plane.
func (t *ClientTunnel) sendFECParity(genID uint16, genIndex, n, m, w int, shard []byte) {
	p := healthiestPath(t.Paths())
	if p == nil {
		return
	}
	fecPayload := make([]byte, proto.FECHeaderLen+len(shard))
	if err := proto.MarshalFECHeader(fecPayload, proto.FECHeader{N: uint8(n), M: uint8(m), W: uint16(w)}); err != nil {
		return
	}
	copy(fecPayload[proto.FECHeaderLen:], shard)

	psn := p.NextSendPSN()
	pkt, err := sealPacket(t.sess, proto.TypeFEC, t.sessionIndex, p.id, proto.InnerDataHeader{
		PSN:          psn,
		PathID:       p.id,
		PayloadLen:   uint16(len(fecPayload)),
		GenerationID: genID,
		GenIndex:     uint8(genIndex),
	}, fecPayload)
	if err != nil {
		return
	}
	if _, err := p.conn.Write(pkt); err != nil {
		return // this path's socket errored; let its read loop notice and report
	}
}

// maintenanceLoop drives delayed ACKs, bounded retransmission, partial FEC generation
// flushes, and receive-side FEC garbage collection.
func (t *ClientTunnel) maintenanceLoop(ctx context.Context) {
	ticker := time.NewTicker(RetransmitTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		now := time.Now()
		t.sendACKIfDue(now)
		paths := t.Paths()
		if lowestRTTActivePath(paths) != nil {
			for _, pkt := range t.rtx.Due(now, retransmitRTO(paths)) {
				t.retransmit(pkt)
			}
		}
		if t.fecSend != nil {
			t.fecSend.Flush(FECGenTimeout)
		}
		if t.fecRecv != nil {
			t.fecRecv.GC(time.Second)
		}
	}
}

func (t *ClientTunnel) sendACKIfDue(now time.Time) {
	t.ackSendMu.Lock()
	defer t.ackSendMu.Unlock()

	snapshot, ok := t.ack.SnapshotIfDue(now)
	if !ok {
		return
	}
	paths := t.Paths()
	p := lowestRTTActivePath(paths)
	if p == nil {
		return
	}
	fillACKReceiverState(&snapshot.payload, paths, t.reorderBuf)
	payload, err := marshalCBOR(snapshot.payload)
	if err != nil {
		return
	}
	pkt, err := sealControl(t.sess, proto.TypeAck, t.sessionIndex, p.id, payload)
	if err != nil {
		return
	}
	if _, err := p.conn.Write(pkt); err != nil {
		return
	}
	t.ack.MarkSent(snapshot.version, time.Now())
	atomic.AddUint64(&t.Stats.TxAcks, 1)
}

func (t *ClientTunnel) retransmit(pending pendingPacket) {
	p := lowestRTTActivePath(t.Paths())
	if p == nil {
		return
	}
	header := proto.InnerDataHeader{
		GSN:        pending.GSN,
		PSN:        p.NextSendPSN(),
		PathID:     p.id,
		Flags:      pending.Flags | proto.FlagRTX,
		PayloadLen: uint16(len(pending.Payload)),
	}
	pkt, err := sealPacket(t.sess, proto.TypeData, t.sessionIndex, p.id, header, pending.Payload)
	if err != nil {
		return
	}
	if _, err := p.conn.Write(pkt); err != nil {
		return
	}
	atomic.AddUint64(&t.Stats.TxRetries, 1)
	atomic.AddUint64(&p.Stats.TxPackets, 1)
	atomic.AddUint64(&p.Stats.TxBytes, uint64(len(pending.Payload)))
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
			t.ack.Observe(inner.GSN, time.Now())
			t.sendACKIfDue(time.Now())
			if t.fecRecv != nil && proto.HasFlag(inner.Flags, proto.FlagFECProtected) {
				plain := make([]byte, proto.InnerHeaderLen+len(payload))
				if err := proto.MarshalInner(plain, inner); err == nil {
					copy(plain[proto.InnerHeaderLen:], payload)
					t.fecRecv.HandleData(inner.GenerationID, int(inner.GenIndex), plain)
				}
			}
		case proto.TypeFEC:
			inner, payload, err := openPacket(t.sess, oh, p.id, buf[consumed:n])
			if err != nil {
				atomic.AddUint64(&t.Stats.RxErrors, 1)
				continue
			}
			p.RecordRecv() // FEC packets consume PSN on send too; keep loss accounting consistent
			if t.fecRecv == nil {
				continue // FEC disabled locally; nothing to do with an unsolicited parity packet
			}
			fh, fhLen, err := proto.UnmarshalFECHeader(payload)
			if err != nil {
				continue
			}
			shard := payload[fhLen:]
			parityIndex := int(inner.GenIndex) - int(fh.N)
			recovered := t.fecRecv.HandleFEC(inner.GenerationID, int(fh.N), int(fh.M), int(fh.W), parityIndex, shard)
			for _, plain := range recovered {
				h, rpayload, ok := unmarshalRecovered(plain)
				if !ok {
					continue
				}
				atomic.AddUint64(&t.Stats.RxPackets, 1)
				atomic.AddUint64(&t.Stats.RxBytes, uint64(len(rpayload)))
				t.reorderBuf.Push(reorder.Packet{GSN: h.GSN, Payload: rpayload, Push: proto.HasFlag(h.Flags, proto.FlagPUSH)})
				t.ack.Observe(h.GSN, time.Now())
				t.sendACKIfDue(time.Now())
			}
		case proto.TypeAck:
			payload, err := openControl(t.sess, oh, p.id, buf[consumed:n])
			if err != nil {
				continue
			}
			var ack AckPayload
			if err := unmarshalCBOR(payload, &ack); err != nil {
				continue
			}
			applyACKPathState(ack, t.Paths())
			t.rtx.Acknowledge(ack, time.Now())
			atomic.AddUint64(&t.Stats.RxAcks, 1)
		case proto.TypeProbeAck:
			payload, err := openControl(t.sess, oh, p.id, buf[consumed:n])
			if err != nil {
				continue
			}
			var ack ProbeAckPayload
			if err := unmarshalCBOR(payload, &ack); err != nil {
				continue
			}
			now := time.Now()
			p.HandleProbeAck(ack, now)
			t.ack.RequestFeedback(now)
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
