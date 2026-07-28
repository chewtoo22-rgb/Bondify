#!/usr/bin/env bash
# Two-path netns topology for phase 2's gate: two independent client<->relay links, each
# individually shapeable with tc netem, so a single flow's aggregate throughput across both
# can be measured against the sum of the two paths' individual capacities.
#
# The relay is given ONE fixed address (10.99.0.1, on a dummy interface) reachable via
# EITHER client uplink -- matching a real deployment, where the relay always has exactly
# one public IP regardless of how many uplinks the client bonds. The relay binary must be
# started with `-listen 10.99.0.1:PORT` (not a wildcard bind): a socket bound to a specific
# local address always uses it as the source for outgoing packets regardless of which
# physical interface a reply happens to egress through, which is what lets a *connected*
# client-side UDP socket (as net.DialUDP creates) accept the reply at all -- a wildcard
# relay bind would source each reply from whichever interface's address the kernel's
# destination routing happened to pick, which a connected UDP socket rejects as
# "not from my peer" the moment it doesn't match path 0's address.
#
# The client reaches that one relay address via two different physical routes using Linux
# policy routing (ARCHITECTURE.md §3.2's prescribed approach for exactly this situation):
# one routing table per uplink, selected by source-address `ip rule`, since a single
# destination-based routing table cannot hold two different routes to the same prefix.
#
#   root ns  <--v-rel-out-h/v-rel-out-->  bondify-relay ns  <--v-rel-wan0/v-cli-wan0-->  bondify-client ns
#                                        [10.99.0.1 dummy]  <--v-rel-wan1/v-cli-wan1-->
#
# Usage: bash two_path.sh {up|down|shape0 <netem args>|shape1 <netem args>|unshape}
set -euo pipefail

EGRESS_DEV="${BONDIFY_TEST_EGRESS_DEV:-$(ip route show default | awk '/default/ {print $5; exit}')}"

up() {
	ip netns add bondify-relay
	ip netns add bondify-client

	ip link add v-rel-out-h type veth peer name v-rel-out netns bondify-relay
	ip addr add 10.50.0.1/30 dev v-rel-out-h
	ip link set v-rel-out-h up
	ip netns exec bondify-relay ip addr add 10.50.0.2/30 dev v-rel-out
	ip netns exec bondify-relay ip link set v-rel-out up
	ip netns exec bondify-relay ip link set lo up
	ip netns exec bondify-relay ip route add default via 10.50.0.1

	# Relay's single fixed tunnel-facing address, reachable via either uplink below. A /32
	# alias on lo (not a dummy interface -- this sandbox's kernel has no loadable netdev
	# drivers beyond veth) is the standard way to give a host an address that's always
	# locally reachable regardless of which real interface traffic arrives on.
	ip netns exec bondify-relay ip addr add 10.99.0.1/32 dev lo

	# Path 0
	ip link add v-cli-wan0 netns bondify-client type veth peer name v-rel-wan0 netns bondify-relay
	ip netns exec bondify-client ip addr add 10.60.0.1/30 dev v-cli-wan0
	ip netns exec bondify-client ip link set v-cli-wan0 up
	ip netns exec bondify-client ip link set lo up
	ip netns exec bondify-relay ip addr add 10.60.0.2/30 dev v-rel-wan0
	ip netns exec bondify-relay ip link set v-rel-wan0 up

	# Path 1
	ip link add v-cli-wan1 netns bondify-client type veth peer name v-rel-wan1 netns bondify-relay
	ip netns exec bondify-client ip addr add 10.61.0.1/30 dev v-cli-wan1
	ip netns exec bondify-client ip link set v-cli-wan1 up
	ip netns exec bondify-relay ip addr add 10.61.0.2/30 dev v-rel-wan1
	ip netns exec bondify-relay ip link set v-rel-wan1 up

	# Policy routing: source 10.60.0.1 -> reach 10.99.0.1 via wan0's "gateway" (the
	# relay's wan0-side address); source 10.61.0.1 -> via wan1's.
	ip netns exec bondify-client ip route add 10.99.0.1/32 via 10.60.0.2 dev v-cli-wan0 table 100
	ip netns exec bondify-client ip route add 10.99.0.1/32 via 10.61.0.2 dev v-cli-wan1 table 101
	ip netns exec bondify-client ip rule add from 10.60.0.1 table 100
	ip netns exec bondify-client ip rule add from 10.61.0.1 table 101

	sysctl -qw net.ipv4.ip_forward=1
	iptables -t nat -C POSTROUTING -s 10.50.0.0/30 -o "$EGRESS_DEV" -j MASQUERADE 2>/dev/null || \
		iptables -t nat -A POSTROUTING -s 10.50.0.0/30 -o "$EGRESS_DEV" -j MASQUERADE
	iptables -C FORWARD -i v-rel-out-h -o "$EGRESS_DEV" -j ACCEPT 2>/dev/null || \
		iptables -A FORWARD -i v-rel-out-h -o "$EGRESS_DEV" -j ACCEPT
	iptables -C FORWARD -i "$EGRESS_DEV" -o v-rel-out-h -j ACCEPT 2>/dev/null || \
		iptables -A FORWARD -i "$EGRESS_DEV" -o v-rel-out-h -j ACCEPT
}

down() {
	ip netns del bondify-client 2>/dev/null || true
	ip netns del bondify-relay 2>/dev/null || true
	ip link del v-rel-out-h 2>/dev/null || true
	iptables -t nat -D POSTROUTING -s 10.50.0.0/30 -o "$EGRESS_DEV" -j MASQUERADE 2>/dev/null || true
	iptables -D FORWARD -i v-rel-out-h -o "$EGRESS_DEV" -j ACCEPT 2>/dev/null || true
	iptables -D FORWARD -i "$EGRESS_DEV" -o v-rel-out-h -j ACCEPT 2>/dev/null || true
}

shape() {
	local dev="$1"
	shift
	if ! ip netns exec bondify-client tc qdisc add dev "$dev" root netem "$@" 2>/tmp/netem-check.err; then
		echo "testbed: WARNING: tc netem unavailable in this kernel ($(cat /tmp/netem-check.err)); running unshaped" >&2
		return 1
	fi
	return 0
}

unshape() {
	ip netns exec bondify-client tc qdisc del dev v-cli-wan0 root 2>/dev/null || true
	ip netns exec bondify-client tc qdisc del dev v-cli-wan1 root 2>/dev/null || true
}

case "${1:-}" in
up) up ;;
down) down ;;
shape0) shift; shape v-cli-wan0 "$@" ;;
shape1) shift; shape v-cli-wan1 "$@" ;;
unshape) unshape ;;
*) echo "usage: $0 {up|down|shape0 <netem args>|shape1 <netem args>|unshape}" >&2; exit 2 ;;
esac
