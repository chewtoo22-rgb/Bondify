package diagnostics

import (
	"strings"
	"testing"
	"time"
)

func TestBuildSnapshotNormalizesBoundsAndSorts(t *testing.T) {
	paths := []PathState{
		{Label: " zeta\n", Role: "PRIMARY!!", Status: "UP", RTTMillis: -1, LossPermille: 1200},
		{Label: "alpha", Role: "backup", Status: "down", RTTMillis: 8, LossPermille: 2},
	}
	got := BuildSnapshot(time.Date(2026, 8, 30, 12, 0, 0, 0, time.FixedZone("EDT", -4*60*60)), " SPEED!! ", true, paths)
	if got.GeneratedAt.Location() != time.UTC || got.Mode != "speed" || !got.Connected || got.PathCount != 2 {
		t.Fatalf("unexpected snapshot header: %+v", got)
	}
	if got.Paths[0].Label != "alpha" || got.Paths[1].Label != "zeta" {
		t.Fatalf("paths not sanitized/sorted: %+v", got.Paths)
	}
	if got.Paths[1].Role != "primary" || got.Paths[1].RTTMillis != 0 || got.Paths[1].LossPermille != 1000 {
		t.Fatalf("path not normalized: %+v", got.Paths[1])
	}
}

func TestBuildSnapshotCapsPathsAndRuneLabels(t *testing.T) {
	paths := make([]PathState, MaxPaths+4)
	for i := range paths {
		paths[i] = PathState{Label: strings.Repeat("x", MaxLabelLength+10)}
	}
	got := BuildSnapshot(time.Time{}, "", false, paths)
	if got.PathCount != MaxPaths || len(got.Paths) != MaxPaths {
		t.Fatalf("path cap failed: field=%d len=%d", got.PathCount, len(got.Paths))
	}
	if len([]rune(got.Paths[0].Label)) != MaxLabelLength || got.Mode != "unknown" {
		t.Fatalf("bounds/defaults failed: %+v", got)
	}
}
