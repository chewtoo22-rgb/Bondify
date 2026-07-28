package bond

import (
	"testing"
	"time"

	"github.com/chewtoo22-rgb/bondify/core/reorder"
	"github.com/chewtoo22-rgb/bondify/core/sched"
)

func TestClientTunnelDiagnostics(t *testing.T) {
	p0 := NewPath(0, nil)
	p0.SetActive()
	p0.Stats.TxPackets = 100
	p0.Stats.TxBytes = 150_000
	p0.Stats.RxPackets = 90
	p0.Stats.RxBytes = 130_000
	p0.HandleProbeAck(ProbeAckPayload{SentAtUnixNano: time.Now().Add(-5 * time.Millisecond).UnixNano(), SentPSN: 10, RecvPSN: 10}, time.Now())

	t1 := &ClientTunnel{
		sessionIndex: 0x7111f81a,
		tunnelIP:     "10.77.0.2",
		startedAt:    time.Now().Add(-90 * time.Second),
		sched:        sched.NewRoundRobin(),
		reorderBuf:   reorder.New(reorder.DefaultDeadlineMin, 0),
		paths:        []*Path{p0},
	}
	t1.Stats.TxPackets = 100
	t1.Stats.TxBytes = 150_000
	t1.Stats.RxPackets = 90
	t1.Stats.RxBytes = 130_000

	d := t1.Diagnostics()

	if d.SessionIndex != "7111f81a" {
		t.Errorf("SessionIndex = %q, want %q", d.SessionIndex, "7111f81a")
	}
	if d.TunnelIP != "10.77.0.2" {
		t.Errorf("TunnelIP = %q, want 10.77.0.2", d.TunnelIP)
	}
	if d.Scheduler != "round-robin" {
		t.Errorf("Scheduler = %q, want round-robin", d.Scheduler)
	}
	if len(d.Paths) != 1 {
		t.Fatalf("len(Paths) = %d, want 1", len(d.Paths))
	}
	pd := d.Paths[0]
	if pd.ID != 0 || pd.State != "ACTIVE" || pd.Role != "BOND" {
		t.Errorf("path = %+v, want id=0 state=ACTIVE role=BOND", pd)
	}
	if pd.TxPackets != 100 || pd.TxBytes != 150_000 {
		t.Errorf("path tx = %d/%d, want 100/150000", pd.TxPackets, pd.TxBytes)
	}
	if pd.RTTMinMS <= 0 {
		t.Errorf("RTTMinMS = %v, want > 0 after a recorded probe ack", pd.RTTMinMS)
	}

	if d.Aggregate.TxPackets != 100 || d.Aggregate.RxBytes != 130_000 {
		t.Errorf("aggregate = %+v, want tx_packets=100 rx_bytes=130000", d.Aggregate)
	}
	if d.Aggregate.ReorderMaxBytes != reorder.DefaultBufMin {
		t.Errorf("ReorderMaxBytes = %d, want default min %d", d.Aggregate.ReorderMaxBytes, reorder.DefaultBufMin)
	}
	if d.Aggregate.UptimeSec < 89 || d.Aggregate.UptimeSec > 91 {
		t.Errorf("UptimeSec = %v, want ~90", d.Aggregate.UptimeSec)
	}
}

func TestSessionIndexHex(t *testing.T) {
	cases := map[uint32]string{
		0:          "00000000",
		0x7111f81a: "7111f81a",
		0xffffffff: "ffffffff",
	}
	for in, want := range cases {
		if got := sessionIndexHex(in); got != want {
			t.Errorf("sessionIndexHex(%#x) = %q, want %q", in, got, want)
		}
	}
}
