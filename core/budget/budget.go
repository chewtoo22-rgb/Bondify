// Package budget provides cancellable byte-rate pacing for Bondify traffic classes.
//
// A zero-value Config is unlimited. A configured limiter uses a token bucket with a
// bounded burst and waits for capacity instead of turning rate limiting into packet loss.
// Queueing policy lives in Pacer; keeping the two concerns separate makes the limiter
// deterministic to test and useful outside the TUN packet pump.
package budget

import (
	"context"
	"fmt"
	"sync"
	"time"
)

const (
	// minBurstBytes is large enough for the biggest IPv4 packet a TUN device can hand us.
	// The normal Bondify MTU is much smaller, but making this invariant hold here prevents
	// a low configured rate from creating a request that can never fit in the bucket.
	minBurstBytes int64 = 1 << 16
	// burstWindow permits a short burst without allowing a full second of unpaced traffic.
	burstWindow = 100 * time.Millisecond
)

// Config is a byte-rate ceiling. BytesPerSecond == 0 means unlimited.
type Config struct {
	BytesPerSecond int64
}

// FromBitsPerSecond converts the conventional command-line/network unit to Config's
// byte-accounting unit, rounding up so a small positive ceiling never becomes unlimited.
func FromBitsPerSecond(bitsPerSecond int64) (Config, error) {
	if bitsPerSecond < 0 {
		return Config{}, fmt.Errorf("budget: bits per second must be >= 0")
	}
	bytesPerSecond := bitsPerSecond / 8
	if bitsPerSecond%8 != 0 {
		bytesPerSecond++
	}
	return Config{BytesPerSecond: bytesPerSecond}, nil
}

// Validate rejects ambiguous negative rates. Zero deliberately means unlimited.
func (c Config) Validate() error {
	if c.BytesPerSecond < 0 {
		return fmt.Errorf("budget: bytes per second must be >= 0")
	}
	return nil
}

// Snapshot is a point-in-time limiter view for diagnostics.
type Snapshot struct {
	BytesPerSecond int64 `json:"bytes_per_second"`
	BurstBytes     int64 `json:"burst_bytes"`
	AvailableBytes int64 `json:"available_bytes"`
}

type timer interface {
	Chan() <-chan time.Time
	Stop() bool
}

type clock interface {
	Now() time.Time
	NewTimer(time.Duration) timer
}

type realClock struct{}

func (realClock) Now() time.Time                 { return time.Now() }
func (realClock) NewTimer(d time.Duration) timer { return realTimer{time.NewTimer(d)} }

type realTimer struct{ *time.Timer }

func (t realTimer) Chan() <-chan time.Time { return t.C }

// Limiter is a concurrent-safe token bucket. Wait is cancellation-aware and never drops.
type Limiter struct {
	mu     sync.Mutex
	clock  clock
	rate   int64
	burst  float64
	tokens float64
	last   time.Time
}

// New constructs a limiter. The returned limiter is unlimited when cfg is zero.
func New(cfg Config) (*Limiter, error) {
	return newWithClock(cfg, realClock{})
}

func newWithClock(cfg Config, c clock) (*Limiter, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if c == nil {
		return nil, fmt.Errorf("budget: clock is nil")
	}
	now := c.Now()
	if cfg.BytesPerSecond == 0 {
		return &Limiter{clock: c, last: now}, nil
	}
	burst := float64(minBurstBytes)
	if windowBurst := float64(cfg.BytesPerSecond) * burstWindow.Seconds(); windowBurst > burst {
		burst = windowBurst
	}
	return &Limiter{
		clock:  c,
		rate:   cfg.BytesPerSecond,
		burst:  burst,
		tokens: burst,
		last:   now,
	}, nil
}

// Wait blocks until n bytes fit in the bucket or ctx is cancelled. An unlimited limiter,
// a nil limiter, and non-positive sizes all return immediately.
func (l *Limiter) Wait(ctx context.Context, n int) error {
	if l == nil || n <= 0 || l.rate == 0 {
		return nil
	}
	if ctx == nil {
		return fmt.Errorf("budget: context is nil")
	}
	need := float64(n)
	if need > l.burst {
		return fmt.Errorf("budget: packet of %d bytes exceeds burst capacity %.0f", n, l.burst)
	}
	for {
		wait := l.takeOrDelay(need)
		if wait <= 0 {
			return nil
		}
		t := l.clock.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.Chan():
			t.Stop()
		}
	}
}

func (l *Limiter) takeOrDelay(need float64) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.refillLocked(l.clock.Now())
	if l.tokens >= need {
		l.tokens -= need
		return 0
	}
	seconds := (need - l.tokens) / float64(l.rate)
	wait := time.Duration(seconds * float64(time.Second))
	if wait < time.Nanosecond {
		wait = time.Nanosecond
	}
	return wait
}

func (l *Limiter) refillLocked(now time.Time) {
	if now.Before(l.last) {
		// A monotonic production clock never moves backward, but deterministic clocks and
		// wall-clock corrections can. Refusing to mint tokens is the conservative choice.
		return
	}
	elapsed := now.Sub(l.last).Seconds()
	l.tokens += elapsed * float64(l.rate)
	if l.tokens > l.burst {
		l.tokens = l.burst
	}
	l.last = now
}

// Snapshot reports the currently available tokens without consuming them.
func (l *Limiter) Snapshot() Snapshot {
	if l == nil {
		return Snapshot{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.refillLocked(l.clock.Now())
	return Snapshot{
		BytesPerSecond: l.rate,
		BurstBytes:     int64(l.burst),
		AvailableBytes: int64(l.tokens),
	}
}
