package bond

import (
	"container/list"
	"net"
	"sync"
	"time"
)

const (
	defaultHandshakeRate      = 5.0
	defaultHandshakeBurst     = 10.0
	defaultHandshakeMaxSources = 4096
)

type handshakeLimiter struct {
	mu         sync.Mutex
	rate       float64
	burst      float64
	maxSources int
	entries    map[string]*handshakeLimitEntry
	lru        *list.List
}

type handshakeLimitEntry struct {
	key    string
	tokens float64
	last   time.Time
	elem   *list.Element
}

func newHandshakeLimiter(rate, burst float64, maxSources int) *handshakeLimiter {
	if rate <= 0 {
		rate = defaultHandshakeRate
	}
	if burst <= 0 {
		burst = defaultHandshakeBurst
	}
	if maxSources <= 0 {
		maxSources = defaultHandshakeMaxSources
	}
	return &handshakeLimiter{
		rate: rate, burst: burst, maxSources: maxSources,
		entries: make(map[string]*handshakeLimitEntry, maxSources), lru: list.New(),
	}
}

func (l *handshakeLimiter) allow(ip net.IP, now time.Time) bool {
	if l == nil || ip == nil {
		return false
	}
	key := ip.String()
	l.mu.Lock()
	defer l.mu.Unlock()

	if e := l.entries[key]; e != nil {
		elapsed := now.Sub(e.last).Seconds()
		if elapsed > 0 {
			e.tokens += elapsed * l.rate
			if e.tokens > l.burst {
				e.tokens = l.burst
			}
			e.last = now
		}
		l.lru.MoveToBack(e.elem)
		if e.tokens < 1 {
			return false
		}
		e.tokens--
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
	return true
}
