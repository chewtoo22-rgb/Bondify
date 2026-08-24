package bond

import (
	"net"
	"sync"
	"testing"
)

func TestIPPoolDuplicateReleaseCannotCreateDuplicateLease(t *testing.T) {
	pool, err := NewIPPool("10.77.0.0/29")
	if err != nil {
		t.Fatal(err)
	}

	first, err := pool.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := first.String(), "10.77.0.2"; got != want {
		t.Fatalf("first lease=%s, want %s", got, want)
	}

	pool.Release(first)
	pool.Release(first) // duplicate cleanup must be a no-op

	reused, err := pool.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	if !reused.Equal(first) {
		t.Fatalf("released lease=%s, want reuse of %s", reused, first)
	}

	next, err := pool.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	if next.Equal(reused) {
		t.Fatalf("duplicate release created a second live lease for %s", next)
	}
	if got, want := next.String(), "10.77.0.3"; got != want {
		t.Fatalf("next lease=%s, want %s", got, want)
	}
}

func TestIPPoolRejectsInvalidReleaseInputsWithoutPoisoningFreeList(t *testing.T) {
	pool, err := NewIPPool("10.77.0.0/29")
	if err != nil {
		t.Fatal(err)
	}

	for _, ip := range []net.IP{
		net.ParseIP("10.77.0.0"), // network
		net.ParseIP("10.77.0.1"), // relay gateway
		net.ParseIP("10.77.0.7"), // broadcast
		net.ParseIP("10.77.1.2"), // outside pool
		net.ParseIP("2001:db8::1"),
		nil,
	} {
		pool.Release(ip)
	}

	first, err := pool.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	second, err := pool.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := first.String(), "10.77.0.2"; got != want {
		t.Fatalf("first lease=%s after invalid releases, want %s", got, want)
	}
	if got, want := second.String(), "10.77.0.3"; got != want {
		t.Fatalf("second lease=%s after invalid releases, want %s", got, want)
	}
}

func TestIPPoolConcurrentAllocateReleaseNeverDuplicatesLiveLease(t *testing.T) {
	pool, err := NewIPPool("10.77.0.0/24")
	if err != nil {
		t.Fatal(err)
	}

	const workers = 96
	var wg sync.WaitGroup
	var mu sync.Mutex
	active := make(map[string]struct{})
	errs := make(chan string, workers)

	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			ip, err := pool.Allocate()
			if err != nil {
				errs <- err.Error()
				return
			}
			key := ip.String()

			mu.Lock()
			if _, exists := active[key]; exists {
				errs <- "duplicate live lease: " + key
			} else {
				active[key] = struct{}{}
			}
			mu.Unlock()

			mu.Lock()
			delete(active, key)
			mu.Unlock()
			pool.Release(ip)
		}()
	}
	wg.Wait()
	close(errs)

	for msg := range errs {
		t.Error(msg)
	}
}