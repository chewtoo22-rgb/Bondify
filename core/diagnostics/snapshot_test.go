package diagnostics

import (
	"strings"
	"testing"
	"time"
)

func TestBuildSnapshotNormalizesAndSorts(t *testing.T) {
	now := time.Date(2026, 8, 29, 4, 30, 0, 0, time.FixedZone("EDT", -4*60*60))
	got := BuildSnapshot(now, " SPEED ", true, []PathState{
		{Label: " Cellular ", Role: "BONDED", Status: "UP", RTTMillis: -9, LossPermille: 1200, TxKbps: -1, RxKbps: 90},
		{Label: "Wi-Fi", Role: " PRIMARY ", Status: " Healthy ", RTTMillis: 18, LossPermille: 4, TxKbps: 400, RxKbps: 900},
	})

	if got.Mode != "speed" || !got.Connected || got.PathCount != 2 {
		t.Fatalf("unexpected snapshot header: %+v", got)
	}
	if got.GeneratedAt.Location() != time.UTC {
		t.Fatalf("GeneratedAt must be UTC: %v", got.GeneratedAt)
	}
	if got.Paths[0].Label != "Cellular" || got.Paths[1].Label != "Wi-Fi" {
		t.Fatalf("paths not deterministically sorted: %+v", got.Paths)
	}
	if got.Paths[0].RTTMillis != 0 || got.Paths[0].LossPermille != 1000 || got.Paths[0].TxKbps != 0 {
		t.Fatalf("metrics not clamped: %+v", got.Paths[0])
	}
	if got.Paths[0].Role != "bonded" || got.Paths[0].Status != "up" {
		t.Fatalf("tokens not normalized: %+v", got.Paths[0])
	}
}

func TestBuildSnapshotBoundsPathsAndLabels(t *testing.T) {
	paths := make([]PathState, MaxPaths+7)
	for i := range paths {
		paths[i] = PathState{Label: strings.Repeat("x", MaxLabelLength+20)}
	}
	got := BuildSnapshot(time.Time{}, "", false, paths)
	if got.PathCount != MaxPaths || len(got.Paths) != MaxPaths {
		t.Fatalf("path bound not enforced: count=%d len=%d", got.PathCount, len(got.Paths))
	}
	if len([]rune(got.Paths[0].Label)) != MaxLabelLength {
		t.Fatalf("label bound not enforced: %q", got.Paths[0].Label)
	}
	if !got.GeneratedAt.Equal(time.Unix(0, 0).UTC()) {
		t.Fatalf("zero time fallback changed: %v", got.GeneratedAt)
	}
	if got.Mode != "unknown" {
		t.Fatalf("empty mode should fail closed to unknown: %q", got.Mode)
	}
}

func TestBuildSnapshotStripsControlCharacters(t *testing.T) {
	got := BuildSnapshot(time.Now(), "custom", false, []PathState{{
		Label:  "WAN\n\tOne",
		Role:   "###",
		Status: "",
	}})
	p := got.Paths[0]
	if p.Label != "WANOne" {
		t.Fatalf("control characters leaked into label: %q", p.Label)
	}
	if p.Role != "unknown" || p.Status != "unknown" {
		t.Fatalf("invalid tokens should fail closed: %+v", p)
	}
}
