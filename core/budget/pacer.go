package budget

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

const schedulerRetryDelay = time.Millisecond

var (
	// ErrClosed means the pacer is shutting down and no longer accepts traffic.
	ErrClosed = errors.New("budget: pacer closed")
	// ErrQueueFull is an explicit, observable overload drop. Rate limiting itself waits;
	// only exhaustion of the configured memory bound can discard a packet.
	ErrQueueFull = errors.New("budget: pacer queue full")
)

// SendFunc attempts to deliver one packet. It returns false when the scheduler currently
// has no eligible path; Pacer retries without charging the packet's byte budget twice.
type SendFunc func([]byte) bool

// Pacer owns copied packets in a bounded queue and drains them under a Limiter. It never
// closes its queue channel: Close cancels a context instead, so Enqueue racing with Close
// cannot panic by sending on a closed channel.
type Pacer struct {
	ctx    context.Context
	cancel context.CancelFunc
	limit  *Limiter
	send   SendFunc
	queue  chan []byte
	wg     sync.WaitGroup
	accept sync.RWMutex
	closed atomic.Bool

	enqueuedPackets atomic.Uint64
	enqueuedBytes   atomic.Uint64
	sentPackets     atomic.Uint64
	sentBytes       atomic.Uint64
	queueDrops      atomic.Uint64
	queueDropBytes  atomic.Uint64
	schedulerWaits  atomic.Uint64
}

// PacerSnapshot is JSON-ready pacing telemetry.
type PacerSnapshot struct {
	Limiter         Snapshot `json:"limiter"`
	QueueDepth      int      `json:"queue_depth"`
	QueueCapacity   int      `json:"queue_capacity"`
	EnqueuedPackets uint64   `json:"enqueued_packets"`
	EnqueuedBytes   uint64   `json:"enqueued_bytes"`
	SentPackets     uint64   `json:"sent_packets"`
	SentBytes       uint64   `json:"sent_bytes"`
	QueueDrops      uint64   `json:"queue_drops"`
	QueueDropBytes  uint64   `json:"queue_drop_bytes"`
	SchedulerWaits  uint64   `json:"scheduler_waits"`
}

// NewPacer starts a worker. queuePackets is a hard memory bound and must be positive.
func NewPacer(parent context.Context, limit *Limiter, queuePackets int, send SendFunc) (*Pacer, error) {
	if parent == nil {
		return nil, errors.New("budget: parent context is nil")
	}
	if queuePackets < 1 {
		return nil, errors.New("budget: queue capacity must be >= 1")
	}
	if send == nil {
		return nil, errors.New("budget: send function is nil")
	}
	ctx, cancel := context.WithCancel(parent)
	p := &Pacer{
		ctx:    ctx,
		cancel: cancel,
		limit:  limit,
		send:   send,
		queue:  make(chan []byte, queuePackets),
	}
	p.wg.Add(1)
	go p.loop()
	return p, nil
}

// Enqueue copies pkt before returning, so callers may immediately reuse their TUN buffer.
func (p *Pacer) Enqueue(pkt []byte) error {
	if p == nil {
		return ErrClosed
	}
	p.accept.RLock()
	defer p.accept.RUnlock()
	if p.closed.Load() || p.ctx.Err() != nil {
		return ErrClosed
	}
	cp := append([]byte(nil), pkt...)
	select {
	case <-p.ctx.Done():
		return ErrClosed
	case p.queue <- cp:
		p.enqueuedPackets.Add(1)
		p.enqueuedBytes.Add(uint64(len(cp)))
		return nil
	default:
		p.queueDrops.Add(1)
		p.queueDropBytes.Add(uint64(len(cp)))
		return ErrQueueFull
	}
}

// Close is idempotent, promptly interrupts limiter/scheduler waits, and is safe to race
// with Enqueue.
func (p *Pacer) Close() {
	if p == nil || !p.closed.CompareAndSwap(false, true) {
		return
	}
	p.accept.Lock()
	p.cancel()
	p.accept.Unlock()
	p.wg.Wait()
}

func (p *Pacer) loop() {
	defer p.wg.Done()
	for {
		select {
		case <-p.ctx.Done():
			return
		case pkt := <-p.queue:
			if err := p.limit.Wait(p.ctx, len(pkt)); err != nil {
				return
			}
			for !p.send(pkt) {
				p.schedulerWaits.Add(1)
				t := time.NewTimer(schedulerRetryDelay)
				select {
				case <-p.ctx.Done():
					t.Stop()
					return
				case <-t.C:
				}
			}
			p.sentPackets.Add(1)
			p.sentBytes.Add(uint64(len(pkt)))
		}
	}
}

// Snapshot returns a race-free view of the limiter and queue.
func (p *Pacer) Snapshot() PacerSnapshot {
	if p == nil {
		return PacerSnapshot{}
	}
	return PacerSnapshot{
		Limiter:         p.limit.Snapshot(),
		QueueDepth:      len(p.queue),
		QueueCapacity:   cap(p.queue),
		EnqueuedPackets: p.enqueuedPackets.Load(),
		EnqueuedBytes:   p.enqueuedBytes.Load(),
		SentPackets:     p.sentPackets.Load(),
		SentBytes:       p.sentBytes.Load(),
		QueueDrops:      p.queueDrops.Load(),
		QueueDropBytes:  p.queueDropBytes.Load(),
		SchedulerWaits:  p.schedulerWaits.Load(),
	}
}
