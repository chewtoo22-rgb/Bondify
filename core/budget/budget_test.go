package budget

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type manualClock struct {
	mu         sync.Mutex
	now        time.Time
	timers     []*manualTimer
	timerAdded chan struct{}
}

type manualTimer struct {
	mu      sync.Mutex
	due     time.Time
	ch      chan time.Time
	stopped bool
	fired   bool
}

func newManualClock() *manualClock {
	return &manualClock{now: time.Unix(1, 0), timerAdded: make(chan struct{}, 16)}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) NewTimer(d time.Duration) timer {
	c.mu.Lock()
	t := &manualTimer{due: c.now.Add(d), ch: make(chan time.Time, 1)}
	c.timers = append(c.timers, t)
	c.mu.Unlock()
	c.timerAdded <- struct{}{}
	return t
}

func (c *manualClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now
	timers := append([]*manualTimer(nil), c.timers...)
	c.mu.Unlock()
	for _, t := range timers {
		t.mu.Lock()
		if !t.stopped && !t.fired && !now.Before(t.due) {
			t.fired = true
			t.ch <- now
		}
		t.mu.Unlock()
	}
}

func (t *manualTimer) Chan() <-chan time.Time { return t.ch }

func (t *manualTimer) Stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped || t.fired {
		return false
	}
	t.stopped = true
	return true
}

func TestLimiterUnlimitedReturnsImmediately(t *testing.T) {
	l, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Wait(context.Background(), 1<<20); err != nil {
		t.Fatalf("unlimited Wait: %v", err)
	}
	if got := l.Snapshot().BytesPerSecond; got != 0 {
		t.Fatalf("rate = %d, want unlimited (0)", got)
	}
}

func TestLimiterPacesWithDeterministicClock(t *testing.T) {
	clock := newManualClock()
	l, err := newWithClock(Config{BytesPerSecond: 1000}, clock)
	if err != nil {
		t.Fatal(err)
	}
	burst := int(l.Snapshot().BurstBytes)
	if err := l.Wait(context.Background(), burst); err != nil {
		t.Fatalf("consume initial burst: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- l.Wait(context.Background(), 100) }()
	select {
	case <-clock.timerAdded:
	case <-time.After(time.Second):
		t.Fatal("limiter never registered its pacing timer")
	}

	clock.Advance(99 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("Wait returned 1ms early: %v", err)
	default:
	}
	clock.Advance(time.Millisecond)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Wait after refill: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Wait did not return after enough deterministic refill time")
	}
}

func TestLimiterWaitCancellation(t *testing.T) {
	clock := newManualClock()
	l, err := newWithClock(Config{BytesPerSecond: 1}, clock)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Wait(context.Background(), int(l.Snapshot().BurstBytes)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Wait(ctx, 1) }()
	select {
	case <-clock.timerAdded:
	case <-time.After(time.Second):
		t.Fatal("limiter never began waiting")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Wait error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not interrupt Wait")
	}
}

func TestConfigRejectsNegativeRate(t *testing.T) {
	if _, err := New(Config{BytesPerSecond: -1}); err == nil {
		t.Fatal("negative rate was accepted")
	}
}

func TestFromBitsPerSecondRoundsPositiveValuesUp(t *testing.T) {
	for _, tc := range []struct {
		bits, want int64
	}{{0, 0}, {1, 1}, {8, 1}, {9, 2}, {8_000_000, 1_000_000}} {
		got, err := FromBitsPerSecond(tc.bits)
		if err != nil {
			t.Fatalf("FromBitsPerSecond(%d): %v", tc.bits, err)
		}
		if got.BytesPerSecond != tc.want {
			t.Fatalf("FromBitsPerSecond(%d) = %d B/s, want %d", tc.bits, got.BytesPerSecond, tc.want)
		}
	}
	if _, err := FromBitsPerSecond(-1); err == nil {
		t.Fatal("negative bit rate was accepted")
	}
}
