package diagnostics

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestMarshalSupportExportNormalizesDirectSnapshotConstruction(t *testing.T) {
	input := Snapshot{
		GeneratedAt: time.Date(2026, 8, 29, 14, 30, 0, 0, time.FixedZone("offset", 3600)),
		Mode:        " SPEED!!! ",
		Connected:   true,
		PathCount:   999,
		Paths: []PathState{
			{Label: " zeta\npath ", Role: " PRIMARY!! ", Status: " UP ", RTTMillis: -4, LossPermille: 1200, TxKbps: -1, RxKbps: 42},
			{Label: "alpha", Role: "backup", Status: "down", RTTMillis: 5, LossPermille: 2, TxKbps: 3, RxKbps: 4},
		},
	}

	payload, err := MarshalSupportExport(input)
	if err != nil {
		t.Fatalf("MarshalSupportExport() error = %v", err)
	}
	if !strings.HasSuffix(string(payload), "\n") || strings.HasSuffix(string(payload), "\n\n") {
		t.Fatalf("export must contain exactly one trailing newline: %q", payload)
	}

	var got SupportExport
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if got.SchemaVersion != SupportExportSchemaVersion || got.Product != "bondify" {
		t.Fatalf("unexpected envelope: %+v", got)
	}
	if got.Snapshot.GeneratedAt.Location() != time.UTC {
		t.Fatalf("generated_at location = %v, want UTC", got.Snapshot.GeneratedAt.Location())
	}
	if got.Snapshot.Mode != "speed" {
		t.Fatalf("mode = %q, want speed", got.Snapshot.Mode)
	}
	if got.Snapshot.PathCount != 2 || len(got.Snapshot.Paths) != 2 {
		t.Fatalf("path count mismatch: field=%d len=%d", got.Snapshot.PathCount, len(got.Snapshot.Paths))
	}
	if got.Snapshot.Paths[0].Label != "alpha" || got.Snapshot.Paths[1].Label != "zetapath" {
		t.Fatalf("paths not sanitized/sorted: %+v", got.Snapshot.Paths)
	}
	zeta := got.Snapshot.Paths[1]
	if zeta.Role != "primary" || zeta.Status != "up" || zeta.RTTMillis != 0 || zeta.LossPermille != 1000 || zeta.TxKbps != 0 {
		t.Fatalf("path metrics not normalized: %+v", zeta)
	}
}

func TestMarshalSupportExportBoundsDirectlyConstructedPaths(t *testing.T) {
	paths := make([]PathState, MaxPaths+8)
	for i := range paths {
		paths[i] = PathState{Label: strings.Repeat("x", MaxLabelLength+20), Role: "primary", Status: "up"}
	}

	payload, err := MarshalSupportExport(Snapshot{Paths: paths})
	if err != nil {
		t.Fatalf("MarshalSupportExport() error = %v", err)
	}
	if len(payload) > MaxSupportExportBytes {
		t.Fatalf("payload size = %d, max = %d", len(payload), MaxSupportExportBytes)
	}

	var got SupportExport
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if len(got.Snapshot.Paths) != MaxPaths || got.Snapshot.PathCount != MaxPaths {
		t.Fatalf("paths not bounded: field=%d len=%d", got.Snapshot.PathCount, len(got.Snapshot.Paths))
	}
	for _, path := range got.Snapshot.Paths {
		if len([]rune(path.Label)) > MaxLabelLength {
			t.Fatalf("label exceeded rune bound: %q", path.Label)
		}
	}
}

func TestMarshalSupportExportIsDeterministic(t *testing.T) {
	input := Snapshot{
		GeneratedAt: time.Unix(123, 0),
		Mode:        "redundant",
		Paths: []PathState{
			{Label: "b", Role: "backup", Status: "up"},
			{Label: "a", Role: "primary", Status: "up"},
		},
	}

	first, err := MarshalSupportExport(input)
	if err != nil {
		t.Fatalf("first export error = %v", err)
	}
	second, err := MarshalSupportExport(input)
	if err != nil {
		t.Fatalf("second export error = %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("exports differ:\nfirst:  %s\nsecond: %s", first, second)
	}
}
