package bond

import (
	"math"
	"net"
	"testing"
	"time"
)

func TestHandshakeLimiterRejectsNonFiniteConfiguration(t *testing.T) {
	cases := []struct {
		name        string
		rate        float64
		burst       float64
		globalRate  float64
		globalBurst float64
	}{
		{name: "nan per-source rate", rate: math.NaN(), burst: 1, globalRate: 1, globalBurst: 1},
		{name: "positive-inf per-source burst", rate: 1, burst: math.Inf(1), globalRate: 1, globalBurst: 1},
		{name: "negative-inf global rate", rate: 1, burst: 1, globalRate: math.Inf(-1), globalBurst: 1},
		{name: "nan global burst", rate: 1, burst: 1, globalRate: 1, globalBurst: math.NaN()},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := newHandshakeLimiterWithGlobal(tc.rate, tc.burst, 8, tc.globalRate, tc.globalBurst)
			if !finitePositive(l.rate) || !finitePositive(l.burst) || !finitePositive(l.globalRate) || !finitePositive(l.globalBurst) {
				t.Fatalf("limiter retained non-finite config: rate=%v burst=%v globalRate=%v globalBurst=%v", l.rate, l.burst, l.globalRate, l.globalBurst)
			}
		})
	}
}

func TestHandshakeLimiterNonFiniteBurstCannotBecomeUnlimited(t *testing.T) {
	l := newHandshakeLimiterWithGlobal(1, math.NaN(), 8, 100, 100)
	ip := net.ParseIP("203.0.113.90")
	now := time.Unix(100, 0)

	for i := 0; i < int(defaultHandshakeBurst); i++ {
		if !l.allow(ip, now) {
			t.Fatalf("fallback burst request %d rejected", i)
		}
	}
	if l.allow(ip, now) {
		t.Fatal("non-finite burst configuration created an unlimited per-source budget")
	}
}

func TestHandshakeLimiterNonFiniteGlobalBurstCannotBecomeUnlimited(t *testing.T) {
	l := newHandshakeLimiterWithGlobal(100, 100, 32, 100, math.Inf(1))
	now := time.Unix(100, 0)

	for i := 0; i < int(defaultHandshakeGlobalBurst); i++ {
		ip := net.IPv4(198, 51, byte(i/254), byte(i%254+1))
		if !l.allow(ip, now) {
			t.Fatalf("fallback global burst request %d rejected", i)
		}
	}
	if l.allow(net.ParseIP("203.0.113.250"), now) {
		t.Fatal("non-finite global burst configuration created an unlimited aggregate budget")
	}
}
