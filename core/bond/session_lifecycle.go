package bond

import (
	"context"
	"log"
	"net"
	"time"

	"github.com/chewtoo22-rgb/bondify/core/sched"
	"github.com/chewtoo22-rgb/bondify/core/tun"
)

const defaultSessionIdleTimeout = 2 * time.Minute

// DefaultSessionIdleTimeout is the relay's conservative inactivity window before a session
// becomes eligible for reclamation. The normal keepalive/probe cadence is far shorter than
// this; a session must also have no non-DEAD path before it can be reaped.
const DefaultSessionIdleTimeout = defaultSessionIdleTimeout

// ServeUDPManaged is ServeUDP plus deterministic idle-session reclamation. Reaping happens
// on this exact goroutine, between UDP packets, rather than in a background goroutine. That
// serialization is security-critical: handleHandshakeInit intentionally drops r.mu while it
// performs the Noise response and constructs the replacement session. A concurrent reaper
// could otherwise observe the old session, delete it, and release its tunnel IP after a
// same-key reconnect had already decided to reuse that lease. Keeping both operations on the
// UDP dispatcher makes the existing reconnect semantics and lease reclamation atomic at the
// protocol-operation level without widening r.mu across expensive crypto work.
//
// A non-positive timeout disables reclamation and falls back to the historical ServeUDP
// loop. With reclamation enabled, a read deadline wakes an otherwise-idle relay at most every
// maintenance interval so abandoned sessions are still collected even when no new packets
// arrive.
func (r *Relay) ServeUDPManaged(idleTimeout time.Duration) error {
	if r == nil {
		return nil
	}
	if idleTimeout <= 0 {
		return r.ServeUDP()
	}

	interval := sessionReapInterval(idleTimeout)
	zeroPathSince := make(map[*relaySession]time.Time)
	nextReap := time.Now().Add(interval)
	buf := make([]byte, 65536)

	for {
		if err := r.conn.SetReadDeadline(nextReap); err != nil {
			return err
		}
		n, src, err := r.conn.ReadFromUDP(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				now := time.Now()
				r.reapIdleSessionsTracked(now, idleTimeout, zeroPathSince)
				nextReap = now.Add(interval)
				continue
			}
			return err
		}

		r.handleUDP(buf[:n], src)
		now := time.Now()
		if !now.Before(nextReap) {
			r.reapIdleSessionsTracked(now, idleTimeout, zeroPathSince)
			nextReap = now.Add(interval)
		}
	}
}

func sessionReapInterval(idleTimeout time.Duration) time.Duration {
	interval := idleTimeout / 4
	if interval < time.Second {
		interval = time.Second
	}
	if interval > 30*time.Second {
		interval = 30 * time.Second
	}
	return interval
}

// reapIdleSessions performs one deterministic reclamation pass and returns the number of
// sessions retired. Tests inject now so no sleeps are required. Sessions that currently have
// zero paths are intentionally not retired by this stateless helper; ServeUDPManaged gives
// that state its own observed idle window via reapIdleSessionsTracked.
func (r *Relay) reapIdleSessions(now time.Time, idleTimeout time.Duration) int {
	return r.reapIdleSessionsTracked(now, idleTimeout, nil)
}

func (r *Relay) reapIdleSessionsTracked(now time.Time, idleTimeout time.Duration, zeroPathSince map[*relaySession]time.Time) int {
	if r == nil || idleTimeout <= 0 {
		return 0
	}
	r.mu.RLock()
	sessions := make([]*relaySession, 0, len(r.byIndex))
	current := make(map[*relaySession]struct{}, len(r.byIndex))
	for _, s := range r.byIndex {
		sessions = append(sessions, s)
		current[s] = struct{}{}
	}
	r.mu.RUnlock()

	for s := range zeroPathSince {
		if _, ok := current[s]; !ok {
			delete(zeroPathSince, s)
		}
	}

	reaped := 0
	for _, s := range sessions {
		paths := s.pathSlice()
		if len(paths) == 0 {
			if zeroPathSince == nil {
				continue
			}
			since, ok := zeroPathSince[s]
			if !ok {
				zeroPathSince[s] = now
				continue
			}
			if now.Sub(since) < idleTimeout {
				continue
			}
		} else {
			delete(zeroPathSince, s)
			if !sessionReapEligibleWithPaths(s, paths, now, idleTimeout) {
				continue
			}
		}
		if r.removeSession(s) {
			reaped++
			delete(zeroPathSince, s)
			log.Printf("bond: session %08x expired after %s idle; released tunnel ip %s", s.sessionIndex, idleTimeout, s.tunnelIP)
		}
	}
	return reaped
}

func sessionReapEligible(s *relaySession, now time.Time, idleTimeout time.Duration) bool {
	if s == nil {
		return false
	}
	paths := s.pathSlice()
	if len(paths) == 0 {
		return false
	}
	return sessionReapEligibleWithPaths(s, paths, now, idleTimeout)
}

func sessionReapEligibleWithPaths(s *relaySession, paths []*Path, now time.Time, idleTimeout time.Duration) bool {
	if s == nil || idleTimeout <= 0 {
		return false
	}
	latest := s.startedAt
	for _, p := range paths {
		// Never reap an ACTIVE/JOINING/DEGRADED path even if its timestamp looks stale.
		// The ordinary liveness loop owns that transition to DEAD first.
		if p.State() != sched.StateDead {
			return false
		}

		// lastProbeAckAt==0 means this path has never observed authenticated traffic. Do
		// not infer that state from LastActivityAt(now)==0: zero also means activity at
		// exactly now (and, after a wall-clock rollback, activity timestamped slightly in
		// the future). Treat any recorded future timestamp as activity at now so a clock
		// discontinuity can delay reclamation but can never make a live session look older.
		if lastNanos := p.lastProbeAckAt.Load(); lastNanos != 0 {
			at := time.Unix(0, lastNanos)
			if at.After(now) {
				at = now
			}
			if at.After(latest) {
				latest = at
			}
		}
	}
	return now.Sub(latest) >= idleTimeout
}

// removeSession atomically detaches s from every relay lookup table and returns its lease
// exactly once. Pointer equality prevents an old/replaced session from deleting a newer
// reconnect that reused the same session index or tunnel IP. Production reclamation calls
// this only from ServeUDPManaged, serial with handleHandshakeInit, so a reconnect cannot be
// midway through deciding to reuse s while its lease is returned to the pool.
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
		current := make(map[*relaySession]struct{})
		r.mu.RLock()
		for _, s := range r.byIndex {
			current[s] = struct{}{}
			if !seen[s] {
				seen[s] = true
				go r.drainManagedSessionReorder(ctx, s)
			}
		}
		r.mu.RUnlock()
		for s := range seen {
			if _, ok := current[s]; !ok {
				delete(seen, s)
			}
		}
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
