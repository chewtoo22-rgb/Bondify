package bond

import (
	"fmt"
	"sync"
	"testing"
)

// TestIPPoolConcurrentLeaseIntegrity locks down the allocator invariant relied on by relay
// handshake cleanup: under concurrent allocate/release churn, no live tunnel address may be
// handed to two clients at once. Every worker releases only its own live lease, then the
// entire usable pool must remain allocatable exactly once.
func TestIPPoolConcurrentLeaseIntegrity(t *testing.T) {
	p, err := NewIPPool("10.77.0.0/24")
	if err != nil {
		t.Fatal(err)
	}

	const workers = 32
	const rounds = 128
	live := make(map[string]struct{})
	var mu sync.Mutex
	var wg sync.WaitGroup
	errCh := make(chan error, workers)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				ip, err := p.Allocate()
				if err != nil {
					errCh <- err
					return
				}
				key := ip.String()
				mu.Lock()
				if _, exists := live[key]; exists {
					mu.Unlock()
					errCh <- fmt.Errorf("duplicate live lease %s", key)
					return
				}
				live[key] = struct{}{}
				mu.Unlock()

				mu.Lock()
				delete(live, key)
				mu.Unlock()
				p.Release(ip)
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}

	mu.Lock()
	if len(live) != 0 {
		t.Fatalf("live lease set not empty after churn: %d", len(live))
	}
	mu.Unlock()

	// /24 reserves network, gateway and broadcast, leaving 253 client leases.
	seen := make(map[string]struct{}, 253)
	for i := 0; i < 253; i++ {
		ip, err := p.Allocate()
		if err != nil {
			t.Fatalf("allocate %d after churn: %v", i, err)
		}
		key := ip.String()
		if _, exists := seen[key]; exists {
			t.Fatalf("duplicate lease after churn: %s", key)
		}
		seen[key] = struct{}{}
	}
	if _, err := p.Allocate(); err == nil {
		t.Fatal("expected pool exhaustion after allocating every usable address")
	}
}

func TestIPPoolConcurrentDuplicateReleaseDoesNotDuplicateLease(t *testing.T) {
	p, err := NewIPPool("10.77.0.0/29")
	if err != nil {
		t.Fatal(err)
	}
	ip, err := p.Allocate()
	if err != nil {
		t.Fatal(err)
	}

	const releasers = 64
	var wg sync.WaitGroup
	for i := 0; i < releasers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.Release(ip)
		}()
	}
	wg.Wait()

	// /29 has five client leases. A duplicate free-list entry would surface here as a
	// repeated live address before exhaustion.
	seen := make(map[string]struct{}, 5)
	for i := 0; i < 5; i++ {
		got, err := p.Allocate()
		if err != nil {
			t.Fatalf("allocate %d: %v", i, err)
		}
		key := got.String()
		if _, exists := seen[key]; exists {
			t.Fatalf("duplicate lease after concurrent duplicate release: %s", key)
		}
		seen[key] = struct{}{}
	}
	if _, err := p.Allocate(); err == nil {
		t.Fatal("expected /29 pool exhaustion")
	}
}
