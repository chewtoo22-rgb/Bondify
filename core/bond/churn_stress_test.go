package bond

import (
	"sync"
	"testing"
	"time"

	"github.com/chewtoo22-rgb/bondify/core/crypto"
	"github.com/chewtoo22-rgb/bondify/core/reorder"
	"github.com/chewtoo22-rgb/bondify/core/sched"
)

// TestConcurrentPathChurnAndSessionReap exercises a bounded mix of path add/remove,
// reorder traffic, and idle-session reaping under the race detector. It is intentionally
// deterministic in structure (fixed iteration counts, no wall-clock flakiness beyond the
// existing reap eligibility rules) so CI can run it without giant sleeps.

func TestConcurrentPathChurnAndSessionReap(t *testing.T) {
	r := makeLifecycleTestRelay(t, "10.77.0.0/24")
	now := time.Now()

	const sessions = 8
	const pathsPer = 4
	const iterations = 40

	var mu sync.Mutex
	live := make([]*relaySession, 0, sessions)

	// Seed sessions with path 0 only.
	for i := 0; i < sessions; i++ {
		s := addLifecycleTestSession(t, r, uint32(100+i), byte(i+1), now, sched.StateActive, now)
		live = append(live, s)
	}
	seeded := append([]*relaySession(nil), live...)

	var wg sync.WaitGroup

	// Worker A: path add/remove churn on live sessions.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for n := 0; n < iterations; n++ {
			mu.Lock()
			if len(live) == 0 {
				mu.Unlock()
				continue
			}
			s := live[n%len(live)]
			mu.Unlock()

			id := uint8(1 + (n % (pathsPer - 1)))
			if n%2 == 0 {
				s.getOrCreatePath(id, nil)
			} else {
				s.removePath(id)
			}
			// Touch reorder buffer concurrently with path map mutations.
			if s.reorderBuf != nil {
				gsn := uint64(n)
				s.reorderBuf.Push(reorder.Packet{GSN: gsn, Payload: []byte{byte(n)}})
			}
		}
	}()

	// Worker B: concurrent diagnostics-style path snapshots + more reorder traffic.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for n := 0; n < iterations; n++ {
			mu.Lock()
			snapshot := append([]*relaySession(nil), live...)
			mu.Unlock()
			for _, s := range snapshot {
				_ = s.pathSlice()
				_ = s.schedPaths()
				if s.reorderBuf != nil {
					s.reorderBuf.Push(reorder.Packet{GSN: uint64(1000 + n), Payload: []byte{1}})
				}
			}
		}
	}()

	// Worker C: session reap while others still hold session pointers (must not double-free
	// pool leases or race on map deletion).
	wg.Add(1)
	go func() {
		defer wg.Done()
		for n := 0; n < iterations/2; n++ {
			// Mark some sessions idle enough to be reaped.
			mu.Lock()
			if len(live) > 2 {
				s := live[0]
				live = live[1:]
				mu.Unlock()
				for _, p := range s.pathSlice() {
					p.state.Store(int32(sched.StateDead))
					p.lastProbeAckAt.Store(now.Add(-time.Hour).UnixNano())
				}
				_ = r.reapIdleSessions(now, time.Minute)
			} else {
				mu.Unlock()
			}
		}
	}()

	wg.Wait()

	// Reaping races intentionally allow workers that already hold a session pointer to touch
	// it after Worker C removes it from the live slice. That can make a previously-idle session
	// active again before the final reap. Cleanup every seeded session explicitly so this test
	// measures lifecycle/race safety rather than scheduler ordering between the workers.
	_ = r.reapIdleSessions(now.Add(time.Hour), time.Minute)
	for _, s := range seeded {
		r.removeSession(s)
		if s.reorderBuf != nil {
			s.reorderBuf.Stop()
		}
	}

	if len(r.byIndex) != 0 || len(r.byTunnelIP) != 0 || len(r.byClientKey) != 0 {
		t.Fatalf("leaked session indexes after churn: byIndex=%d byTunnelIP=%d byClientKey=%d",
			len(r.byIndex), len(r.byTunnelIP), len(r.byClientKey))
	}
}

func TestReorderBufferConcurrentPushStop(t *testing.T) {
	// Bounded concurrent Push + Stop must not race or panic.
	b := reorder.New(20*time.Millisecond, 0)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for n := 0; n < 50; n++ {
				b.Push(reorder.Packet{GSN: uint64(id*50 + n), Payload: []byte{byte(n)}})
			}
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(2 * time.Millisecond)
		b.Stop()
	}()
	wg.Wait()
	b.Stop() // idempotent
}

func TestSessionRemoveIdempotentUnderChurn(t *testing.T) {
	r := makeLifecycleTestRelay(t, "10.77.0.0/28")
	now := time.Now()
	s := addLifecycleTestSession(t, r, 1, 9, now, sched.StateActive, now)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = r.removeSession(s)
		}()
	}
	wg.Wait()

	if len(r.byIndex) != 0 {
		t.Fatalf("session still indexed after concurrent remove: %d", len(r.byIndex))
	}
	// Pool must not have been double-released into an inconsistent free list.
	ip, err := r.pool.Allocate()
	if err != nil {
		t.Fatalf("pool allocate after concurrent remove: %v", err)
	}
	_ = ip
	var key [crypto.KeyLen]byte
	_ = key
}
