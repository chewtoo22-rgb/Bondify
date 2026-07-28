package fec

import (
	"bytes"
	"math/rand"
	"testing"
)

func TestRedundancyFor(t *testing.T) {
	cases := []struct {
		name string
		loss float64
		n    int
		want int
	}{
		{"zero loss", 0, 10, 0},
		{"negative loss clamped", -0.5, 10, 0},
		{"2pct loss", 0.02, 10, 1},           // 0.02*5.0=0.10 -> ceil(1.0)=1
		{"5pct loss saturates", 0.05, 10, 3}, // 0.05*5.0=0.25=max -> ceil(2.5)=3
		{"10pct loss still capped", 0.10, 10, 3},
		{"50pct loss still capped", 0.50, 10, 3},
		{"n=0", 0.5, 0, 0},
		{"n=5 saturated", 0.5, 5, 2}, // ceil(0.25*5)=2
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := RedundancyFor(c.loss, c.n)
			if got != c.want {
				t.Errorf("RedundancyFor(%v, %d) = %d, want %d", c.loss, c.n, got, c.want)
			}
			if got > c.n {
				t.Errorf("RedundancyFor(%v, %d) = %d exceeds n=%d", c.loss, c.n, got, c.n)
			}
		})
	}
}

func makeGeneration(n int, r *rand.Rand) [][]byte {
	shards := make([][]byte, n)
	for i := range shards {
		size := 40 + r.Intn(200) // varying lengths, exercising the self-describing prefix
		buf := make([]byte, size)
		_, _ = r.Read(buf)
		shards[i] = buf
	}
	return shards
}

func maxLen(shards [][]byte) int {
	m := 0
	for _, s := range shards {
		if len(s) > m {
			m = len(s)
		}
	}
	return m
}

func TestEncodeReconstructRoundTrip(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	data := makeGeneration(8, r)
	w := ShardWidth(maxLen(data))
	m := 3

	parity, err := EncodeParity(data, w, m)
	if err != nil {
		t.Fatalf("EncodeParity: %v", err)
	}
	if len(parity) != m {
		t.Fatalf("len(parity) = %d, want %d", len(parity), m)
	}
	for _, p := range parity {
		if len(p) != w {
			t.Fatalf("parity shard len = %d, want %d", len(p), w)
		}
	}

	n := len(data)
	shards := make([][]byte, n+m)
	present := make([]bool, n+m)
	copy(shards[:n], data)
	copy(shards[n:], parity)
	for i := range shards {
		present[i] = true
	}

	// Drop exactly m shards (a mix of data and parity) -- still reconstructible.
	dropped := map[int]bool{1: true, 4: true, n: true} // 2 data + 1 parity
	for idx := range dropped {
		present[idx] = false
	}

	recovered, err := Reconstruct(shards, present, n, m, w)
	if err != nil {
		t.Fatalf("Reconstruct: %v", err)
	}
	for idx := range dropped {
		if idx >= n {
			continue // parity indices are never in the recovered map
		}
		got, ok := recovered[idx]
		if !ok {
			t.Fatalf("expected recovered[%d] to be present", idx)
		}
		if !bytes.Equal(got, data[idx]) {
			t.Errorf("recovered[%d] = %x (len %d), want %x (len %d)", idx, got, len(got), data[idx], len(data[idx]))
		}
	}
}

func TestReconstructTooFewShards(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	data := makeGeneration(6, r)
	w := ShardWidth(maxLen(data))
	m := 2

	parity, err := EncodeParity(data, w, m)
	if err != nil {
		t.Fatalf("EncodeParity: %v", err)
	}

	n := len(data)
	shards := make([][]byte, n+m)
	present := make([]bool, n+m)
	copy(shards[:n], data)
	copy(shards[n:], parity)
	for i := range shards {
		present[i] = true
	}
	// Drop 3 shards when only 2 parity exist -- mathematically unrecoverable.
	present[0] = false
	present[1] = false
	present[2] = false

	_, err = Reconstruct(shards, present, n, m, w)
	if err != ErrTooFewShards {
		t.Fatalf("Reconstruct error = %v, want ErrTooFewShards", err)
	}
}

func TestReconstructNoLossNeeded(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	data := makeGeneration(4, r)
	w := ShardWidth(maxLen(data))
	m := 2

	parity, err := EncodeParity(data, w, m)
	if err != nil {
		t.Fatalf("EncodeParity: %v", err)
	}

	n := len(data)
	shards := make([][]byte, n+m)
	present := make([]bool, n+m)
	copy(shards[:n], data)
	copy(shards[n:], parity)
	for i := range shards {
		present[i] = true
	}

	recovered, err := Reconstruct(shards, present, n, m, w)
	if err != nil {
		t.Fatalf("Reconstruct: %v", err)
	}
	if len(recovered) != 0 {
		t.Errorf("recovered = %v, want empty (nothing missing)", recovered)
	}
}

func TestEncodeParityZeroRedundancy(t *testing.T) {
	r := rand.New(rand.NewSource(4))
	data := makeGeneration(5, r)
	w := ShardWidth(maxLen(data))

	parity, err := EncodeParity(data, w, 0)
	if err != nil {
		t.Fatalf("EncodeParity(m=0): %v", err)
	}
	if parity != nil {
		t.Errorf("parity = %v, want nil for m=0", parity)
	}
}

func TestEncodeParityEmptyGeneration(t *testing.T) {
	_, err := EncodeParity(nil, 100, 2)
	if err != ErrEmptyGeneration {
		t.Fatalf("error = %v, want ErrEmptyGeneration", err)
	}
}

func TestEncodeParityPayloadExceedsWidth(t *testing.T) {
	data := [][]byte{make([]byte, 100)}
	_, err := EncodeParity(data, 50, 1) // w too small for a 100-byte payload
	if err == nil {
		t.Fatal("expected an error for payload exceeding shard width")
	}
}

func TestReconstructAllShardsMissingBeyondParity(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	data := makeGeneration(10, r)
	w := ShardWidth(maxLen(data))
	m := RedundancyFor(0.10, len(data)) // saturated: m=3

	parity, err := EncodeParity(data, w, m)
	if err != nil {
		t.Fatalf("EncodeParity: %v", err)
	}

	n := len(data)
	shards := make([][]byte, n+m)
	present := make([]bool, n+m)
	copy(shards[:n], data)
	copy(shards[n:], parity)
	for i := range shards {
		present[i] = true
	}
	// Drop exactly m data shards -- right at the recoverable boundary.
	for i := 0; i < m; i++ {
		present[i] = false
	}

	recovered, err := Reconstruct(shards, present, n, m, w)
	if err != nil {
		t.Fatalf("Reconstruct at exact boundary: %v", err)
	}
	for i := 0; i < m; i++ {
		if !bytes.Equal(recovered[i], data[i]) {
			t.Errorf("recovered[%d] mismatch", i)
		}
	}
}
