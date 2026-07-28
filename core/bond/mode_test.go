package bond

import (
	"testing"
	"time"
)

func TestModeFromString(t *testing.T) {
	cases := map[string]Mode{"": ModeSpeed, "speed": ModeSpeed, "redundant": ModeRedundant}
	for in, want := range cases {
		got, err := ModeFromString(in)
		if err != nil {
			t.Errorf("ModeFromString(%q) error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ModeFromString(%q) = %v, want %v", in, got, want)
		}
	}
	if _, err := ModeFromString("bogus"); err == nil {
		t.Error("expected an error for an unknown mode")
	}
}

func TestModeString(t *testing.T) {
	if ModeSpeed.String() != "SPEED" || ModeRedundant.String() != "REDUNDANT" {
		t.Errorf("Mode.String() unexpected: %q, %q", ModeSpeed, ModeRedundant)
	}
}

func TestSelectRedundantPathsPrefersLowerRTT(t *testing.T) {
	slow := NewPath(0, nil)
	slow.SetActive()
	fast := NewPath(1, nil)
	fast.SetActive()
	// Force real RTT samples via HandleProbeAck rather than poking unexported fields.
	now := time.Now()
	slow.HandleProbeAck(ProbeAckPayload{SentAtUnixNano: now.Add(-80 * time.Millisecond).UnixNano()}, now)
	fast.HandleProbeAck(ProbeAckPayload{SentAtUnixNano: now.Add(-10 * time.Millisecond).UnixNano()}, now)

	got := selectRedundantPaths([]*Path{slow, fast}, 2)
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	if got[0].ID() != 1 || got[1].ID() != 0 {
		t.Errorf("order = [%d,%d], want fast(1) before slow(0)", got[0].ID(), got[1].ID())
	}
}

func TestSelectRedundantPathsCapsAtN(t *testing.T) {
	var paths []*Path
	for i := uint8(0); i < 4; i++ {
		p := NewPath(i, nil)
		p.SetActive()
		paths = append(paths, p)
	}
	got := selectRedundantPaths(paths, 2)
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
}

func TestSelectRedundantPathsFewerThanNIsFine(t *testing.T) {
	p := NewPath(0, nil)
	p.SetActive()
	got := selectRedundantPaths([]*Path{p}, DupFactor)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1 (only one eligible path exists)", len(got))
	}
}

func TestSelectRedundantPathsExcludesIneligible(t *testing.T) {
	dead := NewPath(0, nil) // stays JOINING, never activated
	active := NewPath(1, nil)
	active.SetActive()
	got := selectRedundantPaths([]*Path{dead, active}, DupFactor)
	if len(got) != 1 || got[0].ID() != 1 {
		t.Fatalf("got = %v, want only the active path", got)
	}
}

// setLoss drives a path's lossEWMA toward the given ratio by repeatedly feeding
// HandleProbeAck the same instLoss sample -- alpha=0.2 per PROTOCOL.md §6, so ~30
// iterations converges well within floating-point-comparison precision of target. Each
// call's SentPSN/RecvPSN are cumulative running totals (matching how the real PSN
// counters work), incremented by a fixed batch per iteration so every iteration's
// deltaSent/deltaRecv -- and so its instLoss -- stays at exactly ratio.
func setLoss(p *Path, ratio float64) {
	now := time.Now()
	const batch = 100
	recvBatch := uint32(float64(batch) * (1 - ratio))
	var sentPSN, recvPSN uint32
	for i := 0; i < 30; i++ {
		sentPSN += batch
		recvPSN += recvBatch
		p.HandleProbeAck(ProbeAckPayload{SentAtUnixNano: now.UnixNano(), SentPSN: sentPSN, RecvPSN: recvPSN}, now)
	}
}

func TestHealthiestPathPrefersLowerLoss(t *testing.T) {
	lossy := NewPath(0, nil)
	lossy.SetActive()
	setLoss(lossy, 0.10)
	clean := NewPath(1, nil)
	clean.SetActive()
	setLoss(clean, 0.01)

	got := healthiestPath([]*Path{lossy, clean})
	if got == nil || got.ID() != 1 {
		t.Fatalf("got = %v, want the lower-loss path (1)", got)
	}
}

func TestHealthiestPathTieBreaksOnRTT(t *testing.T) {
	slow := NewPath(0, nil)
	slow.SetActive()
	fast := NewPath(1, nil)
	fast.SetActive()
	now := time.Now()
	slow.HandleProbeAck(ProbeAckPayload{SentAtUnixNano: now.Add(-80 * time.Millisecond).UnixNano()}, now)
	fast.HandleProbeAck(ProbeAckPayload{SentAtUnixNano: now.Add(-10 * time.Millisecond).UnixNano()}, now)
	// Both paths' loss stays at 0 (deltaSent==0 on every call above): an equal-loss tie.

	got := healthiestPath([]*Path{slow, fast})
	if got == nil || got.ID() != 1 {
		t.Fatalf("got = %v, want the lower-RTT path (1) as the equal-loss tie-break", got)
	}
}

func TestHealthiestPathNilWhenNoneEligible(t *testing.T) {
	dead := NewPath(0, nil) // stays JOINING, never activated
	if got := healthiestPath([]*Path{dead}); got != nil {
		t.Fatalf("got = %v, want nil (no eligible path)", got)
	}
	if got := healthiestPath(nil); got != nil {
		t.Fatalf("got = %v, want nil for an empty path list", got)
	}
}
