package bond

import (
	"context"
	"fmt"

	"github.com/chewtoo22-rgb/bondify/core/budget"
)

// DefaultBulkQueuePackets bounds queued BULK traffic to roughly 3 MiB at Bondify's normal
// MTU. The exact memory use varies with packet size. Operators can lower it for large relay
// fan-out or raise it for high-bandwidth, high-delay paths; diagnostics make saturation
// visible either way.
const DefaultBulkQueuePackets = 2048

func resolvedBulkQueuePackets(configured int) int {
	if configured > 0 {
		return configured
	}
	return DefaultBulkQueuePackets
}

func newBulkPacer(ctx context.Context, cfg budget.Config, queuePackets int, send budget.SendFunc) (*budget.Pacer, error) {
	limiter, err := budget.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("bond: bulk limiter: %w", err)
	}
	p, err := budget.NewPacer(ctx, limiter, resolvedBulkQueuePackets(queuePackets), send)
	if err != nil {
		return nil, fmt.Errorf("bond: bulk pacer: %w", err)
	}
	return p, nil
}

func (t *ClientTunnel) startBulkPacer(ctx context.Context) error {
	if !t.classify || t.mode == ModeRedundant {
		return nil
	}
	p, err := newBulkPacer(ctx, t.bulkBudget, t.bulkQueue, func(pkt []byte) bool {
		return t.trySendSpeedCapped(pkt, bulkHeadroomFraction)
	})
	if err != nil {
		return err
	}
	if !t.bulkPacer.CompareAndSwap(nil, p) {
		p.Close()
		return fmt.Errorf("bond: client tunnel Run called more than once")
	}
	return nil
}
