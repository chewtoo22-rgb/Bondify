package budget

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func unlimitedLimiter(t *testing.T) *Limiter {
	t.Helper()
	l, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestPacerQueueOverflowIsExplicit(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	p, err := NewPacer(context.Background(), unlimitedLimiter(t), 1, func([]byte) bool {
		select {
		case <-started:
		default:
			close(started)
		}
		<-release
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	if err := p.Enqueue([]byte("first")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker did not take first packet")
	}
	if err := p.Enqueue([]byte("second")); err != nil {
		t.Fatal(err)
	}
	if err := p.Enqueue([]byte("third")); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("third Enqueue error = %v, want ErrQueueFull", err)
	}
	s := p.Snapshot()
	if s.QueueDrops != 1 || s.QueueDropBytes != uint64(len("third")) {
		t.Fatalf("drop metrics = %+v, want one %d-byte drop", s, len("third"))
	}
	close(release)
}

func TestPacerOwnsPacketCopy(t *testing.T) {
	release := make(chan struct{})
	received := make(chan []byte, 1)
	p, err := NewPacer(context.Background(), unlimitedLimiter(t), 1, func(pkt []byte) bool {
		<-release
		received <- append([]byte(nil), pkt...)
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	pkt := []byte("original")
	if err := p.Enqueue(pkt); err != nil {
		t.Fatal(err)
	}
	copy(pkt, "mutated!")
	close(release)
	select {
	case got := <-received:
		if string(got) != "original" {
			t.Fatalf("paced packet = %q, want owned copy %q", got, "original")
		}
	case <-time.After(time.Second):
		t.Fatal("paced packet was not delivered")
	}
}

func TestPacerRetriesSchedulerWithoutDoubleSending(t *testing.T) {
	var attempts atomic.Uint64
	delivered := make(chan struct{})
	p, err := NewPacer(context.Background(), unlimitedLimiter(t), 1, func([]byte) bool {
		if attempts.Add(1) < 3 {
			return false
		}
		close(delivered)
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.Enqueue([]byte("packet")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("scheduler retry never delivered packet")
	}

	deadline := time.Now().Add(time.Second)
	for {
		s := p.Snapshot()
		if s.SchedulerWaits == 2 && s.SentPackets == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("snapshot = %+v, want two waits and one sent packet", s)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestPacerCloseInterruptsRateWait(t *testing.T) {
	l, err := New(Config{BytesPerSecond: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Wait(context.Background(), int(l.Snapshot().BurstBytes)); err != nil {
		t.Fatal(err)
	}
	p, err := NewPacer(context.Background(), l, 1, func([]byte) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Enqueue([]byte{1}); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		p.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Close did not interrupt a one-second limiter wait")
	}
}

func TestPacerEnqueueCanRaceClose(t *testing.T) {
	p, err := NewPacer(context.Background(), unlimitedLimiter(t), 64, func([]byte) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 100; j++ {
				_ = p.Enqueue([]byte{byte(j)})
			}
		}()
	}
	close(start)
	p.Close()
	wg.Wait()
	if err := p.Enqueue([]byte("late")); !errors.Is(err, ErrClosed) {
		t.Fatalf("Enqueue after Close = %v, want ErrClosed", err)
	}
}
