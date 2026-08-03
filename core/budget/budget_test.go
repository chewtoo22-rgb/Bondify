package budget

import (
	"testing"
	"time"

	"github.com/chewtoo22-rgb/bondify/core/classify"
)

func TestUnlimitedAlwaysAllows(t *testing.T) {
	b := New(Unlimited())
	for _, c := range []classify.Class{classify.Bulk, classify.Latency, classify.Realtime, classify.Interactive} {
		if !b.Allow(c, 1<<20) {
			t.Fatalf("%s should be unlimited", c)
		}
	}
	if !((*Budget)(nil)).Allow(classify.Bulk, 100) {
		t.Fatal("nil Budget should allow")
	}
}

func TestBulkCap(t *testing.T) {
	// 1000 bytes/sec, burst 1000.
	b := New(Config{BulkBPS: 1000})
	if !b.Allow(classify.Bulk, 800) {
		t.Fatal("first 800 should fit in burst")
	}
	if b.Allow(classify.Bulk, 800) {
		t.Fatal("second 800 should be denied (only ~200 tokens left)")
	}
	// Latency is still unlimited.
	if !b.Allow(classify.Latency, 1<<20) {
		t.Fatal("latency should be unlimited")
	}
	// Wait for refill.
	time.Sleep(600 * time.Millisecond)
	if !b.Allow(classify.Bulk, 400) {
		t.Fatal("after refill, 400 should be allowed")
	}
}

func TestRemaining(t *testing.T) {
	b := New(Config{BulkBPS: 5000})
	_ = b.Allow(classify.Bulk, 1000)
	r := b.Remaining(classify.Bulk)
	if r < 3000 || r > 5000 {
		t.Fatalf("Remaining = %v, want roughly 4000", r)
	}
}
