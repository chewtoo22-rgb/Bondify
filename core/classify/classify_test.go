package classify

import "testing"

// buildIPv4 constructs a minimal, real (checksum-less -- Classify never validates checksums)
// IPv4 packet with a 20-byte header, given DSCP and protocol, followed by payload (for
// TCP/UDP, payload's first 4 bytes should be src/dst port).
func buildIPv4(dscp, proto byte, payload []byte) []byte {
	pkt := make([]byte, 20+len(payload))
	pkt[0] = 0x45 // version 4, IHL 5 (20 bytes)
	pkt[1] = dscp << 2
	pkt[9] = proto
	copy(pkt[20:], payload)
	return pkt
}

// buildIPv6 packs dscp (a 6-bit DSCP value) into the version/traffic-class bytes the way a
// real IPv6 header does: traffic_class = dscp<<2 | ecn(0), split across the low nibble of
// byte 0 and the high nibble of byte 1. Kept as the exact inverse of classifyIPv6's
// extraction so a test failure here means Classify's decode is wrong, not this encoding.
func buildIPv6(dscp, nextHeader byte, payload []byte) []byte {
	pkt := make([]byte, 40+len(payload))
	pkt[0] = 0x60 | (dscp >> 2)
	pkt[1] = (dscp & 0x03) << 6
	pkt[6] = nextHeader
	copy(pkt[40:], payload)
	return pkt
}

func udpPayload(srcPort, dstPort uint16) []byte {
	b := make([]byte, 8)
	b[0], b[1] = byte(srcPort>>8), byte(srcPort)
	b[2], b[3] = byte(dstPort>>8), byte(dstPort)
	return b
}

func tcpPayload(srcPort, dstPort uint16) []byte {
	b := make([]byte, 20)
	b[0], b[1] = byte(srcPort>>8), byte(srcPort)
	b[2], b[3] = byte(dstPort>>8), byte(dstPort)
	return b
}

func TestClassifyDSCPTakesPriorityOverPort(t *testing.T) {
	// Port 80 (HTTP) would otherwise fall to BULK, but an EF marking must win regardless.
	pkt := buildIPv4(dscpEF, protoUDP, udpPayload(5000, 80))
	if got := Classify(pkt); got != Realtime {
		t.Fatalf("Classify = %s, want REALTIME (DSCP EF must override port heuristic)", got)
	}
}

func TestClassifyDSCPValues(t *testing.T) {
	cases := []struct {
		name string
		dscp byte
		want Class
	}{
		{"EF (VoIP)", dscpEF, Realtime},
		{"CS5", dscpCS5, Realtime},
		{"CS6", dscpCS6, Realtime},
		{"CS7", dscpCS7, Realtime},
		{"CS4 (real-time interactive)", dscpCS4, Interactive},
		{"AF41", dscpAF41, Interactive},
		{"AF42", dscpAF42, Interactive},
		{"AF43", dscpAF43, Interactive},
		{"CS1 (low-priority)", dscpCS1, Bulk},
		{"AF11", dscpAF11, Bulk},
		{"AF12", dscpAF12, Bulk},
		{"AF13", dscpAF13, Bulk},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pkt := buildIPv4(tc.dscp, protoUDP, udpPayload(4000, 4001))
			if got := Classify(pkt); got != tc.want {
				t.Fatalf("Classify(dscp=%#x) = %s, want %s", tc.dscp, got, tc.want)
			}
		})
	}
}

func TestClassifySSHIsInteractive(t *testing.T) {
	pkt := buildIPv4(0, protoTCP, tcpPayload(51000, 22))
	if got := Classify(pkt); got != Interactive {
		t.Fatalf("Classify(TCP dst=22) = %s, want INTERACTIVE", got)
	}
	// Also from the server's reply direction (src=22).
	reply := buildIPv4(0, protoTCP, tcpPayload(22, 51000))
	if got := Classify(reply); got != Interactive {
		t.Fatalf("Classify(TCP src=22) = %s, want INTERACTIVE", got)
	}
}

func TestClassifyDNSIsLatency(t *testing.T) {
	pkt := buildIPv4(0, protoUDP, udpPayload(51000, 53))
	if got := Classify(pkt); got != Latency {
		t.Fatalf("Classify(UDP dst=53) = %s, want LATENCY", got)
	}
}

func TestClassifyICMPIsLatency(t *testing.T) {
	pkt := buildIPv4(0, protoICMP, []byte{8, 0, 0, 0})
	if got := Classify(pkt); got != Latency {
		t.Fatalf("Classify(ICMP) = %s, want LATENCY", got)
	}
}

func TestClassifyHTTPSDefaultsToBulk(t *testing.T) {
	pkt := buildIPv4(0, protoTCP, tcpPayload(51000, 443))
	if got := Classify(pkt); got != Bulk {
		t.Fatalf("Classify(TCP dst=443) = %s, want BULK (safe default)", got)
	}
}

func TestClassifyIPv6DSCPAndPorts(t *testing.T) {
	realtime := buildIPv6(dscpEF, protoUDP, udpPayload(5000, 5001))
	if got := Classify(realtime); got != Realtime {
		t.Fatalf("Classify(v6, dscp=EF) = %s, want REALTIME", got)
	}
	ssh := buildIPv6(0, protoTCP, tcpPayload(51000, 22))
	if got := Classify(ssh); got != Interactive {
		t.Fatalf("Classify(v6, TCP dst=22) = %s, want INTERACTIVE", got)
	}
	bulk := buildIPv6(0, protoTCP, tcpPayload(51000, 443))
	if got := Classify(bulk); got != Bulk {
		t.Fatalf("Classify(v6, TCP dst=443) = %s, want BULK", got)
	}
}

func TestClassifyNeverPanicsOnGarbage(t *testing.T) {
	cases := [][]byte{
		nil,
		{},
		{0x45},
		{0x45, 0x00},
		make([]byte, 19), // one byte short of a valid IPv4 header
		{0x45, 0x00, 0, 0, 0, 0, 0, 0, 0, protoTCP, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, // no transport bytes
		{0x00},           // version 0, unrecognized
		{0xF0},           // version 15, unrecognized
		make([]byte, 39), // one byte short of a valid IPv6 header
	}
	for i, pkt := range cases {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("case %d: Classify panicked: %v", i, r)
				}
			}()
			_ = Classify(pkt)
		}()
	}
}

func TestClassifyUnclassifiableIPv4ProtocolDefaultsToBulk(t *testing.T) {
	pkt := buildIPv4(0, 253, nil) // 253/254 are reserved for experimentation (RFC 3692)
	if got := Classify(pkt); got != Bulk {
		t.Fatalf("Classify(unknown proto) = %s, want BULK", got)
	}
}

func TestClassStringNames(t *testing.T) {
	cases := map[Class]string{
		Unknown:     "UNKNOWN",
		Latency:     "LATENCY",
		Realtime:    "REALTIME",
		Interactive: "INTERACTIVE",
		Bulk:        "BULK",
		Class(99):   "UNKNOWN",
	}
	for c, want := range cases {
		if got := c.String(); got != want {
			t.Errorf("Class(%d).String() = %q, want %q", c, got, want)
		}
	}
}
