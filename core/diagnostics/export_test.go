package diagnostics

import (
	"encoding/json"
	"testing"
	"time"
)

func TestMarshalSupportExportReNormalizesAndIsDeterministic(t *testing.T) {
	input := Snapshot{
		GeneratedAt: time.Date(2026, 8, 30, 12, 0, 0, 0, time.FixedZone("EDT", -4*60*60)),
		Mode: " SPEED!! ",
		PathCount: 999,
		Paths: []PathState{{Label: "z\n", Role: "PRIMARY!!", Status: "UP", RTTMillis: -3, LossPermille: 1200}},
	}
	first, err := MarshalSupportExport(input)
	if err != nil { t.Fatal(err) }
	second, err := MarshalSupportExport(input)
	if err != nil { t.Fatal(err) }
	if string(first) != string(second) { t.Fatal("support export is not deterministic") }
	if len(first) > MaxSupportExportBytes { t.Fatalf("payload too large: %d", len(first)) }
	var got SupportExport
	if err := json.Unmarshal(first, &got); err != nil { t.Fatal(err) }
	if got.SchemaVersion != 1 || got.Product != "bondify" || got.Snapshot.PathCount != 1 || got.Snapshot.Mode != "speed" {
		t.Fatalf("unexpected export: %+v", got)
	}
	p := got.Snapshot.Paths[0]
	if p.Label != "z" || p.Role != "primary" || p.Status != "up" || p.RTTMillis != 0 || p.LossPermille != 1000 {
		t.Fatalf("snapshot not normalized: %+v", p)
	}
}
