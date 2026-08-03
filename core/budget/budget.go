// Package budget implements Phase 7 per-class bandwidth budgets (ARCHITECTURE.md §5).
//
// The Phase 7 gate requires that a loaded bulk download does not push interactive
// latency (SSH RTT) more than 25% above the unloaded baseline. Class-aware scheduling
// already reserves HEADROOM on every path for non-bulk traffic; budgets add an optional
// explicit rate ceiling so bulk cannot consume the entire residual capacity either.
//
// Default behaviour: unlimited for LATENCY / REALTIME / INTERACTIVE; BULK is soft-capped
// only when a Budget is configured with BulkBPS > 0. Unlimited is represented as 0.
package budget

import (
	"sync"
	"time"

	"github.com/chewtoo22-rgb/bondify/core/classify"
)

// Config holds optional per-class byte-per-second ceilings. Zero means unlimited.
type Config struct {
	LatencyBPS     int64
	RealtimeBPS    int64
	InteractiveBPS int64
	BulkBPS        int64
}

// Unlimited is the zero-value config: no class is rate-limited.
func Unlimited() Config { return Config{} }

// Budget is a set of per-class token buckets. Safe for concurrent use.
type Budget struct {
	mu    sync.Mutex
	class map[classify.Class]*bucket
}

type bucket struct {
	rate   float64 // bytes per second; 0 = unlimited
	tokens float64
	cap    float64 // burst = 1 second of rate
	last   time.Time
}

// New builds a Budget from cfg. Classes with BPS == 0 are unlimited.
func New(cfg Config) *Budget {
	b := &Budget{class: make(map[classify.Class]*bucket)}
	add := func(c classify.Class, bps int64) {
		if bps <= 0 {
			b.class[c] = &bucket{} // unlimited
			return
		}
		b.class[c] = &bucket{
			rate:   float64(bps),
			tokens: float64(bps), // start full
			cap:    float64(bps),
			last:   time.Now(),
		}
	}
	add(classify.Latency, cfg.LatencyBPS)
	add(classify.Realtime, cfg.RealtimeBPS)
	add(classify.Interactive, cfg.InteractiveBPS)
	add(classify.Bulk, cfg.BulkBPS)
	return b
}

// Allow reports whether n bytes of class c may be sent right now. Unlimited classes
// always return true. On true, tokens are consumed; on false, the caller should drop
// or queue the packet (Bondify currently drops — same as no-eligible-path).
func (b *Budget) Allow(c classify.Class, n int) bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	bk, ok := b.class[c]
	if !ok || bk.rate <= 0 {
		return true // unlimited
	}
	now := time.Now()
	elapsed := now.Sub(bk.last).Seconds()
	if elapsed > 0 {
		bk.tokens += elapsed * bk.rate
		if bk.tokens > bk.cap {
			bk.tokens = bk.cap
		}
		bk.last = now
	}
	need := float64(n)
	if bk.tokens < need {
		return false
	}
	bk.tokens -= need
	return true
}

// Remaining returns approximate tokens left for class c (0 if unlimited or unknown).
// Intended for diagnostics, not control decisions.
func (b *Budget) Remaining(c classify.Class) float64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	bk, ok := b.class[c]
	if !ok || bk.rate <= 0 {
		return 0
	}
	now := time.Now()
	elapsed := now.Sub(bk.last).Seconds()
	tokens := bk.tokens + elapsed*bk.rate
	if tokens > bk.cap {
		tokens = bk.cap
	}
	return tokens
}
