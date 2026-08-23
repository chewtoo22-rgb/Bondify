package crypto

import (
	"errors"
	"sync/atomic"
	"testing"
)

func TestReplayWindowConcurrentSameCounterHasOneWinner(t *testing.T) {
	w := newReplayWindow(64)
	entered := make(chan struct{})
	release := make(chan struct{})
	results := make(chan error, 2)
	var authenticated atomic.Int32

	go func() {
		results <- w.authenticateAndMark(7, func() error {
			authenticated.Add(1)
			close(entered)
			<-release
			return nil
		})
	}()

	// Force the first candidate to remain inside authentication while the second
	// attempts admission for the exact same nonce. The second must wait for the
	// transaction and then observe the nonce as a replay; it must never authenticate.
	<-entered
	go func() {
		results <- w.authenticateAndMark(7, func() error {
			authenticated.Add(1)
			return nil
		})
	}()
	close(release)

	var successes, replays int
	for i := 0; i < 2; i++ {
		switch err := <-results; {
		case err == nil:
			successes++
		case errors.Is(err, ErrReplay):
			replays++
		default:
			t.Fatalf("unexpected admission result: %v", err)
		}
	}
	if successes != 1 || replays != 1 {
		t.Fatalf("same counter results: successes=%d replays=%d, want 1/1", successes, replays)
	}
	if got := authenticated.Load(); got != 1 {
		t.Fatalf("authentication callback ran %d times, want exactly once", got)
	}
}

func TestReplayWindowAuthFailureDoesNotBurnCounter(t *testing.T) {
	w := newReplayWindow(64)
	wantErr := errors.New("authentication failed")
	if err := w.authenticateAndMark(11, func() error { return wantErr }); !errors.Is(err, wantErr) {
		t.Fatalf("failed authentication returned %v, want %v", err, wantErr)
	}
	if err := w.authenticateAndMark(11, func() error { return nil }); err != nil {
		t.Fatalf("counter was burned by failed authentication: %v", err)
	}
	if err := w.authenticateAndMark(11, func() error { return nil }); !errors.Is(err, ErrReplay) {
		t.Fatalf("authenticated duplicate returned %v, want ErrReplay", err)
	}
}
