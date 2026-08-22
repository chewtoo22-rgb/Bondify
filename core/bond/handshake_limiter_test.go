package bond

import (
	"net"
	"testing"
	"time"
)

func TestHandshakeLimiterBurstAndRefill(t *testing.T) {
	l := newHandshakeLimiter(2, 3, 8)
	ip := net.ParseIP("203.0.113.10")
	now := time.Unix(100, 0)
	for i := 0; i < 3; i++ {
		if !l.allow(ip, now) {
			t.Fatalf("burst request %d rejected", i)
		}
	}
	if l.allow(ip, now) {
		t.Fatal("request beyond burst was allowed")
	}
	if !l.allow(ip, now.Add(500*time.Millisecond)) {
		t.Fatal("one token did not refill after 500ms at 2/s")
	}
	if l.allow(ip, now.Add(500*time.Millisecond)) {
		t.Fatal("refilled token was not consumed")
	}
}

func TestHandshakeLimiterIsPerSourceIP(t *testing.T) {
	l := newHandshakeLimiter(1, 1, 8)
	now := time.Unix(100, 0)
	a := net.ParseIP("198.51.100.1")
	b := net.ParseIP("198.51.100.2")
	if !l.allow(a, now) || !l.allow(b, now) {
		t.Fatal("independent sources should each receive their own burst")
	}
	if l.allow(a, now) || l.allow(b, now) {
		t.Fatal("per-source burst limits were not enforced")
	}
}

func TestHandshakeLimiterBoundsSourceTable(t *testing.T) {
	l := newHandshakeLimiter(1, 1, 2)
	now := time.Unix(100, 0)
	ips := []net.IP{
		net.ParseIP("192.0.2.1"),
		net.ParseIP("192.0.2.2"),
		net.ParseIP("192.0.2.3"),
	}
	for _, ip := range ips {
		if !l.allow(ip, now) {
			t.Fatalf("first request for %s rejected", ip)
		}
	}
	if len(l.entries) != 2 {
		t.Fatalf("source table size=%d, want 2", len(l.entries))
	}
	if _, ok := l.entries[ips[0].String()]; ok {
		t.Fatal("oldest source was not evicted when table reached its bound")
	}
}
