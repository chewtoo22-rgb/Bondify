package bond

import (
	"sort"
	"sync/atomic"
	"time"

	"github.com/chewtoo22-rgb/bondify/core/budget"
)

// PathDiag is one path's live telemetry, shaped for direct JSON serialization -- this is
// the same data the CLI's statsLoop prints, plus the counters PROTOCOL.md §6's `stats`
// control message documents as the diagnostics UI's contract.
type PathDiag struct {
	ID         uint8   `json:"id"`
	Role       string  `json:"role"`
	State      string  `json:"state"`
	RTTMinMS   float64 `json:"rtt_min_ms"`
	LossPct    float64 `json:"loss_pct"`
	InFlight   int64   `json:"in_flight_bytes"`
	CWND       int64   `json:"cwnd_bytes"`
	TxPackets  uint64  `json:"tx_packets"`
	RxPackets  uint64  `json:"rx_packets"`
	TxBytes    uint64  `json:"tx_bytes"`
	RxBytes    uint64  `json:"rx_bytes"`
	LastActive float64 `json:"last_active_sec"`
}

func pathDiagOf(p *Path) PathDiag {
	stats := snapshotStats(&p.Stats)
	return PathDiag{
		ID:         p.ID(),
		Role:       p.Role().String(),
		State:      p.State().String(),
		RTTMinMS:   float64(p.RTTMin()) / float64(time.Millisecond),
		LossPct:    p.Loss() * 100,
		InFlight:   p.InFlight(),
		CWND:       p.CWND(),
		TxPackets:  stats.TxPackets,
		RxPackets:  stats.RxPackets,
		TxBytes:    stats.TxBytes,
		RxBytes:    stats.RxBytes,
		LastActive: p.LastActivity().Seconds(),
	}
}

func snapshotStats(stats *Stats) Stats {
	return Stats{
		TxPackets: atomic.LoadUint64(&stats.TxPackets),
		RxPackets: atomic.LoadUint64(&stats.RxPackets),
		TxBytes:   atomic.LoadUint64(&stats.TxBytes),
		RxBytes:   atomic.LoadUint64(&stats.RxBytes),
		RxErrors:  atomic.LoadUint64(&stats.RxErrors),
		TxAcks:    atomic.LoadUint64(&stats.TxAcks),
		RxAcks:    atomic.LoadUint64(&stats.RxAcks),
		TxRetries: atomic.LoadUint64(&stats.TxRetries),
	}
}

// AggregateDiag is the bonded-tunnel-wide view: the sum across every path plus the
// reordering buffer's current state, which belongs to the session as a whole rather than
// any one path.
type AggregateDiag struct {
	TxPackets             uint64  `json:"tx_packets"`
	RxPackets             uint64  `json:"rx_packets"`
	TxBytes               uint64  `json:"tx_bytes"`
	RxBytes               uint64  `json:"rx_bytes"`
	RxErrors              uint64  `json:"rx_errors"`
	TxAcks                uint64  `json:"tx_acks"`
	RxAcks                uint64  `json:"rx_acks"`
	TxRetries             uint64  `json:"tx_retries"`
	ReorderOccupancyBytes int     `json:"reorder_occupancy_bytes"`
	ReorderMaxBytes       int     `json:"reorder_max_bytes"`
	ReorderForcedReleases uint64  `json:"reorder_forced_releases"`
	UptimeSec             float64 `json:"uptime_sec"`
	// BulkPacing is absent when traffic classification is disabled. When present it makes
	// configured rate/headroom pacing and its bounded-queue overload visible.
	BulkPacing *budget.PacerSnapshot `json:"bulk_pacing,omitempty"`
}

// SessionDiag is one bonded session's full diagnostics snapshot: scheduler in use, every
// path, and the aggregate. Both ClientTunnel (always exactly one session) and Relay
// (arbitrarily many, one per connected client) report this shape so a single dashboard can
// render either.
type SessionDiag struct {
	SessionIndex string        `json:"session_index"`
	TunnelIP     string        `json:"tunnel_ip"`
	Scheduler    string        `json:"scheduler"`
	Paths        []PathDiag    `json:"paths"`
	Aggregate    AggregateDiag `json:"aggregate"`
}

func sortPathDiagnostics(paths []PathDiag) {
	sort.Slice(paths, func(i, j int) bool {
		return paths[i].ID < paths[j].ID
	})
}

func sortSessionDiagnostics(sessions []SessionDiag) {
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].SessionIndex < sessions[j].SessionIndex
	})
}

// Diagnostics returns the client tunnel's current state as a JSON-ready snapshot -- the
// payload core/diag.Server serves on the client's localhost diagnostics endpoint.
func (t *ClientTunnel) Diagnostics() SessionDiag {
	paths := t.Paths()
	pd := make([]PathDiag, len(paths))
	for i, p := range paths {
		pd[i] = pathDiagOf(p)
	}
	sortPathDiagnostics(pd)
	var bulkPacing *budget.PacerSnapshot
	if p := t.bulkPacer.Load(); p != nil {
		snapshot := p.Snapshot()
		bulkPacing = &snapshot
	}
	stats := snapshotStats(&t.Stats)
	return SessionDiag{
		SessionIndex: sessionIndexHex(t.sessionIndex),
		TunnelIP:     t.tunnelIP,
		Scheduler:    t.sched.Name(),
		Paths:        pd,
		Aggregate: AggregateDiag{
			TxPackets:             stats.TxPackets,
			RxPackets:             stats.RxPackets,
			TxBytes:               stats.TxBytes,
			RxBytes:               stats.RxBytes,
			RxErrors:              stats.RxErrors,
			TxAcks:                stats.TxAcks,
			RxAcks:                stats.RxAcks,
			TxRetries:             stats.TxRetries,
			ReorderOccupancyBytes: t.reorderBuf.Occupancy(),
			ReorderMaxBytes:       t.reorderBuf.MaxBytes(),
			ReorderForcedReleases: t.reorderBuf.ForcedReleases(),
			UptimeSec:             time.Since(t.startedAt).Seconds(),
			BulkPacing:            bulkPacing,
		},
	}
}

// RelayDiag is the relay-wide diagnostics snapshot: one SessionDiag per connected client,
// since a relay serves many bonded tunnels at once.
type RelayDiag struct {
	Sessions []SessionDiag `json:"sessions"`
}

// Diagnostics returns every active session's current state. Locking mirrors the rest of
// Relay's session-table access (r.mu for the index, each session's own pathsMu for its
// path map).
func (r *Relay) Diagnostics() RelayDiag {
	r.mu.RLock()
	sessions := make([]*relaySession, 0, len(r.byIndex))
	for _, s := range r.byIndex {
		sessions = append(sessions, s)
	}
	r.mu.RUnlock()

	out := make([]SessionDiag, len(sessions))
	for i, s := range sessions {
		var bulkPacing *budget.PacerSnapshot
		if s.bulkPacer != nil {
			snapshot := s.bulkPacer.Snapshot()
			bulkPacing = &snapshot
		}
		stats := snapshotStats(&s.Stats)
		s.pathsMu.RLock()
		pd := make([]PathDiag, 0, len(s.paths))
		for _, p := range s.paths {
			pd = append(pd, pathDiagOf(p))
		}
		s.pathsMu.RUnlock()
		sortPathDiagnostics(pd)
		out[i] = SessionDiag{
			SessionIndex: sessionIndexHex(s.sessionIndex),
			TunnelIP:     s.tunnelIP.String(),
			Scheduler:    s.sched.Name(),
			Paths:        pd,
			Aggregate: AggregateDiag{
				TxPackets:             stats.TxPackets,
				RxPackets:             stats.RxPackets,
				TxBytes:               stats.TxBytes,
				RxBytes:               stats.RxBytes,
				RxErrors:              stats.RxErrors,
				TxAcks:                stats.TxAcks,
				RxAcks:                stats.RxAcks,
				TxRetries:             stats.TxRetries,
				ReorderOccupancyBytes: s.reorderBuf.Occupancy(),
				ReorderMaxBytes:       s.reorderBuf.MaxBytes(),
				ReorderForcedReleases: s.reorderBuf.ForcedReleases(),
				UptimeSec:             time.Since(s.startedAt).Seconds(),
				BulkPacing:            bulkPacing,
			},
		}
	}
	sortSessionDiagnostics(out)
	return RelayDiag{Sessions: out}
}

func sessionIndexHex(idx uint32) string {
	const hexdigits = "0123456789abcdef"
	b := make([]byte, 8)
	for i := 7; i >= 0; i-- {
		b[i] = hexdigits[idx&0xf]
		idx >>= 4
	}
	return string(b)
}
