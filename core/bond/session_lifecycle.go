package bond

import (
	"context"
	"log"
	"time"

	"github.com/chewtoo22-rgb/bondify/core/sched"
	"github.com/chewtoo22-rgb/bondify/core/tun"
)

const defaultSessionIdleTimeout = 2 * time.Minute

// DefaultSessionIdleTimeout is the relay's conservative inactivity window before a session
// becomes eligible for reclamation. The normal keepalive/probe cadence is far shorter than
// this; a session must also have no non-DEAD path before it can be reaped.
const DefaultSessionIdleTimeout = defaultSessionIdleTimeout

// RunSessionReaper reclaims sessions that have been fully dead and inactive for idleTimeout.
// A non-positive timeout disables reclamation. This loop is deliberately independent from
// path liveness so tests can exercise the policy deterministically and operators can choose a
// conservative lease lifetime without changing path failover behavior.
func (r *Relay) RunSessionReaper(ctx context.Context, idleTimeout time.Duration) {
	if r == nil || idleTimeout <= 0 {
		return
	}
	interval := idleTimeout / 4
	if interval < time.Second {
		interval = time.Second
	}
	if interval > 30*time.Second {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			r.reapIdleSessions(now, idleTimeout)
		}
	}
}

// reapIdleSessions performs one deterministic reclamation pass and returns the number of
// sessions retired. Tests inject now so no sleeps are required.
func (r *Relay) reapIdleSessions(now time.Time, idleTimeout time.Duration) int {
	if r == nil || idleTimeout <= 0 {
		return 0
	}
	r.mu.RLock()
	sessions := make([]*relaySession, 0, len(r.byIndex))
	for _, s := range r.byIndex {
		sessions = append(sessions, s)
	}
	r.mu.RUnlock()

	reaped := 0
	for _, s := range sessions {
		if !sessionReapEligible(s, now, idleTimeout) {
			continue
		}
		if r.removeSession(s) {
			reaped++
			log.Printf("bond: session %08x expired after %s idle; released tunnel ip %s", s.sessionIndex, idleTimeout, s.tunnelIP)
		}
	}
	return reaped
}

func sessionReapEligible(s *relaySession, now time.Time, idleTimeout time.Duration) bool {
	if s == nil || idleTimeout <= 0 {
		return false
	}
	latest := s.startedAt
	paths := s.pathSlice()
	for _, p := range paths {
		// Never reap an ACTIVE/JOINING/DEGRADED path even if its timestamp looks stale.
		// The ordinary liveness loop owns that transition to DEAD first.
		if p.State() != sched.StateDead {
			return false
		}
		if idle := p.LastActivityAt(now); idle > 0 {
			at := now.Add(-idle)
			if at.After(latest) {
				latest = at
			}
		}
	}
	idle := now.Sub(latest)
	return idle >= idleTimeout
}

// removeSession atomically detaches s from every relay lookup table and returns its lease
// exactly once. Pointer equality prevents an old/replaced session from deleting a newer
// reconnect that reused the same session index or tunnel IP.
func (r *Relay) removeSession(s *relaySession) bool {
	if r == nil || s == nil {
		return false
	}
	r.mu.Lock()
	if current := r.byIndex[s.sessionIndex]; current != s {
		r.mu.Unlock()
		return false
	}
	delete(r.byIndex, s.sessionIndex)
	if current := r.byTunnelIP[s.tunnelIP.String()]; current == s {
		delete(r.byTunnelIP, s.tunnelIP.String())
	}
	for key, current := range r.byClientKey {
		if current == s {
			delete(r.byClientKey, key)
		}
	}
	r.mu.Unlock()

	if s.bulkPacer != nil {
		s.bulkPacer.Close()
	}
	if s.reorderBuf != nil {
		s.reorderBuf.Stop()
	}
	r.pool.Release(s.tunnelIP)
	return true
}

// ServeManagedReorder is the lifecycle-aware relay reorder dispatcher. Unlike the legacy
// ServeReorder helper, each per-session drain goroutine periodically verifies that its exact
// session pointer is still current; retired/replaced sessions therefore stop owning a
// blocked goroutine instead of accumulating one forever under high client churn.
func (r *Relay) ServeManagedReorder(ctx context.Context) {
	seen := make(map[*relaySession]bool)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		r.mu.RLock()
		for _, s := range r.byIndex {
			if !seen[s] {
				seen[s] = true
				go r.drainManagedSessionReorder(ctx, s)
			}
		}
		r.mu.RUnlock()
	}
}

func (r *Relay) drainManagedSessionReorder(ctx context.Context, s *relaySession) {
	writeBuf := make([]byte, tun.IOOffset+65536)
	writeBufs := make([][]byte, 1)
	lifecycleTick := time.NewTicker(time.Second)
	defer lifecycleTick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-lifecycleTick.C:
			r.mu.RLock()
			current := r.byIndex[s.sessionIndex] == s
			r.mu.RUnlock()
			if !current {
				return
			}
		case pkt := <-s.reorderBuf.Out():
			n := copy(writeBuf[tun.IOOffset:], pkt.Payload)
			writeBufs[0] = writeBuf[:tun.IOOffset+n]
			if _, err := r.dev.Write(writeBufs, tun.IOOffset); err != nil {
				log.Printf("bond: relay tun write error: %v", err)
			}
		}
	}
}
