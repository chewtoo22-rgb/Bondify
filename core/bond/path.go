package bond

import (
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chewtoo22-rgb/bondify/core/cc"
	"github.com/chewtoo22-rgb/bondify/core/sched"
)

// Default constants from PROTOCOL.md's Appendix.
const (
	ProbeInterval            = 200 * time.Millisecond
	ProbeIdleAfter           = 30 * time.Second
	ProbeIdleInterval        = 1 * time.Second
	PathDeadTimeout          = 10 * time.Second
	DegradeLoss              = 0.15
	MissedProbesToDegrade    = 3
	HealthyProbesToUndegrade = 3
	rttWindow                = 10 * time.Second
	lossEWMAAlpha            = 0.2
	srttAlpha                = 0.125 // RFC 6298 convention
	rttVarBeta               = 0.25
)

// Path is one BOND/1 path. It implements sched.Path directly so schedulers consume it
// without an adapter. State transitions (JOINING/ACTIVE/DEGRADED/DEAD) follow PROTOCOL.md
// §5's diagram, driven by probe results.
//
// Scope note (phase 2): only the client side runs the full probe-driven state machine
// (loss/RTT thresholds, hysteresis). The relay tracks path liveness via a simpler
// last-activity timeout for its own return-traffic scheduling -- see relay.go. Symmetric,
// relay-initiated probing and the session-median-RTT-relative DEGRADE condition (as
// opposed to the absolute loss threshold implemented here) are deferred to phase 3, when
// the richer scheduler tiers actually need the extra signal; round-robin does not.
type Path struct {
	id   uint8
	role atomic.Int32 // sched.Role

	state          atomic.Int32 // sched.State
	lastProbeAckAt atomic.Int64 // UnixNano; 0 = never

	// conn is set only on the client side: each client path owns a dedicated UDP socket
	// bound to a specific physical uplink. The relay has no per-path socket -- it
	// demultiplexes every path of every session through one shared listener (relay.go)
	// and only needs to know where to send, which remoteAddr below provides.
	conn *net.UDPConn

	remoteAddr atomic.Pointer[net.UDPAddr]

	sendPSN atomic.Uint32
	recvPSN atomic.Uint32

	inflight atomic.Int64

	cc *cc.Controller

	mu                       sync.Mutex
	srtt                     time.Duration
	rttVar                   time.Duration
	rttSamples               []rttSample // pruned to rttWindow, for rtt_min
	lossEWMA                 float64
	consecutiveMissedProbes  int
	consecutiveHealthyProbes int
	lastProbeSentPSN         uint32
	lastProbeAckedPSN        uint32
	lastProbeAckedRecvPSN    uint32
	lastProbeAckedTxBytes    uint64
	lastProbeAckTime         time.Time

	Stats Stats
}

type rttSample struct {
	at  time.Time
	rtt time.Duration
}

// NewPath constructs a path in JOINING state. conn is nil on the relay side.
func NewPath(id uint8, conn *net.UDPConn) *Path {
	p := &Path{id: id, conn: conn, cc: cc.NewController()}
	p.role.Store(int32(sched.RoleBond))
	p.state.Store(int32(sched.StateJoining))
	return p
}

// sched.Path interface.
func (p *Path) ID() uint8          { return p.id }
func (p *Path) State() sched.State { return sched.State(p.state.Load()) }
func (p *Path) Role() sched.Role   { return sched.Role(p.role.Load()) }
func (p *Path) InFlight() int64    { return p.inflight.Load() }

// CWND is core/cc's live BBR-style congestion window for this path (ARCHITECTURE.md
// §2.4). Only the client side feeds it real samples today (see HandleProbeAck) -- the
// relay's paths never call Sample (symmetric relay-initiated probing is deferred, per this
// type's doc comment), so a relay-side path's CWND stays at cc's generous initial value
// rather than adapting. That's an acceptable asymmetry for now: it only under-constrains
// the relay's own return-traffic scheduling, never the client's.
func (p *Path) CWND() int64 { return p.cc.CWND() }

// Goodput is core/cc's windowed-max delivery-rate estimate in bytes/sec -- the same signal
// congestion control uses, reused by sched.WeightedGoodput (Tier 2) so both consumers agree
// on what a path is actually delivering.
func (p *Path) Goodput() float64 { return p.cc.DeliveryRate() }

func (p *Path) SetRole(r sched.Role) { p.role.Store(int32(r)) }

func (p *Path) SetActive() { p.state.Store(int32(sched.StateActive)) }

func (p *Path) RemoteAddr() *net.UDPAddr     { return p.remoteAddr.Load() }
func (p *Path) SetRemoteAddr(a *net.UDPAddr) { p.remoteAddr.Store(a) }

// NextSendPSN returns the next PSN to stamp on an outgoing DATA packet for this path.
func (p *Path) NextSendPSN() uint32 { return p.sendPSN.Add(1) - 1 }

// RecordRecv updates the path's received-packet counter and last-activity timestamp; used
// for both DATA and PROBE arrivals so relay-side liveness (last-activity based) and
// client-side loss computation (PSN-delta based) both have accurate input.
func (p *Path) RecordRecv() uint32 {
	p.lastProbeAckAt.Store(time.Now().UnixNano())
	return p.recvPSN.Add(1) - 1
}

// LastActivity reports how long it has been since any packet was recorded received on
// this path (DATA or PROBE) -- the relay's simplified liveness signal.
func (p *Path) LastActivity() time.Duration {
	last := p.lastProbeAckAt.Load()
	if last == 0 {
		return 0
	}
	return time.Since(time.Unix(0, last))
}

// BuildProbe returns the payload for the next outgoing PROBE on this path.
func (p *Path) BuildProbe() ProbePayload {
	psn := p.sendPSN.Load()
	p.mu.Lock()
	p.lastProbeSentPSN = psn
	p.mu.Unlock()
	return ProbePayload{SentAtUnixNano: time.Now().UnixNano(), SentPSN: psn}
}

// HandleProbeAck folds a PROBE_ACK into this path's RTT/loss estimates and health state.
// now is passed in (rather than read internally) to keep this deterministically testable.
func (p *Path) HandleProbeAck(ack ProbeAckPayload, now time.Time) {
	rtt := now.Sub(time.Unix(0, ack.SentAtUnixNano))
	if rtt < 0 {
		return // clock oddity; drop the sample rather than corrupt the estimate
	}

	p.mu.Lock()
	if p.srtt == 0 {
		p.srtt = rtt
		p.rttVar = rtt / 2
	} else {
		diff := rtt - p.srtt
		if diff < 0 {
			diff = -diff
		}
		p.rttVar += time.Duration(rttVarBeta * float64(diff-p.rttVar))
		p.srtt += time.Duration(srttAlpha * float64(rtt-p.srtt))
	}
	p.rttSamples = append(p.rttSamples, rttSample{at: now, rtt: rtt})
	p.rttSamples = pruneOld(p.rttSamples, now)

	// Loss: 1 - (delta received / delta sent) since the last probe, per PROTOCOL.md §6.
	//
	// deltaSent==0 (no DATA sent on this path since the last probe) must still feed an
	// instLoss=0 sample into the EWMA, not skip the update entirely. A path that
	// DEGRADEs from real loss gets excluded from the scheduler, which means it stops
	// carrying DATA, which means deltaSent stays 0 forever after -- if that also froze
	// lossEWMA at its last (high) value, DEGRADED->ACTIVE recovery could never trigger
	// (it requires loss <= DegradeLoss), permanently stranding the path and, if every
	// path degrades at once, silently halting the whole tunnel with no way back. A
	// probe round trip completing at all, even carrying no data, is itself evidence the
	// path currently works.
	deltaSent := ack.SentPSN - p.lastProbeAckedPSN
	deltaRecv := ack.RecvPSN - p.lastProbeAckedRecvPSN
	instLoss := 0.0
	if deltaSent > 0 {
		instLoss = 1 - float64(deltaRecv)/float64(deltaSent)
		if instLoss < 0 {
			instLoss = 0
		}
	}
	p.lossEWMA += lossEWMAAlpha * (instLoss - p.lossEWMA)
	p.lastProbeAckedPSN = ack.SentPSN
	p.lastProbeAckedRecvPSN = ack.RecvPSN

	// Feed core/cc a delivery-rate sample for this interval: bytes sent since the last
	// probe, scaled down by the same deltaRecv/deltaSent fraction just computed for loss,
	// approximates bytes actually delivered (we have no per-packet ACK yet -- PROBE_ACK's
	// PSN-delta reflection is the only delivery signal that exists today; see this
	// function's loss comment above for why deltaSent==0 already gets handled). Elapsed
	// time is wall-clock since the last PROBE_ACK, tracked separately from
	// lastProbeAckAt (below), which RecordRecv also touches on every DATA/PROBE arrival
	// and so can't double as "time of the last probe specifically."
	txBytesNow := atomic.LoadUint64(&p.Stats.TxBytes)
	if !p.lastProbeAckTime.IsZero() && deltaSent > 0 {
		deltaBytes := txBytesNow - p.lastProbeAckedTxBytes
		delivered := int64(float64(deltaBytes) * float64(deltaRecv) / float64(deltaSent))
		p.cc.Sample(delivered, now.Sub(p.lastProbeAckTime), minRTT(p.rttSamples))
	}
	p.lastProbeAckedTxBytes = txBytesNow
	p.lastProbeAckTime = now

	p.consecutiveMissedProbes = 0
	p.consecutiveHealthyProbes++
	loss := p.lossEWMA
	healthy := p.consecutiveHealthyProbes
	p.mu.Unlock()

	p.lastProbeAckAt.Store(now.UnixNano())

	switch p.State() {
	case sched.StateJoining:
		p.state.Store(int32(sched.StateActive))
	case sched.StateDegraded:
		if loss <= DegradeLoss && healthy >= HealthyProbesToUndegrade {
			p.state.Store(int32(sched.StateActive))
		}
	case sched.StateActive:
		if loss > DegradeLoss {
			p.degrade()
		}
	}
}

// HandleProbeMissed is called when an expected PROBE_ACK doesn't arrive within one probe
// interval's grace period. Repeated misses degrade the path; prolonged silence kills it.
func (p *Path) HandleProbeMissed() {
	p.mu.Lock()
	p.consecutiveMissedProbes++
	p.consecutiveHealthyProbes = 0
	missed := p.consecutiveMissedProbes
	p.mu.Unlock()

	if missed >= MissedProbesToDegrade && p.State() == sched.StateActive {
		p.degrade()
	}
	if p.lastProbeAckAt.Load() != 0 && time.Since(time.Unix(0, p.lastProbeAckAt.Load())) > PathDeadTimeout {
		p.state.Store(int32(sched.StateDead))
	}
}

func (p *Path) degrade() {
	p.state.Store(int32(sched.StateDegraded))
}

// RTTMin returns the windowed minimum RTT (over the last 10s), the metric PROTOCOL.md §6
// mandates for scheduling decisions -- smoothed RTT includes self-induced queueing delay
// and creates a load-unload oscillation feedback loop; minimum RTT is stable.
func (p *Path) RTTMin() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	samples := pruneOld(p.rttSamples, time.Now())
	p.rttSamples = samples
	return minRTT(samples)
}

func (p *Path) Loss() float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lossEWMA
}

// minRTT returns the smallest rtt among samples, or 0 if samples is empty. Shared by
// RTTMin (the public windowed-min-RTT accessor) and HandleProbeAck's cc.Controller.Sample
// call (which needs the same "rt_prop" value but is already holding p.mu, so it can't call
// RTTMin -- that would re-lock).
func minRTT(samples []rttSample) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	min := samples[0].rtt
	for _, s := range samples[1:] {
		if s.rtt < min {
			min = s.rtt
		}
	}
	return min
}

func pruneOld(samples []rttSample, now time.Time) []rttSample {
	cut := 0
	for cut < len(samples) && now.Sub(samples[cut].at) > rttWindow {
		cut++
	}
	if cut == 0 {
		return samples
	}
	return append([]rttSample(nil), samples[cut:]...)
}
