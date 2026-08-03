package bond

import (
	"context"
	"testing"

	"github.com/chewtoo22-rgb/bondify/core/budget"
)

func TestClientBudgetRequiresClassifiedSpeedMode(t *testing.T) {
	for _, cfg := range []ClientConfig{
		{BulkBudget: budget.Config{BytesPerSecond: 1}},
		{BulkBudget: budget.Config{BytesPerSecond: 1}, Classify: true, Mode: ModeRedundant},
	} {
		if _, _, err := DialHandshake(context.Background(), cfg); err == nil {
			t.Fatalf("DialHandshake accepted inert bulk budget: %+v", cfg)
		}
	}
}

func TestRelayBudgetRequiresClassifiedSpeedMode(t *testing.T) {
	for _, cfg := range []RelayConfig{
		{BulkBudget: budget.Config{BytesPerSecond: 1}},
		{BulkBudget: budget.Config{BytesPerSecond: 1}, Classify: true, Mode: ModeRedundant},
	} {
		if _, err := NewRelay(cfg, nil); err == nil {
			t.Fatalf("NewRelay accepted inert bulk budget: %+v", cfg)
		}
	}
}

func TestResolvedQueuePackets(t *testing.T) {
	if got := resolvedQueuePackets(0, DefaultBulkQueuePackets); got != DefaultBulkQueuePackets {
		t.Fatalf("resolved zero queue = %d, want default %d", got, DefaultBulkQueuePackets)
	}
	if got := resolvedQueuePackets(17, DefaultBulkQueuePackets); got != 17 {
		t.Fatalf("resolved explicit queue = %d, want 17", got)
	}
}
