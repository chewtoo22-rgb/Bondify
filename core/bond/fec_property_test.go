package bond

import (
	"testing"

	"github.com/chewtoo22-rgb/bondify/core/fec"
)

// --- Additional FEC + reorder-adjacent property coverage (release hardening) ---

func TestFECGenBufferDuplicateParityIgnored(t *testing.T) {
	const n, m, w = 3, 1, 8
	payloads := [][]byte{[]byte("a"), []byte("b"), []byte("c")}
	parity, err := fec.EncodeParity(payloads, w, m)
	if err != nil {
		t.Fatalf("EncodeParity: %v", err)
	}
	buf := newFECGenBuffer()
	buf.HandleData(20, 0, payloads[0])
	buf.HandleData(20, 2, payloads[2])
	// First parity reconstructs missing shard 1 and retires the generation.
	r1 := buf.HandleFEC(20, n, m, w, 0, parity[0])
	if r1 == nil || string(r1[1]) != "b" {
		t.Fatalf("first parity recovery = %v", r1)
	}
	if len(buf.gens) != 0 {
		t.Fatalf("generation not retired after successful recovery: %#v", buf.gens)
	}
	// A late duplicate parity may allocate a short-lived empty generation entry
	// (getOrCreate on HandleFEC). It must not report another recovery, and GC must
	// reclaim it — that is the resource bound for authenticated parity spray.
	r2 := buf.HandleFEC(20, n, m, w, 0, parity[0])
	if r2 != nil {
		t.Fatalf("duplicate parity after retirement recovered again: %v", r2)
	}
	buf.GC(0)
	if len(buf.gens) != 0 {
		t.Fatalf("GC did not reclaim post-retirement duplicate parity state: %#v", buf.gens)
	}
}

func TestFECGenBufferStaleGenerationAfterGC(t *testing.T) {
	buf := newFECGenBuffer()
	buf.HandleData(30, 0, []byte("old"))
	if len(buf.gens) != 1 {
		t.Fatalf("len(gens)=%d want 1", len(buf.gens))
	}
	buf.GC(0)
	if len(buf.gens) != 0 {
		t.Fatal("GC(0) did not clear stale generation")
	}
	// Late original after GC must start a fresh generation, not revive the GC'd one with
	// mixed state.
	buf.HandleData(30, 1, []byte("late"))
	if len(buf.gens) != 1 {
		t.Fatalf("len(gens) after late data = %d, want 1", len(buf.gens))
	}
	g := buf.gens[30]
	if _, ok := g.data[0]; ok {
		t.Fatal("GC'd data shard 0 leaked into revived generation")
	}
	if string(g.data[1]) != "late" {
		t.Fatalf("data[1]=%q want late", g.data[1])
	}
}

func TestFECGenBufferConflictingGeometryRejected(t *testing.T) {
	buf := newFECGenBuffer()
	buf.HandleData(40, 0, []byte("a"))
	// First FEC establishes geometry n=3,m=1,w=8.
	_ = buf.HandleFEC(40, 3, 1, 8, 0, make([]byte, 8))
	if len(buf.gens) == 0 {
		// complete-enough path retired; re-seed
		buf.HandleData(40, 0, []byte("a"))
		_ = buf.HandleFEC(40, 3, 1, 8, 0, make([]byte, 8))
	}
	// Conflicting n/m/w must not replace geometry or allocate recovered data under wrong shape.
	if r := buf.HandleFEC(40, 4, 1, 8, 0, make([]byte, 8)); r != nil {
		t.Fatalf("conflicting n accepted: %v", r)
	}
	if r := buf.HandleFEC(40, 3, 2, 8, 0, make([]byte, 8)); r != nil {
		t.Fatalf("conflicting m accepted: %v", r)
	}
	if r := buf.HandleFEC(40, 3, 1, 16, 0, make([]byte, 16)); r != nil {
		t.Fatalf("conflicting w accepted: %v", r)
	}
}

func TestFECGenBufferRecoveryThenLateOriginal(t *testing.T) {
	const n, m, w = 3, 1, 8
	payloads := [][]byte{[]byte("x"), []byte("y"), []byte("z")}
	parity, err := fec.EncodeParity(payloads, w, m)
	if err != nil {
		t.Fatalf("EncodeParity: %v", err)
	}
	buf := newFECGenBuffer()
	buf.HandleData(50, 0, payloads[0])
	buf.HandleData(50, 2, payloads[2])
	recovered := buf.HandleFEC(50, n, m, w, 0, parity[0])
	if recovered == nil || string(recovered[1]) != "y" {
		t.Fatalf("recovery = %v, want y at index 1", recovered)
	}
	if len(buf.gens) != 0 {
		t.Fatal("generation not retired after recovery")
	}
	// Late original for the recovered index must not re-open unbounded state in a harmful way.
	buf.HandleData(50, 1, payloads[1])
	// A new partial generation may exist; it must not contain conflicting recovered+late mix
	// that survives without GC. Bounded: at most one generation entry for this genID.
	if len(buf.gens) > 1 {
		t.Fatalf("late original created extra generations: %d", len(buf.gens))
	}
}

func TestFECGenBufferHostileGenIDSprayBoundedByUint16Space(t *testing.T) {
	// genID is uint16 — the map cannot exceed 65536 distinct keys. Prove GC reclaims and
	// that filling many IDs does not panic or leave unreclaimable state.
	buf := newFECGenBuffer()
	const spray = 512
	for id := 0; id < spray; id++ {
		buf.HandleData(uint16(id), 0, []byte{byte(id)})
	}
	if len(buf.gens) != spray {
		t.Fatalf("len(gens)=%d want %d", len(buf.gens), spray)
	}
	buf.GC(0)
	if len(buf.gens) != 0 {
		t.Fatalf("after GC len(gens)=%d want 0", len(buf.gens))
	}
}

func TestFECGenBufferMissingShardsNoFalseRecovery(t *testing.T) {
	buf := newFECGenBuffer()
	// Only one of five data shards + one parity — mathematically insufficient.
	buf.HandleData(60, 0, []byte("only"))
	r := buf.HandleFEC(60, 5, 1, 8, 0, make([]byte, 8))
	if r != nil {
		t.Fatalf("false recovery with insufficient shards: %v", r)
	}
	// State may remain until GC; that is expected and bounded by GC maxAge.
	if len(buf.gens) != 1 {
		t.Fatalf("len(gens)=%d want 1 pending GC", len(buf.gens))
	}
	buf.GC(0)
	if len(buf.gens) != 0 {
		t.Fatal("insufficient generation not GC'd")
	}
}

func TestFECGenBufferNegativeAndOverflowIndexesRejected(t *testing.T) {
	buf := newFECGenBuffer()
	buf.HandleData(70, -1, []byte("neg"))
	buf.HandleData(70, fec.K, []byte("k"))
	buf.HandleData(70, fec.K+5, []byte("over"))
	if len(buf.gens) != 0 {
		t.Fatalf("out-of-range genIndex allocated state: %#v", buf.gens)
	}
	if r := buf.HandleFEC(70, 3, 1, 8, -1, make([]byte, 8)); r != nil {
		t.Fatal("negative parity index accepted")
	}
	if r := buf.HandleFEC(70, 3, 1, 8, 1, make([]byte, 8)); r != nil {
		// m=1 so parityIndex 1 is out of range
		t.Fatal("parity index >= m accepted")
	}
	if len(buf.gens) != 0 {
		t.Fatalf("malformed FEC allocated state: %#v", buf.gens)
	}
}
