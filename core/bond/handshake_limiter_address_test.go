package bond

import (
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHandshakeLimiterCanonicalizesEquivalentIPRepresentations(t *testing.T) {
	now := time.Unix(100, 0)

	t.Run("ipv4 mapped ipv6 cannot bypass per-source bucket", func(t *testing.T) {
		l := newHandshakeLimiterWithGlobal(1, 1, 8, 100, 100)
		ipv4 := net.ParseIP("192.0.2.44")
		mapped := net.ParseIP("::ffff:192.0.2.44")
		if ipv4 == nil || mapped == nil {
			t.Fatal("failed to parse test addresses")
		}
		if !l.allow(ipv4, now) {
			t.Fatal("first IPv4 request rejected")
		}
		if l.allow(mapped, now) {
			t.Fatal("IPv4-mapped IPv6 representation bypassed per-source rate limit")
		}
	})

	t.Run("equivalent ipv6 spellings share one bucket", func(t *testing.T) {
		l := newHandshakeLimiterWithGlobal(1, 1, 8, 100, 100)
		compressed := net.ParseIP("2001:db8::1")
		expanded := net.ParseIP("2001:0db8:0000:0000:0000:0000:0000:0001")
		if compressed == nil || expanded == nil {
			t.Fatal("failed to parse test addresses")
		}
		if !l.allow(compressed, now) {
			t.Fatal("first IPv6 request rejected")
		}
		if l.allow(expanded, now) {
			t.Fatal("alternate IPv6 spelling bypassed per-source rate limit")
		}
	})
}

func TestHandshakeLimiterConcurrentSourceChurnHonorsGlobalBurst(t *testing.T) {
	const (
		workers     = 128
		globalBurst = 32
	)
	l := newHandshakeLimiterWithGlobal(1000, 1000, 16, 1, globalBurst)
	now := time.Unix(100, 0)

	var allowed atomic.Int64
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		i := i
		go func() {
			defer wg.Done()
			<-start
			ip := net.ParseIP(fmt.Sprintf("2001:db8::%x", i+1))
			if ip == nil {
				t.Errorf("failed to parse worker IP %d", i)
				return
			}
			if l.allow(ip, now) {
				allowed.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := allowed.Load(); got != globalBurst {
		t.Fatalf("concurrent source churn allowed %d handshakes, want exactly global burst %d", got, globalBurst)
	}
	if got := len(l.entries); got > l.maxSources {
		t.Fatalf("source table grew to %d entries, maxSources=%d", got, l.maxSources)
	}
}
