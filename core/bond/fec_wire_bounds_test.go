package bond

import (
	"testing"

	"github.com/chewtoo22-rgb/bondify/core/fec"
)

func TestFECGenBufferRejectsImpossibleSenderGeometry(t *testing.T) {
	cases := []struct {
		name               string
		n, m, w, parityIdx int
		shardLen           int
	}{
		{
			name: "too many data shards",
			n:    fec.K + 1, m: 1, w: 8, parityIdx: 0, shardLen: 8,
		},
		{
			name: "too much parity",
			n:    fec.K, m: fec.RedundancyFor(1, fec.K) + 1, w: 8, parityIdx: 0, shardLen: 8,
		},
		{
			name: "parity width mismatch",
			n:    fec.K, m: 1, w: 64, parityIdx: 0, shardLen: 8,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := newFECGenBuffer()
			got := buf.HandleFEC(7, tc.n, tc.m, tc.w, tc.parityIdx, make([]byte, tc.shardLen))
			if got != nil {
				t.Fatalf("HandleFEC returned recovered data for impossible geometry: %v", got)
			}
			if len(buf.gens) != 0 {
				t.Fatalf("impossible FEC geometry allocated receive state: %#v", buf.gens)
			}
		})
	}
}

func TestFECGenBufferRejectsDataIndexOutsideSenderWindow(t *testing.T) {
	buf := newFECGenBuffer()
	buf.HandleData(9, fec.K, []byte("authenticated but impossible"))
	if len(buf.gens) != 0 {
		t.Fatalf("out-of-window DATA genIndex allocated receive state: %#v", buf.gens)
	}
}

func TestFECGenBufferDropsCompleteGenerationImmediately(t *testing.T) {
	const (
		n = 3
		m = 1
		w = 8
	)
	buf := newFECGenBuffer()
	buf.HandleData(11, 0, []byte("a"))
	buf.HandleData(11, 1, []byte("b"))
	buf.HandleData(11, 2, []byte("c"))

	if len(buf.gens) != 1 {
		t.Fatalf("len(gens) before geometry = %d, want 1", len(buf.gens))
	}
	if got := buf.HandleFEC(11, n, m, w, 0, make([]byte, w)); got != nil {
		t.Fatalf("complete generation unexpectedly reconstructed data: %v", got)
	}
	if len(buf.gens) != 0 {
		t.Fatalf("complete generation retained until GC: %#v", buf.gens)
	}
}

func TestFECGenBufferDropsGenerationAfterReconstruction(t *testing.T) {
	const (
		n = 3
		m = 1
		w = 8
	)
	payloads := [][]byte{[]byte("a"), []byte("b"), []byte("c")}
	parity, err := fec.EncodeParity(payloads, w, m)
	if err != nil {
		t.Fatalf("EncodeParity: %v", err)
	}

	buf := newFECGenBuffer()
	buf.HandleData(12, 0, payloads[0])
	buf.HandleData(12, 2, payloads[2])
	recovered := buf.HandleFEC(12, n, m, w, 0, parity[0])
	if recovered == nil || string(recovered[1]) != "b" {
		t.Fatalf("reconstruction = %v, want missing shard 1", recovered)
	}
	if len(buf.gens) != 0 {
		t.Fatalf("reconstructed generation retained until GC: %#v", buf.gens)
	}
}
