package budget

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/chewtoo22-rgb/bondify/core/classify"
)

// Packet is one unit of paced traffic.
type Packet struct {
	Class   classify.Class
	Payload []byte
}

// SendFunc delivers a paced packet. Must not block indefinitely; the pacer holds no
// budget/scheduler locks while calling it.
type SendFunc func(Packet)

// Pacer applies Budget pacing to a class (typically Bulk) via a bounded queue.
// Latency-sensitive classes should not be sent through a Pacer; send them directly.
//
// When the queue is at capacity, Enqueue drops the packet, increments QueueDrops, and
// returns false. That is the only loss path and is explicit + observable.
type Pacer struct {
	budget   *Budget
	class    classify.Class
	send     SendFunc
	maxQueue int

	ch     chan Packet
	closed atomic.Bool
	wg     sync.WaitGroup

	QueueDrops atomic.Uint64
	Enqueued   atomic.Uint64
	Sent       atomic.Uint64
}

// NewPacer starts a background worker that drains the queue under budget.Acquire pacing.
// maxQueue must be >= 1. send must be non-nil.
func NewPacer(b *Budget, class classify.Class, maxQueue int, send SendFunc) *Pacer {
	if maxQueue < 1 {
		maxQueue = 1
	}
	if send == nil {
		send = func(Packet) {}
	}
	p := &Pacer{
		budget:   b,
		class:    class,
		send:     send,
		maxQueue: maxQueue,
		ch:       make(chan Packet, maxQueue),
	}
	p.wg.Add(1)
	go p.loop()
	return p
}

// Enqueue tries to accept pkt. Returns false if the pacer is closed or the queue is full
// (hard drop; QueueDrops is incremented).
func (p *Pacer) Enqueue(pkt Packet) bool {
	if p == nil || p.closed.Load() {
		return false
	}
	select {
	case p.ch <- pkt:
		p.Enqueued.Add(1)
		return true
	default:
		p.QueueDrops.Add(1)
		return false
	}
}

// Depth returns the current queue occupancy.
func (p *Pacer) Depth() int {
	if p == nil {
		return 0
	}
	return len(p.ch)
}

// Close stops accepting packets and waits for the worker to finish draining or exit.
// Safe to call multiple times.
func (p *Pacer) Close() {
	if p == nil || !p.closed.CompareAndSwap(false, true) {
		return
	}
	close(p.ch)
	p.wg.Wait()
}

func (p *Pacer) loop() {
	defer p.wg.Done()
	for pkt := range p.ch {
		n := len(pkt.Payload)
		if p.budget != nil {
			if w := p.budget.Acquire(p.class, n); w > 0 {
				timer := newTimer(w)
				<-timer.C
				timer.Stop()
			}
		}
		p.send(pkt)
		p.Sent.Add(1)
	}
}

// Snapshot merges budget + pacer diagnostics.
func (p *Pacer) Snapshot() Snapshot {
	var s Snapshot
	if p.budget != nil {
		s = p.budget.Snapshot()
	}
	s.QueueDrops = p.QueueDrops.Load()
	s.QueueDepth = p.Depth()
	return s
}

// RunWithContext closes the pacer when ctx is done. Optional convenience for callers.
func (p *Pacer) RunWithContext(ctx context.Context) {
	go func() {
		<-ctx.Done()
		p.Close()
	}()
}
