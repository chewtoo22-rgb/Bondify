package bond

import "testing"

func TestRelayNewSessionIndexAvoidsZeroAndLiveIndexes(t *testing.T) {
	r := &Relay{byIndex: make(map[uint32]*relaySession)}

	const samples = 4096
	seen := make(map[uint32]struct{}, samples)
	for i := 0; i < samples; i++ {
		idx := r.newSessionIndex()
		if idx == 0 {
			t.Fatal("newSessionIndex returned reserved zero session index")
		}
		if _, exists := seen[idx]; exists {
			t.Fatalf("newSessionIndex returned live session index %08x", idx)
		}

		seen[idx] = struct{}{}
		r.byIndex[idx] = &relaySession{sessionIndex: idx}
	}

	if got := len(r.byIndex); got != samples {
		t.Fatalf("live session index count = %d, want %d", got, samples)
	}
}
