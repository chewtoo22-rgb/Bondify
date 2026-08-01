// Package classify implements ARCHITECTURE.md §2.1 Tier 5's traffic classification: a
// best-effort, per-packet coarse classification of the raw IP packets Bondify tunnels, used
// to route by class ("Traffic-class routing (Latency Smoothing)" -- LATENCY never splits,
// REALTIME duplicates on the best paths, BULK spreads with a headroom cap). This package
// only classifies; core/bond decides what each Class means for routing.
//
// Classification works from two signals, in priority order:
//
//  1. DSCP (the IP header's Differentiated Services Code Point, RFC 4594's per-hop-behavior
//     marking): the OS or application already telling the network how to treat this packet.
//     Respected when present because it's the most reliable signal available -- no guessing.
//  2. Well-known ports, as a fallback when nothing set a DSCP marking (the overwhelmingly
//     common case for ordinary client traffic): SSH lands as INTERACTIVE because it's the
//     canonical example ARCHITECTURE.md's own Phase 7 gate tests ("SSH RTT stays within 25%
//     of unloaded" under a concurrent bulk download), DNS as LATENCY (a slow DNS lookup
//     stalls everything waiting on it despite being tiny), and everything else defaults to
//     BULK -- the safe default, since misclassifying bulk traffic as latency-sensitive
//     defeats the whole point of the headroom cap, while misclassifying latency-sensitive
//     traffic as bulk merely loses the benefit for that one flow rather than degrading
//     everything else sharing the path.
//
// What this deliberately does not do: deep packet inspection (TLS SNI, QUIC-is-UDP-443,
// per-application awareness), IPv6 extension header walking (a v6 packet with any extension
// header before its transport header falls back to the DSCP-only/default path), or anything
// requiring state across packets. A single malformed, truncated, or unrecognized packet
// never panics -- Classify returns Unknown, which core/bond treats identically to Bulk.
package classify

// Class is a coarse traffic classification (ARCHITECTURE.md §2.1 Tier 5).
type Class int

const (
	// Unknown means Classify couldn't determine a class (too short to parse, or a protocol
	// version/number it doesn't recognize). Routed identically to Bulk.
	Unknown Class = iota
	// Latency is small, delay-sensitive, low-rate traffic where waiting even briefly for a
	// reply stalls something else (DNS lookups, ICMP). Never split across paths -- pinned
	// to the single lowest-RTT path so reordering across paths never adds latency back.
	Latency
	// Realtime is continuous low-latency, loss-intolerant media (DSCP EF/CS5+ -- VoIP,
	// video calls, real-time control). Duplicated onto the best paths for near-zero
	// effective loss, the same tradeoff REDUNDANT mode makes tunnel-wide but applied only
	// to traffic that actually needs it.
	Realtime
	// Interactive is latency-sensitive but not continuous: a human waiting on a round trip
	// (SSH, DSCP AF4x/CS4). Routed through the ordinary scheduler tier, uncapped -- it
	// needs low latency, not raw throughput, and the scheduler tiers already optimize for
	// that.
	Interactive
	// Bulk is everything else: downloads, backups, anything throughput-oriented rather than
	// latency-sensitive. Routed through the ordinary scheduler tier with a headroom cap
	// (core/bond's bulkHeadroomFraction) so it can never fully saturate a path's congestion
	// window and starve LATENCY/REALTIME traffic sharing that same path.
	Bulk
)

func (c Class) String() string {
	switch c {
	case Latency:
		return "LATENCY"
	case Realtime:
		return "REALTIME"
	case Interactive:
		return "INTERACTIVE"
	case Bulk:
		return "BULK"
	default:
		return "UNKNOWN"
	}
}

// DSCP per-hop-behavior values this package recognizes (RFC 4594), as the 6-bit DSCP value
// (not shifted into the IPv4 TOS/IPv6 traffic-class byte's top 6 bits -- extractDSCP already
// un-shifts before comparing against these).
const (
	dscpCS1  = 0x08 // 8: Low-Priority Data
	dscpAF11 = 0x0A // 10
	dscpAF12 = 0x0C // 12
	dscpAF13 = 0x0E // 14
	dscpCS4  = 0x20 // 32: Real-Time Interactive
	dscpAF41 = 0x22 // 34: Multimedia Conferencing
	dscpAF42 = 0x24 // 36
	dscpAF43 = 0x26 // 38
	dscpEF   = 0x2E // 46: Expedited Forwarding (VoIP)
	dscpCS5  = 0x28 // 40: Broadcast Video
	dscpCS6  = 0x30 // 48: Network Control
	dscpCS7  = 0x38 // 56: Network Control
)

func classFromDSCP(dscp byte) (Class, bool) {
	switch dscp {
	case dscpEF, dscpCS5, dscpCS6, dscpCS7:
		return Realtime, true
	case dscpCS4, dscpAF41, dscpAF42, dscpAF43:
		return Interactive, true
	case dscpCS1, dscpAF11, dscpAF12, dscpAF13:
		return Bulk, true
	default:
		return Unknown, false
	}
}

const (
	protoICMP   = 1
	protoTCP    = 6
	protoUDP    = 17
	protoICMPv6 = 58
)

// Classify inspects packet -- a raw IP packet exactly as read from the TUN device, IPv4 or
// IPv6, no L2 framing -- and returns its traffic class. See the package doc comment for the
// classification method and its limitations. Never panics; a packet too short or malformed
// to parse safely returns Unknown.
func Classify(packet []byte) Class {
	if len(packet) < 1 {
		return Unknown
	}
	version := packet[0] >> 4
	switch version {
	case 4:
		return classifyIPv4(packet)
	case 6:
		return classifyIPv6(packet)
	default:
		return Unknown
	}
}

func classifyIPv4(packet []byte) Class {
	if len(packet) < 20 {
		return Unknown
	}
	ihl := int(packet[0]&0x0F) * 4
	if ihl < 20 || len(packet) < ihl {
		return Unknown
	}
	dscp := packet[1] >> 2
	if class, ok := classFromDSCP(dscp); ok {
		return class
	}
	proto := packet[9]
	return classifyByProtoAndPorts(proto, packet[ihl:])
}

func classifyIPv6(packet []byte) Class {
	const v6HeaderLen = 40
	if len(packet) < v6HeaderLen {
		return Unknown
	}
	dscp := (packet[0]&0x0F)<<2 | packet[1]>>6
	if class, ok := classFromDSCP(dscp); ok {
		return class
	}
	// Extension headers before the transport header are not walked (see package doc); only
	// a directly-following TCP/UDP/ICMPv6 header is classified by port.
	nextHeader := packet[6]
	return classifyByProtoAndPorts(nextHeader, packet[v6HeaderLen:])
}

// wellKnownPort classes a transport-layer port Bondify has a confident, generic answer for.
// Deliberately small: guessing wrong actively hurts (see package doc), so this only lists
// ports whose traffic is overwhelmingly one class in practice.
func wellKnownPort(port uint16) (Class, bool) {
	switch port {
	case 53: // DNS
		return Latency, true
	case 22, 23: // SSH, Telnet -- interactive remote shells
		return Interactive, true
	default:
		return Unknown, false
	}
}

func classifyByProtoAndPorts(proto byte, transport []byte) Class {
	switch proto {
	case protoICMP, protoICMPv6:
		// Ping and other control traffic: small, latency-sensitive, never bulk.
		return Latency
	case protoTCP, protoUDP:
		if len(transport) < 4 {
			return Bulk
		}
		srcPort := be16(transport[0:2])
		dstPort := be16(transport[2:4])
		if class, ok := wellKnownPort(dstPort); ok {
			return class
		}
		if class, ok := wellKnownPort(srcPort); ok {
			return class
		}
		return Bulk
	default:
		return Bulk
	}
}

func be16(b []byte) uint16 { return uint16(b[0])<<8 | uint16(b[1]) }
