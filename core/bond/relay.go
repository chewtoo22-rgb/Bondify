package bond

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"log"
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

// RelayConfig configures the relay's single UDP listener and shared TUN device.
type RelayConfig struct {
	ListenAddr string // host:port, e.g. ":51820"
	RelayKey   crypto.Keypair
	PoolCIDR   string   // e.g. "10.77.0.0/24"
	DNS        []string // pushed to clients via cfg_push
	MTU        int
	KeepAlive  int
	// Scheduler selects the scheduling tier used for the relay's own return traffic
	// (core/sched.New's names). Empty defaults to round-robin.
	Scheduler string
	// Mode selects how the relay sends its own return (relay->client) traffic. See
	// ClientConfig.Mode.
	Mode Mode
	// FEC enables adaptive FEC on the relay's own return traffic. See ClientConfig.FEC.
	FEC bool
}

type relaySession struct {
	sessionIndex uint32
	sess         *crypto.Session
	tunnelIP     net.IP
	startedAt    time.Time

	pathsMu sync.RWMutex
	paths   map[uint8]*Path

	sched      sched.Scheduler
	reorderBuf *reorder.Buffer

	mode    Mode
	fecSend *fecSender    // nil when FEC is disabled
	fecRecv *fecGenBuffer // nil when FEC is disabled

	sendGSN atomic.Uint64

	Stats Stats
}

func newRelaySession(r *Relay, sessionIndex uint32, sess *crypto.Session, tunnelIP net.IP, cfg RelayConfig) *relaySession {
	scheduler, err := sched.New(cfg.Scheduler)
	if err != nil {
		// Already validated once in NewRelay; an unknown name here would be a caller bug,
		// not a runtime condition worth failing a session over. Fall back to the always-
		// correct baseline rather than propagate an error deep into handshake handling.
		scheduler = sched.NewRoundRobin()
	}
	rs := &relaySession{
		sessionIndex: sessionIndex,
		sess:         sess,
		tunnelIP:     tunnelIP,
		startedAt:    time.Now(),
		paths:        make(map[uint8]*Path),
		sched:        scheduler,
		reorderBuf:   reorder.New(reorder.DefaultDeadlineMin, 0),
		mode:         cfg.Mode,
	}
	if cfg.FEC {
		rs.fecSend = newFECSender(rs.fecLossEstimate, func(genID uint16, genIndex, n, m, w int, shard []byte) {
			r.sendFECParity(rs, genID, genIndex, n, m, w, shard)
		})
		rs.fecRecv = newFECGenBuffer()
	}
	return rs
}

// pathSlice returns a snapshot of this session's current path set as concrete *Path
// values, for callers (REDUNDANT/FEC path selection) that need core/bond's own methods
// rather than the sched.Path interface view schedPaths() provides.
func (rs *relaySession) pathSlice() []*Path {
	rs.pathsMu.RLock()
	defer rs.pathsMu.RUnlock()
	out := make([]*Path, 0, len(rs.paths))
	for _, p := range rs.paths {
		out = append(out, p)
	}
	return out
}

// fecLossEstimate mirrors ClientTunnel.fecLossEstimate for the relay's own outgoing
// traffic. Known limitation, same root cause as CWND's relay-side asymmetry (see
// ARCHITECTURE.md §9): only the client actively probes today, so a relay-side path's
// Loss() never advances off zero, and this therefore never drives real redundancy for the
// relay's own sends. Symmetric relay-initiated probing (already flagged as future work)
// would close this gap; until then, relay-side FEC is present and correct but inert.
func (rs *relaySession) fecLossEstimate() float64 {
	var maxLoss float64
	for _, p := range rs.pathSlice() {
		if p.State() != sched.StateActive || p.Role() != sched.RoleBond {
			continue
		}
		if l := p.Loss(); l > maxLoss {
			maxLoss = l
		}
	}
	return maxLoss
}

func (rs *relaySession) getOrCreatePath(id uint8, addr *net.UDPAddr) (*Path, bool) {
	rs.pathsMu.Lock()
	defer rs.pathsMu.Unlock()
	p, ok := rs.paths[id]
	if !ok {
		p = NewPath(id, nil) // relay paths share the listener socket; no dedicated conn
		p.SetRemoteAddr(addr)
		rs.paths[id] = p
		return p, true
	}
	return p, false
}

func (rs *relaySession) schedPaths() []sched.Path {
	rs.pathsMu.RLock()
	defer rs.pathsMu.RUnlock()
	out := make([]sched.Path, 0, len(rs.paths))
	for _, p := range rs.paths {
		out = append(out, p)
	}
	return out
}

func (rs *relaySession) pathByID(id uint8) *Path {
	rs.pathsMu.RLock()
	defer rs.pathsMu.RUnlock()
	return rs.paths[id]
}

// Relay is the multi-path relay: one UDP socket demultiplexing by Session Index and Path
// ID, one shared TUN device for all sessions (the kernel's own routing/NAT handles getting
// decrypted packets to the real internet and back).
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
	if _, err := sched.New(cfg.Scheduler); err != nil {
		return nil, fmt.Errorf("bond: %w", err)
	}
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
	r := &Relay{
		cfg:         cfg,
		conn:        conn,
		dev:         dev,
		pool:        pool,
		byIndex:     make(map[uint32]*relaySession),
		byTunnelIP:  make(map[string]*relaySession),
		byClientKey: make(map[[crypto.KeyLen]byte]*relaySession),
	}
	go r.livenessLoop()
	return r, nil
}

// GatewayIP is the relay's own address inside the tunnel subnet.
func (r *Relay) GatewayIP() net.IP { return r.pool.Gateway() }

// livenessLoop marks relay-side paths DEAD after PathDeadTimeout of no traffic, so the
// relay's own round-robin stops wasting return-traffic capacity on a path the client has
// abandoned. See path.go's doc comment for why the relay uses this simpler signal instead
// of the full probe-driven state machine the client runs.
func (r *Relay) livenessLoop() {
	ticker := time.NewTicker(ProbeInterval)
	defer ticker.Stop()
	for range ticker.C {
		r.mu.RLock()
		sessions := make([]*relaySession, 0, len(r.byIndex))
		for _, s := range r.byIndex {
			sessions = append(sessions, s)
		}
		r.mu.RUnlock()
		for _, s := range sessions {
			s.pathsMu.RLock()
			paths := make([]*Path, 0, len(s.paths))
			for _, p := range s.paths {
				paths = append(paths, p)
			}
			s.pathsMu.RUnlock()
			for _, p := range paths {
				if p.State() != sched.StateDead && p.LastActivity() > PathDeadTimeout {
					p.state.Store(int32(sched.StateDead))
				}
			}
		}
	}
}

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
			if sess.mode == ModeRedundant {
				r.sendRedundant(sess, pkt)
			} else {
				r.sendSpeed(sess, pkt)
			}
		}
	}
}

// sendSpeed sends pkt on sess's scheduler-chosen single path, stamping FEC generation
// info and FlagFECProtected when FEC is enabled for this session.
func (r *Relay) sendSpeed(sess *relaySession, pkt []byte) {
	path := sess.sched.Next(sess.schedPaths(), len(pkt))
	if path == nil {
		return // no eligible path right now; drop
	}
	p := path.(*Path)
	addr := p.RemoteAddr()
	if addr == nil {
		return
	}
	gsn := sess.sendGSN.Add(1) - 1
	psn := p.NextSendPSN()

	header := proto.InnerDataHeader{GSN: gsn, PSN: psn, PathID: p.id, PayloadLen: uint16(len(pkt))}
	var fecGenID uint16
	var fecGenIndex int
	var fecInner []byte
	if sess.fecSend != nil {
		fecGenID, fecGenIndex = sess.fecSend.NextSlot()
		header.GenerationID = fecGenID
		header.GenIndex = uint8(fecGenIndex)
		header.Flags |= proto.FlagFECProtected
		buf := make([]byte, proto.InnerHeaderLen+len(pkt))
		if err := proto.MarshalInner(buf, header); err == nil {
			copy(buf[proto.InnerHeaderLen:], pkt)
			fecInner = buf
		}
	}

	out, err := sealPacket(sess.sess, proto.TypeData, sess.sessionIndex, p.id, header, pkt)
	if err != nil {
		log.Printf("bond: relay seal error: %v", err)
		return
	}
	if _, err := r.conn.WriteToUDP(out, addr); err != nil {
		log.Printf("bond: relay udp write error: %v", err)
		return
	}
	atomic.AddUint64(&r.Stats.TxPackets, 1)
	atomic.AddUint64(&r.Stats.TxBytes, uint64(len(pkt)))
	atomic.AddUint64(&p.Stats.TxPackets, 1)
	atomic.AddUint64(&p.Stats.TxBytes, uint64(len(pkt)))

	// See ClientTunnel.sendSpeed's comment: Record (and, on the Kth packet, close and emit
	// parity for) this generation only after the packet itself is actually on the wire.
	if fecInner != nil {
		sess.fecSend.Record(fecGenID, fecGenIndex, fecInner)
	}
}

// sendRedundant duplicates pkt, under one shared GSN, onto up to DupFactor distinct
// healthy paths for sess. See ClientTunnel.sendRedundant for the dedup rationale.
func (r *Relay) sendRedundant(sess *relaySession, pkt []byte) {
	paths := selectRedundantPaths(sess.pathSlice(), DupFactor)
	if len(paths) == 0 {
		return
	}
	gsn := sess.sendGSN.Add(1) - 1
	for i, p := range paths {
		addr := p.RemoteAddr()
		if addr == nil {
			continue
		}
		psn := p.NextSendPSN()
		var flags uint8
		if i > 0 {
			flags |= proto.FlagDUP
		}
		out, err := sealPacket(sess.sess, proto.TypeData, sess.sessionIndex, p.id, proto.InnerDataHeader{
			GSN: gsn, PSN: psn, PathID: p.id, Flags: flags, PayloadLen: uint16(len(pkt)),
		}, pkt)
		if err != nil {
			continue
		}
		if _, err := r.conn.WriteToUDP(out, addr); err != nil {
			continue
		}
		atomic.AddUint64(&p.Stats.TxPackets, 1)
		atomic.AddUint64(&p.Stats.TxBytes, uint64(len(pkt)))
	}
	atomic.AddUint64(&r.Stats.TxPackets, 1)
	atomic.AddUint64(&r.Stats.TxBytes, uint64(len(pkt)))
}

// sendFECParity seals and sends one parity shard for sess on the healthiest currently
// ACTIVE+BOND path. See ClientTunnel.sendFECParity.
func (r *Relay) sendFECParity(sess *relaySession, genID uint16, genIndex, n, m, w int, shard []byte) {
	p := healthiestPath(sess.pathSlice())
	if p == nil {
		return
	}
	addr := p.RemoteAddr()
	if addr == nil {
		return
	}
	fecPayload := make([]byte, proto.FECHeaderLen+len(shard))
	if err := proto.MarshalFECHeader(fecPayload, proto.FECHeader{N: uint8(n), M: uint8(m), W: uint16(w)}); err != nil {
		return
	}
	copy(fecPayload[proto.FECHeaderLen:], shard)

	psn := p.NextSendPSN()
	out, err := sealPacket(sess.sess, proto.TypeFEC, sess.sessionIndex, p.id, proto.InnerDataHeader{
		PSN:          psn,
		PathID:       p.id,
		PayloadLen:   uint16(len(fecPayload)),
		GenerationID: genID,
		GenIndex:     uint8(genIndex),
	}, fecPayload)
	if err != nil {
		return
	}
	_, _ = r.conn.WriteToUDP(out, addr)
}

// FECMaintenanceLoop periodically flushes partial send-side generations and evicts stale
// receive-side generation state, across every session that has FEC enabled. Mirrors
// ClientTunnel's internal fecMaintenanceLoop; the relay runs one shared loop for all
// sessions since, unlike the client, it doesn't already have one goroutine per session.
// Exported: cmd/bondify-relay starts it alongside ServeUDP/ServeTUN/ServeReorder.
func (r *Relay) FECMaintenanceLoop(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		r.mu.RLock()
		sessions := make([]*relaySession, 0, len(r.byIndex))
		for _, s := range r.byIndex {
			sessions = append(sessions, s)
		}
		r.mu.RUnlock()
		for _, s := range sessions {
			if s.fecSend != nil {
				s.fecSend.Flush(FECGenTimeout)
			}
			if s.fecRecv != nil {
				s.fecRecv.GC(time.Second)
			}
		}
	}
}

// ServeReorder drains every session's reorder buffer to the shared TUN device. Run as its
// own goroutine by cmd/bondify-relay alongside ServeUDP/ServeTUN.
func (r *Relay) ServeReorder(ctx context.Context) {
	// Polling the session table for new reorder buffers is simpler and safe under
	// concurrent session creation; sessions are few and this loop is cheap.
	seen := make(map[uint32]bool)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		r.mu.RLock()
		for idx, s := range r.byIndex {
			if !seen[idx] {
				seen[idx] = true
				go r.drainSessionReorder(s)
			}
		}
		r.mu.RUnlock()
	}
}

func (r *Relay) drainSessionReorder(s *relaySession) {
	writeBuf := make([]byte, tun.IOOffset+65536)
	writeBufs := make([][]byte, 1)
	for pkt := range s.reorderBuf.Out() {
		n := copy(writeBuf[tun.IOOffset:], pkt.Payload)
		writeBufs[0] = writeBuf[:tun.IOOffset+n]
		if _, err := r.dev.Write(writeBufs, tun.IOOffset); err != nil {
			log.Printf("bond: relay tun write error: %v", err)
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
	case proto.TypeFEC:
		r.handleFEC(oh, buf[consumed:], src)
	case proto.TypePathAdd:
		r.handlePathAdd(oh, buf[consumed:], src)
	case proto.TypeProbe:
		r.handleProbe(oh, buf[consumed:], src)
	case proto.TypeProbeAck:
		r.handleProbeAck(oh, buf[consumed:], src)
	default:
		// PATH_DROP/CTRL(other kinds) land in later phases.
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

	rs := newRelaySession(r, sessionIndex, sess, tunnelIP, r.cfg)
	p0, _ := rs.getOrCreatePath(PathZero, src)
	p0.SetActive()

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

// PathZero is the well-known path ID established implicitly by the handshake, before any
// PATH_ADD. Reserved for log/diagnostic clarity, not reused for a later real path.
const PathZero uint8 = 0

func (r *Relay) handlePathAdd(oh proto.OuterHeader, ciphertext []byte, src *net.UDPAddr) {
	r.mu.RLock()
	sess := r.byIndex[oh.SessionIndex]
	r.mu.RUnlock()
	if sess == nil {
		return
	}
	// PATH_ADD's own path ID isn't known yet (that's what we're learning), so it's
	// authenticated using the path ID it declares in its payload -- symmetric with how
	// the client picks pathID before sending. If AEAD verification fails under that
	// assumed ID, the packet is simply dropped like any other authentication failure.
	// We peek the ID by trying openControl with each declared candidate is unnecessary:
	// the sender already knows which pathID it's registering, so it stamps the AEAD
	// nonce's top byte with that same ID (see crypto.Session.Seal), and openControl only
	// needs that ID to construct the matching nonce for verification.
	pathID := ciphertext0PathIDHint(oh)
	payload, err := openControl(sess.sess, oh, pathID, ciphertext)
	if err != nil {
		return
	}
	var req PathAddPayload
	if err := unmarshalCBOR(payload, &req); err != nil {
		return
	}
	p, _ := sess.getOrCreatePath(req.PathID, src)
	p.SetActive()

	ackPayload, err := marshalCBOR(CtrlPathAddAck{Kind: "path_add_ack", PathID: req.PathID})
	if err != nil {
		return
	}
	ackPkt, err := sealControl(sess.sess, proto.TypeCtrl, sess.sessionIndex, req.PathID, ackPayload)
	if err != nil {
		return
	}
	if _, err := r.conn.WriteToUDP(ackPkt, src); err != nil {
		log.Printf("bond: send path_add ack: %v", err)
		return
	}
	log.Printf("bond: session %08x path %d added from %s", sess.sessionIndex, req.PathID, src)
}

// ciphertext0PathIDHint extracts the AEAD nonce's top byte (the path ID) from the outer
// header, which crypto.Session.Open needs as an input to reconstruct the nonce, not
// something it discovers from the plaintext -- see core/crypto/session.go's Open, which
// itself already asserts nonce[0]==pathID before attempting decryption.
func ciphertext0PathIDHint(oh proto.OuterHeader) uint8 { return oh.Nonce[0] }

func (r *Relay) handleProbe(oh proto.OuterHeader, ciphertext []byte, src *net.UDPAddr) {
	r.mu.RLock()
	sess := r.byIndex[oh.SessionIndex]
	r.mu.RUnlock()
	if sess == nil {
		return
	}
	pathID := ciphertext0PathIDHint(oh)
	payload, err := openControl(sess.sess, oh, pathID, ciphertext)
	if err != nil {
		return
	}
	var probe ProbePayload
	if err := unmarshalCBOR(payload, &probe); err != nil {
		return
	}
	p, isNew := sess.getOrCreatePath(pathID, src)
	if !isNew {
		p.SetRemoteAddr(src) // NAT rebinding: latest source wins (rate limiting lands with phase 3's full state machine)
	}
	recvPSN := p.RecordRecv()

	ackPayload, err := marshalCBOR(ProbeAckPayload{SentAtUnixNano: probe.SentAtUnixNano, SentPSN: probe.SentPSN, RecvPSN: recvPSN})
	if err != nil {
		return
	}
	ackPkt, err := sealControl(sess.sess, proto.TypeProbeAck, sess.sessionIndex, pathID, ackPayload)
	if err != nil {
		return
	}
	if _, err := r.conn.WriteToUDP(ackPkt, src); err != nil {
		log.Printf("bond: send probe ack: %v", err)
	}
}

func (r *Relay) handleProbeAck(oh proto.OuterHeader, ciphertext []byte, src *net.UDPAddr) {
	r.mu.RLock()
	sess := r.byIndex[oh.SessionIndex]
	r.mu.RUnlock()
	if sess == nil {
		return
	}
	pathID := ciphertext0PathIDHint(oh)
	p := sess.pathByID(pathID)
	if p == nil {
		return
	}
	payload, err := openControl(sess.sess, oh, pathID, ciphertext)
	if err != nil {
		return
	}
	var ack ProbeAckPayload
	if err := unmarshalCBOR(payload, &ack); err != nil {
		return
	}
	p.HandleProbeAck(ack, time.Now())
}

func (r *Relay) handleData(oh proto.OuterHeader, ciphertext []byte, src *net.UDPAddr) {
	r.mu.RLock()
	sess := r.byIndex[oh.SessionIndex]
	r.mu.RUnlock()
	if sess == nil {
		return
	}
	pathID := ciphertext0PathIDHint(oh)
	inner, payload, err := openPacket(sess.sess, oh, pathID, ciphertext)
	if err != nil {
		atomic.AddUint64(&sess.Stats.RxErrors, 1)
		atomic.AddUint64(&r.Stats.RxErrors, 1)
		return
	}

	p, isNew := sess.getOrCreatePath(pathID, src)
	if !isNew {
		// NAT rebinding (PROTOCOL.md §5): a known session, valid AEAD, new source
		// address is simply a path that moved. Rate-limiting updates to 1/s is phase 3
		// scope alongside the rest of the full spoofing-resistance state machine.
		cur := p.RemoteAddr()
		if cur == nil || !udpAddrEqual(cur, src) {
			p.SetRemoteAddr(src)
		}
	}
	p.RecordRecv()

	atomic.AddUint64(&sess.Stats.RxPackets, 1)
	atomic.AddUint64(&sess.Stats.RxBytes, uint64(len(payload)))
	atomic.AddUint64(&r.Stats.RxPackets, 1)
	atomic.AddUint64(&r.Stats.RxBytes, uint64(len(payload)))
	atomic.AddUint64(&p.Stats.RxPackets, 1)
	atomic.AddUint64(&p.Stats.RxBytes, uint64(len(payload)))

	cp := append([]byte(nil), payload...)
	sess.reorderBuf.Push(reorder.Packet{GSN: inner.GSN, Payload: cp, Push: proto.HasFlag(inner.Flags, proto.FlagPUSH)})

	if sess.fecRecv != nil && proto.HasFlag(inner.Flags, proto.FlagFECProtected) {
		plain := make([]byte, proto.InnerHeaderLen+len(payload))
		if err := proto.MarshalInner(plain, inner); err == nil {
			copy(plain[proto.InnerHeaderLen:], payload)
			sess.fecRecv.HandleData(inner.GenerationID, int(inner.GenIndex), plain)
		}
	}
}

// handleFEC processes an incoming FEC parity packet (client->relay direction), attempting
// reconstruction of any missing sibling data shard in the same generation and pushing
// anything recovered straight into the session's reorder buffer. Mirrors the TypeFEC case
// in ClientTunnel.pathReadLoop.
func (r *Relay) handleFEC(oh proto.OuterHeader, ciphertext []byte, src *net.UDPAddr) {
	r.mu.RLock()
	sess := r.byIndex[oh.SessionIndex]
	r.mu.RUnlock()
	if sess == nil {
		return
	}
	pathID := ciphertext0PathIDHint(oh)
	inner, payload, err := openPacket(sess.sess, oh, pathID, ciphertext)
	if err != nil {
		atomic.AddUint64(&sess.Stats.RxErrors, 1)
		atomic.AddUint64(&r.Stats.RxErrors, 1)
		return
	}
	p, isNew := sess.getOrCreatePath(pathID, src)
	if !isNew {
		cur := p.RemoteAddr()
		if cur == nil || !udpAddrEqual(cur, src) {
			p.SetRemoteAddr(src)
		}
	}
	p.RecordRecv() // FEC packets consume PSN on send too; keep loss accounting consistent

	if sess.fecRecv == nil {
		return // FEC disabled locally; nothing to do with an unsolicited parity packet
	}
	fh, fhLen, err := proto.UnmarshalFECHeader(payload)
	if err != nil {
		return
	}
	shard := payload[fhLen:]
	parityIndex := int(inner.GenIndex) - int(fh.N)
	recovered := sess.fecRecv.HandleFEC(inner.GenerationID, int(fh.N), int(fh.M), int(fh.W), parityIndex, shard)
	for _, plain := range recovered {
		h, rpayload, ok := unmarshalRecovered(plain)
		if !ok {
			continue
		}
		atomic.AddUint64(&sess.Stats.RxPackets, 1)
		atomic.AddUint64(&sess.Stats.RxBytes, uint64(len(rpayload)))
		atomic.AddUint64(&r.Stats.RxPackets, 1)
		atomic.AddUint64(&r.Stats.RxBytes, uint64(len(rpayload)))
		sess.reorderBuf.Push(reorder.Packet{GSN: h.GSN, Payload: rpayload, Push: proto.HasFlag(h.Flags, proto.FlagPUSH)})
	}
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
