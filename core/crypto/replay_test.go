package crypto

import "testing"

func TestReplayWindowInOrder(t *testing.T) {
	w := newReplayWindow(64)
	for i := uint64(0); i < 200; i++ {
		if !w.CheckOnly(i) {
			t.Fatalf("counter %d should be accepted (first time)", i)
		}
		w.MarkSeen(i)
		if w.CheckOnly(i) {
			t.Fatalf("counter %d should be rejected as duplicate immediately after MarkSeen", i)
		}
	}
}

func TestReplayWindowOutOfOrderWithinWindow(t *testing.T) {
	w := newReplayWindow(64)
	order := []uint64{5, 3, 4, 1, 2, 0, 6}
	for _, c := range order {
		if !w.CheckOnly(c) {
			t.Fatalf("counter %d should be accepted", c)
		}
		w.MarkSeen(c)
	}
	for _, c := range order {
		if w.CheckOnly(c) {
			t.Fatalf("counter %d should now be rejected as duplicate", c)
		}
	}
}

func TestReplayWindowTooOld(t *testing.T) {
	w := newReplayWindow(64)
	for i := uint64(0); i < 200; i++ {
		w.MarkSeen(i)
	}
	// 199 is max; window size 64, so anything <= 199-64 is out of window.
	if w.CheckOnly(100) {
		t.Fatalf("counter 100 should be rejected as too old (max=199, window=64)")
	}
	if w.CheckOnly(199) {
		t.Fatalf("counter 199 was already marked seen, should be rejected as duplicate")
	}
}

func TestReplayWindowDuplicateAfterJump(t *testing.T) {
	w := newReplayWindow(128)
	w.MarkSeen(1000)
	w.MarkSeen(999)
	if w.CheckOnly(999) {
		t.Fatal("999 should be a duplicate")
	}
	if w.CheckOnly(1000) {
		t.Fatal("1000 should be a duplicate")
	}
	if !w.CheckOnly(1001) {
		t.Fatal("1001 should be accepted (new max)")
	}
	w.MarkSeen(1001)
	if w.CheckOnly(1000) {
		t.Fatal("1000 should still be a duplicate after window advanced")
	}
	if w.CheckOnly(999) {
		t.Fatal("999 should still be a duplicate after window advanced")
	}
}

func TestReplayWindowFirstPacketZero(t *testing.T) {
	w := newReplayWindow(64)
	if !w.CheckOnly(0) {
		t.Fatal("very first counter (0) should be accepted")
	}
	w.MarkSeen(0)
	if w.CheckOnly(0) {
		t.Fatal("0 should now be a duplicate")
	}
	if !w.CheckOnly(1) {
		t.Fatal("1 should be accepted")
	}
}
