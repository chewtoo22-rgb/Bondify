package bond

import (
	"bytes"
	"testing"
	"time"

	"github.com/chewtoo22-rgb/bondify/core/fec"
	"github.com/chewtoo22-rgb/bondify/core/proto"
)

type fecCall struct {
	genID, genIndex, n, m, w int
	shard                    []byte
}

func recordPacket(t *testing.T, sender *fecSender, gsn uint64, payload []byte) {
	t.Helper()
	genID, genIndex := sender.NextSlot()
	h := proto.InnerDataHeader{
		GSN:          gsn,
		PSN:          uint32(gsn),
		PathID:       0,
		Flags:        proto.FlagFECProtected,
		PayloadLen:   uint16(len(payload)),
		GenerationID: genID,
		GenIndex:     uint8(genIndex),
	}
	buf := make([]byte, proto.InnerHeaderLen+len(payload))
	if err := proto.MarshalInner(buf, h); err != nil {
		t.Fatalf("MarshalInner: %v", err)
	}
	copy(buf[proto.InnerHeaderLen:], payload)
	sender.Record(genID, genIndex, buf)
}

func TestFECSenderClosesAtK(t *testing.T) {
	var calls []fecCall
	s := newFECSender(
		func() float64 { return 0.10 }, // saturated -> m=3 for n=10
		func(genID uint16, genIndex, n, m, w int, shard []byte) {
			calls = append(calls, fecCall{int(genID), genIndex, n, m, w, shard})
		},
	)

	for i := 0; i < fec.K; i++ {
		recordPacket(t, s, uint64(i), []byte{byte(i), byte(i + 1)})
	}

	if len(calls) != 3 {
		t.Fatalf("len(calls) = %d, want 3 parity packets for n=10 at 10%% loss", len(calls))
	}
	for i, c := range calls {
		if c.genID != 0 {
			t.Errorf("call %d: genID = %d, want 0", i, c.genID)
		}
		if c.n != fec.K {
			t.Errorf("call %d: n = %d, want %d", i, c.n, fec.K)
		}
		if c.m != 3 {
			t.Errorf("call %d: m = %d, want 3", i, c.m)
		}
		if c.genIndex != fec.K+i {
			t.Errorf("call %d: genIndex = %d, want %d", i, c.genIndex, fec.K+i)
		}
	}

	// A new generation should have started.
	genID, genIndex := s.NextSlot()
	if genID != 1 || genIndex != 0 {
		t.Errorf("next slot after close = (%d,%d), want (1,0)", genID, genIndex)
	}
}

func TestFECSenderFlushOnTimeout(t *testing.T) {
	var calls []fecCall
	s := newFECSender(
		func() float64 { return 0.10 },
		func(genID uint16, genIndex, n, m, w int, shard []byte) {
			calls = append(calls, fecCall{int(genID), genIndex, n, m, w, shard})
		},
	)

	for i := 0; i < 4; i++ { // well under fec.K
		recordPacket(t, s, uint64(i), []byte{byte(i)})
	}
	if len(calls) != 0 {
		t.Fatalf("expected no parity before close, got %d calls", len(calls))
	}

	s.Flush(0) // age=0 always closes a non-empty generation
	if len(calls) == 0 {
		t.Fatal("expected Flush to close the partial generation and emit parity")
	}
	for _, c := range calls {
		if c.n != 4 {
			t.Errorf("n = %d, want 4 (partial generation)", c.n)
		}
	}
}

func TestFECSenderFlushRespectsAge(t *testing.T) {
	var calls int
	s := newFECSender(func() float64 { return 0.10 }, func(uint16, int, int, int, int, []byte) { calls++ })
	recordPacket(t, s, 0, []byte{1})
	s.Flush(time.Hour) // far larger than elapsed -- must not close yet
	if calls != 0 {
		t.Fatalf("Flush with a large age closed early, calls = %d", calls)
	}
}

func TestFECSenderZeroLossNoParity(t *testing.T) {
	var calls int
	s := newFECSender(func() float64 { return 0 }, func(uint16, int, int, int, int, []byte) { calls++ })
	for i := 0; i < fec.K; i++ {
		recordPacket(t, s, uint64(i), []byte{byte(i)})
	}
	if calls != 0 {
		t.Fatalf("zero loss should need zero parity, got %d calls", calls)
	}
}

func TestFECGenBufferReconstructsMissingShard(t *testing.T) {
	// Build a real generation's worth of packets through fecSender, capturing both the
	// data shards' inner plaintexts (as a real client.go/relay.go caller would have on
	// hand) and the parity shards fecSender emits.
	type dataShard struct {
		genIndex int
		gsn      uint64
		payload  []byte
		plain    []byte
	}
	var data []dataShard
	var parity []fecCall

	s := newFECSender(
		func() float64 { return 0.10 }, // m=3
		func(genID uint16, genIndex, n, m, w int, shard []byte) {
			parity = append(parity, fecCall{int(genID), genIndex, n, m, w, shard})
		},
	)

	for i := 0; i < fec.K; i++ {
		payload := bytes.Repeat([]byte{byte(i + 1)}, 30+i*7) // varying lengths
		genID, genIndex := s.NextSlot()
		h := proto.InnerDataHeader{
			GSN: uint64(1000 + i), PSN: uint32(i), Flags: proto.FlagFECProtected,
			PayloadLen: uint16(len(payload)), GenerationID: genID, GenIndex: uint8(genIndex),
		}
		buf := make([]byte, proto.InnerHeaderLen+len(payload))
		if err := proto.MarshalInner(buf, h); err != nil {
			t.Fatalf("MarshalInner: %v", err)
		}
		copy(buf[proto.InnerHeaderLen:], payload)
		s.Record(genID, genIndex, buf)
		data = append(data, dataShard{genIndex, h.GSN, payload, buf})
	}
	if len(parity) != 3 {
		t.Fatalf("len(parity) = %d, want 3", len(parity))
	}

	// Receiver: deliver every data shard except index 5, plus all parity shards.
	buf := newFECGenBuffer()
	const dropped = 5
	for _, d := range data {
		if d.genIndex == dropped {
			continue
		}
		buf.HandleData(0, d.genIndex, d.plain)
	}

	var recovered map[int][]byte
	for _, p := range parity {
		r := buf.HandleFEC(uint16(p.genID), p.n, p.m, p.w, p.genIndex-p.n, p.shard)
		if r != nil {
			recovered = r
		}
	}

	got, ok := recovered[dropped]
	if !ok {
		t.Fatalf("expected recovered[%d] to be present, recovered = %v", dropped, recovered)
	}
	if !bytes.Equal(got, data[dropped].plain) {
		t.Fatalf("recovered plaintext mismatch:\n got  %x\n want %x", got, data[dropped].plain)
	}

	h, payload, ok := unmarshalRecovered(got)
	if !ok {
		t.Fatal("unmarshalRecovered failed on a successfully reconstructed shard")
	}
	if h.GSN != data[dropped].gsn {
		t.Errorf("recovered GSN = %d, want %d", h.GSN, data[dropped].gsn)
	}
	if !bytes.Equal(payload, data[dropped].payload) {
		t.Errorf("recovered payload mismatch:\n got  %x\n want %x", payload, data[dropped].payload)
	}
}

func TestFECGenBufferNoReconstructionWhenComplete(t *testing.T) {
	buf := newFECGenBuffer()
	buf.HandleData(0, 0, []byte("a"))
	buf.HandleData(0, 1, []byte("b"))
	recovered := buf.HandleFEC(0, 2, 1, 8, 0, make([]byte, 8))
	if recovered != nil {
		t.Fatalf("expected nil (nothing missing), got %v", recovered)
	}
}

func TestFECGenBufferTooFewShards(t *testing.T) {
	buf := newFECGenBuffer()
	buf.HandleData(0, 0, []byte("a")) // only 1 of 5 data shards, 1 parity -- unrecoverable
	recovered := buf.HandleFEC(0, 5, 1, 8, 0, make([]byte, 8))
	if recovered != nil {
		t.Fatalf("expected nil (too few shards to reconstruct), got %v", recovered)
	}
}

func TestFECGenBufferGC(t *testing.T) {
	buf := newFECGenBuffer()
	buf.HandleData(0, 0, []byte("a"))
	if len(buf.gens) != 1 {
		t.Fatalf("len(gens) = %d, want 1 before GC", len(buf.gens))
	}
	buf.GC(0) // maxAge=0 evicts anything already created
	if len(buf.gens) != 0 {
		t.Fatalf("len(gens) = %d, want 0 after GC(0)", len(buf.gens))
	}
}

func TestUnmarshalRecoveredRejectsGarbage(t *testing.T) {
	if _, _, ok := unmarshalRecovered([]byte{1, 2, 3}); ok {
		t.Fatal("expected unmarshalRecovered to reject a too-short buffer")
	}
}
