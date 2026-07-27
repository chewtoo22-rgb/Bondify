#!/usr/bin/env bash
# Builds the phase-1/phase-2-baseline netns test topology: a client namespace and a relay
# namespace connected by one veth pair (the "path" under test), with the relay namespace
# additionally bridged to the root namespace's real network so it can reach a genuine
# internet target (this is what makes the HTTP-through-tunnel gate test real traffic, not
# a loopback fixture).
#
#   root ns  <--v-rel-out-h/v-rel-out-->  hydra-relay ns  <--v-rel-wan/v-cli-wan-->  hydra-client ns
#   (real internet via NAT)                (runs hydra-relay)                        (runs hydra client)
#
# Usage: source this file, or run `single_path.sh up` / `single_path.sh down`.
# Requires root (CAP_NET_ADMIN) and a network policy that allows the calling host real
# outbound internet access on its default route (only the relay ns's uplink needs it).
set -euo pipefail

EGRESS_DEV="${HYDRA_TEST_EGRESS_DEV:-$(ip route show default | awk '/default/ {print $5; exit}')}"

up() {
	ip netns add hydra-relay
	ip netns add hydra-client

	ip link add v-rel-out-h type veth peer name v-rel-out netns hydra-relay
	ip addr add 10.50.0.1/30 dev v-rel-out-h
	ip link set v-rel-out-h up
	ip netns exec hydra-relay ip addr add 10.50.0.2/30 dev v-rel-out
	ip netns exec hydra-relay ip link set v-rel-out up
	ip netns exec hydra-relay ip link set lo up
	ip netns exec hydra-relay ip route add default via 10.50.0.1

	ip link add v-cli-wan netns hydra-client type veth peer name v-rel-wan netns hydra-relay
	ip netns exec hydra-client ip addr add 10.60.0.1/30 dev v-cli-wan
	ip netns exec hydra-client ip link set v-cli-wan up
	ip netns exec hydra-client ip link set lo up
	ip netns exec hydra-relay ip addr add 10.60.0.2/30 dev v-rel-wan
	ip netns exec hydra-relay ip link set v-rel-wan up

	sysctl -qw net.ipv4.ip_forward=1
	iptables -t nat -C POSTROUTING -s 10.50.0.0/30 -o "$EGRESS_DEV" -j MASQUERADE 2>/dev/null || \
		iptables -t nat -A POSTROUTING -s 10.50.0.0/30 -o "$EGRESS_DEV" -j MASQUERADE
	iptables -C FORWARD -i v-rel-out-h -o "$EGRESS_DEV" -j ACCEPT 2>/dev/null || \
		iptables -A FORWARD -i v-rel-out-h -o "$EGRESS_DEV" -j ACCEPT
	iptables -C FORWARD -i "$EGRESS_DEV" -o v-rel-out-h -j ACCEPT 2>/dev/null || \
		iptables -A FORWARD -i "$EGRESS_DEV" -o v-rel-out-h -j ACCEPT
}

down() {
	ip netns del hydra-client 2>/dev/null || true
	ip netns del hydra-relay 2>/dev/null || true
	ip link del v-rel-out-h 2>/dev/null || true
	iptables -t nat -D POSTROUTING -s 10.50.0.0/30 -o "$EGRESS_DEV" -j MASQUERADE 2>/dev/null || true
	iptables -D FORWARD -i v-rel-out-h -o "$EGRESS_DEV" -j ACCEPT 2>/dev/null || true
	iptables -D FORWARD -i "$EGRESS_DEV" -o v-rel-out-h -j ACCEPT 2>/dev/null || true
}

# shape_wan applies tc netem to the client-side link of the single simulated path. No-ops
# with a warning (not a failure) if the kernel lacks sch_netem — some sandboxed/container
# kernels don't ship it; GitHub Actions' standard runners do.
shape_wan() {
	local args="$*"
	if ! ip netns exec hydra-client tc qdisc add dev v-cli-wan root netem "$args" 2>/tmp/netem-check.err; then
		echo "testbed: WARNING: tc netem unavailable in this kernel ($(cat /tmp/netem-check.err)); running unshaped" >&2
		return 1
	fi
	return 0
}

unshape_wan() {
	ip netns exec hydra-client tc qdisc del dev v-cli-wan root 2>/dev/null || true
}

case "${1:-}" in
up) up ;;
down) down ;;
shape) shift; shape_wan "$@" ;;
unshape) unshape_wan ;;
*) echo "usage: $0 {up|down|shape <netem args>|unshape}" >&2; exit 2 ;;
esac
