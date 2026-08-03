package bond

import (
	"context"
	"fmt"

	"github.com/chewtoo22-rgb/bondify/core/budget"
)

const (
	// DefaultEgressQueuePackets bounds the ordinary scheduler retry queue to roughly 3 MiB
	// at Bondify's normal MTU. This queue is required now that InFlight is real: Scheduler.Next
	// can legitimately return nil at CWND instead of only when every path is dead.
	DefaultEgressQueuePackets = 2048
	// DefaultBulkQueuePackets independently bounds classified BULK traffic waiting for its
	// byte-rate budget or reserved congestion-window headroom.
	DefaultBulkQueuePackets = 2048
)

func resolvedQueuePackets(configured, fallback int) int {
	if configured > 0 {
		return configured
	}
	return fallback
}

func newPacketPacer(ctx context.Context, name string, cfg budget.Config, queuePackets int, send budget.SendFunc) (*budget.Pacer, error) {
	limiter, err := budget.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("bond: %s limiter: %w", name, err)
	}
	p, err := budget.NewPacer(ctx, limiter, queuePackets, send)
	if err != nil {
		return nil, fmt.Errorf("bond: %s pacer: %w", name, err)
	}
	return p, nil
}

func (t *ClientTunnel) startPacers(ctx context.Context) error {
	if t.mode == ModeRedundant {
		return nil
	}
	egress, err := newPacketPacer(ctx, "egress", budget.Config{},
		resolvedQueuePackets(t.egressQueue, DefaultEgressQueuePackets), func(pkt []byte) bool {
			return t.trySendSpeed(pkt)
		})
	if err != nil {
		return err
	}
	if !t.egressPacer.CompareAndSwap(nil, egress) {
		egress.Close()
		return fmt.Errorf("bond: client tunnel Run called more than once")
	}
	if !t.classify {
		return nil
	}
	bulk, err := newPacketPacer(ctx, "bulk", t.bulkBudget,
		resolvedQueuePackets(t.bulkQueue, DefaultBulkQueuePackets), func(pkt []byte) bool {
			return t.trySendSpeedCapped(pkt, bulkHeadroomFraction)
		})
	if err != nil {
		t.egressPacer.CompareAndSwap(egress, nil)
		egress.Close()
		return err
	}
	if !t.bulkPacer.CompareAndSwap(nil, bulk) {
		bulk.Close()
		t.egressPacer.CompareAndSwap(egress, nil)
		egress.Close()
		return fmt.Errorf("bond: client tunnel Run called more than once")
	}
	return nil
}
