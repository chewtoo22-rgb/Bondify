package bond

import (
	"container/list"
	"math"
	"net"
	"sync"
	"time"
)

const (
	defaultHandshakeRate        = 5.0
	defaultHandshakeBurst       = 10.0
	defaultHandshakeGlobalRate  = 100.0
	defaultHandshakeGlobalBurst = 200.0
	defaultHandshakeMaxSources  = 4096
)

type handshakeLimiter struct {
	mu sync.Mutex

	rate       float64
	burst      float64
	maxSources int
	entries    map[string]*handshakeLimitEntry
	lru        *list.List

	globalRate   float64
	globalBurst  float64
	globalTokens float64
	globalLast   time.Time
}

type handshakeLimitEntry struct {
	key    string
	tokens float64
	last   time.Time
	elem   *list.Element
}

func newHandshakeLimiter(rate, burst float64, maxSources int) *handshakeLimiter {
	return newHandshakeLimiterWithGlobal(
		rate,
		burst,
		maxSources,
		defaultHandshakeGlobalRate,
		defaultHandshakeGlobalBurst,
	)
}

func finitePositive(v float64) bool {
	return v > 0 && !math.IsNaN(v) && !math.IsInf(v, 0)
}

func newHandshakeLimiterWithGlobal(rate, burst float64, maxSources int, globalRate, globalBurst float64) *handshakeLimiter {
	if !finitePositive(rate) {
		rate = defaultHandshakeRate
	}
	if !finitePositive(burst) {
		burst = defaultHandshakeBurst
	}
	if maxSources <= 0 {
		maxSources = defaultHandshakeMaxSources
	}
	if !finitePositive(globalRate) {
		globalRate = defaultHandshakeGlobalRate
	}
	if !finitePositive(globalBurst) {
		globalBurst = defaultHandshakeGlobalBurst
	}
	return &handshakeLimiter{
		rate:         rate,
		burst:        burst,
		maxSources:   maxSources,
		entries:      make(map[string]*handshakeLimitEntry, maxSources),
		lru:          list.New(),
		globalRate:   globalRate,
		globalBurst:  globalBurst,
		globalTokens: globalBurst,
	}
}

func refillTokens(tokens, rate, burst float64, last, now time.Time) (float64, time.Time) {
	if last.IsZero() {
		return tokens, now
	}
	elapsed := now.Sub(last).Seconds()
	if elapsed <= 0 {
		return tokens, last
	}
	tokens += elapsed * rate
	if tokens > burst {
		tokens = burst
	}
	return tokens, now
}

func (l *handshakeLimiter) allow(ip net.IP, now time.Time) bool {
	if l == nil || ip == nil {
		return false
	}
	key := ip.String()
	l.mu.Lock()
	defer l.mu.Unlock()

	// A per-source limiter alone is not enough: an attacker that rotates or spoofs source
	// addresses can continually force LRU eviction and receive a fresh burst for every new
	// address. The global bucket caps aggregate expensive handshake work even under that
	// source-churn pattern.
	l.globalTokens, l.globalLast = refillTokens(
		l.globalTokens,
		l.globalRate,
		l.globalBurst,
		l.globalLast,
		now,
	)
	if l.globalTokens < 1 {
		return false
	}

	if e := l.entries[key]; e != nil {
		e.tokens, e.last = refillTokens(e.tokens, l.rate, l.burst, e.last, now)
		l.lru.MoveToBack(e.elem)
		if e.tokens < 1 {
			return false
		}
		e.tokens--
		l.globalTokens--
		return true
	}

	if len(l.entries) >= l.maxSources {
		front := l.lru.Front()
		if front != nil {
			old := front.Value.(*handshakeLimitEntry)
			delete(l.entries, old.key)
			l.lru.Remove(front)
		}
	}
	e := &handshakeLimitEntry{key: key, tokens: l.burst - 1, last: now}
	e.elem = l.lru.PushBack(e)
	l.entries[key] = e
	l.globalTokens--
	return true
}
