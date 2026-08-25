package bond

import (
	"net"
	"sync"
	"testing"

	"github.com/chewtoo22-rgb/bondify/core/sched"
)

func newPathTableTestSession() *relaySession {
	rs := &relaySession{paths: make(map[uint8]*Path)}
	rs.schedPathView.Store([]sched.Path(nil))
	return rs
}

func TestRelayPathTableConcurrentSameIDPublishesOnce(t *testing.T) {
	rs := newPathTableTestSession()
	addr := &net.UDPAddr{IP: net.IPv4(198, 51, 100, 10), Port: 40000}

	const workers = 128
	paths := make(chan *Path, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			p, _ := rs.getOrCreatePath(7, addr)
			paths <- p
		}()
	}
	wg.Wait()
	close(paths)

	if got := len(rs.paths); got != 1 {
		t.Fatalf("path table size = %d, want 1", got)
	}
	if got := len(rs.schedPaths()); got != 1 {
		t.Fatalf("scheduler path view size = %d, want 1", got)
	}

	var first *Path
	for p := range paths {
		if first == nil {
			first = p
			continue
		}
		if p != first {
			t.Fatal("concurrent creation published more than one Path for the same path ID")
		}
	}
}

func TestRelayPathTableBoundedByPathIDNamespace(t *testing.T) {
	rs := newPathTableTestSession()
	addr := &net.UDPAddr{IP: net.IPv4(203, 0, 113, 20), Port: 41000}

	// Exercise every representable on-wire path ID concurrently. The uint8 path-ID
	// namespace is an intentional hard resource bound: authenticated churn must not
	// manufacture more live path entries or scheduler slots than the protocol can name.
	var wg sync.WaitGroup
	wg.Add(256)
	for i := 0; i < 256; i++ {
		id := uint8(i)
		go func() {
			defer wg.Done()
			rs.getOrCreatePath(id, addr)
		}()
	}
	wg.Wait()

	if got := len(rs.paths); got != 256 {
		t.Fatalf("path table size = %d, want 256", got)
	}
	view := rs.schedPaths()
	if got := len(view); got != 256 {
		t.Fatalf("scheduler path view size = %d, want 256", got)
	}
	seen := make(map[uint8]struct{}, 256)
	for _, p := range view {
		seen[p.ID()] = struct{}{}
	}
	if got := len(seen); got != 256 {
		t.Fatalf("unique scheduler path IDs = %d, want 256", got)
	}

	// Hammer repeated IDs after saturation. This models authenticated path churn and
	// proves duplicates cannot grow either the table or immutable scheduler view.
	for i := 0; i < 4096; i++ {
		rs.getOrCreatePath(uint8(i), addr)
	}
	if got := len(rs.paths); got != 256 {
		t.Fatalf("path table grew after duplicate churn: %d", got)
	}
	if got := len(rs.schedPaths()); got != 256 {
		t.Fatalf("scheduler view grew after duplicate churn: %d", got)
	}
}
