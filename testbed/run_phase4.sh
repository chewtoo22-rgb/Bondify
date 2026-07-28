#!/usr/bin/env bash
# Phase 4 gates (ARCHITECTURE.md §5): "REDUNDANT + adaptive FEC: 5% loss -> <1% goodput
# loss, no TCP breaks on path death."
#
# Unlike phases 1-3's gates, this one does NOT need tc netem: it injects real random
# packet loss with the `xt_statistic` iptables match module (a different kernel facility
# from netem/tbf qdiscs), which this project's dev sandbox happens to support even though
# it has no loadable qdisc modules at all -- confirmed by hand before writing this script.
# If a runner genuinely lacks xt_statistic, this script detects that and exits 0 with a
# warning rather than fabricate a pass, matching the honest pattern in run.sh/run_phase2.sh/
# run_phase3.sh.
#
# Loss is injected on the RELAY's INPUT chain (packets arriving from the client), not the
# client's OUTPUT chain. This matters for real reasons, found by hand while debugging this
# gate: a DROP verdict in a *local* OUTPUT chain is delivered synchronously back to the
# owning connected UDP socket as an immediate EPERM on write() (confirmed with a minimal
# Go/Python reproduction against this exact sandbox kernel) -- unlike real WAN loss, which
# never gives the sender any signal. sendSpeed() (correctly) treats a write() error as
# "this packet never left the machine" and returns before recording it into a FEC
# generation, so a client-side OUTPUT drop is invisible to FEC by construction, not a bug
# in FEC itself. Dropping on the relay's INPUT chain instead reproduces real loss
# faithfully: the client's write() always succeeds, the packet is genuinely recorded for
# FEC, and it simply never arrives -- exactly the failure mode FEC is meant to recover
# from. See ARCHITECTURE.md §9.
#
# Two independent sub-gates:
#   A. FEC:  single path, 5% random loss injected client->relay, real UDP traffic through
#      the tunnel must show <1% loss at the application layer (iperf3's own count).
#   B. Survivability: two paths, one is killed outright (100% loss) partway through a
#      long-running TCP transfer; the transfer must complete successfully on the
#      surviving path alone, with zero resets.
#
# Usage: sudo bash testbed/run_phase4.sh
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

SINGLE_TOPO=testbed/topo/single_path.sh
TWO_TOPO=testbed/topo/two_path.sh
BUILD=build
FAIL=0

log() { echo "[phase4] $*" >&2; }
fail() { echo "[phase4] FAIL: $*" >&2; FAIL=1; }

cleanup() {
	kill "${RELAY_PID:-}" "${CLIENT_PID:-}" "${IPERF_PID:-}" 2>/dev/null || true
	sleep 1
	bash "$SINGLE_TOPO" down 2>/dev/null || true
	bash "$TWO_TOPO" down 2>/dev/null || true
}
trap cleanup EXIT

log "building binaries"
mkdir -p "$BUILD"
go build -o "$BUILD/bondify-relay" ./relay/cmd/bondify-relay
go build -o "$BUILD/bondify" ./desktop/cmd/bondify

# Confirm xt_statistic is actually usable before committing to either sub-gate -- probed
# with a rule that's immediately removed again, no lasting effect.
if ! iptables -t filter -A OUTPUT -m statistic --mode random --probability 0.5 -j ACCEPT 2>/tmp/bondify-phase4-statistic-check.err; then
	log "WARNING: iptables xt_statistic unavailable on this kernel ($(cat /tmp/bondify-phase4-statistic-check.err)); phase 4 gates cannot be meaningfully evaluated here. Exiting without pass/fail."
	exit 0
fi
iptables -t filter -D OUTPUT -m statistic --mode random --probability 0.5 -j ACCEPT

##########################################################################
# Sub-gate A: FEC -- 5% loss -> <1% goodput loss, single path
##########################################################################
log "=== sub-gate A: FEC recovery under 5% real random loss (single path) ==="
bash "$SINGLE_TOPO" down 2>/dev/null || true
bash "$SINGLE_TOPO" up

ip netns exec bondify-relay "$PWD/$BUILD/bondify-relay" \
	-listen :51820 -pool 10.77.0.0/24 -tun bondify0 \
	-key-file /tmp/bondify-phase4-relay.key -nat-iface v-rel-out -mtu 1408 \
	-fec=true -diag-addr "" \
	>/tmp/bondify-phase4-relay.log 2>&1 &
RELAY_PID=$!
sleep 1
RELAY_PUB=$(grep -oE '[A-Za-z0-9+/=]{40,}' /tmp/bondify-phase4-relay.log | head -1)
if [ -z "$RELAY_PUB" ]; then
	fail "sub-gate A: relay did not print a public key"
	cat /tmp/bondify-phase4-relay.log >&2
	exit 1
fi

ip netns exec bondify-client "$PWD/$BUILD/bondify" \
	-relay 10.60.0.2:51820 -relay-pubkey "$RELAY_PUB" \
	-tun bondify0 -key-file /tmp/bondify-phase4-client.key -default-route -mtu 1408 \
	-fec=true -diag-addr "" \
	>/tmp/bondify-phase4-client.log 2>&1 &
CLIENT_PID=$!
sleep 2
if ! grep -q "tun bondify0 up" /tmp/bondify-phase4-client.log; then
	fail "sub-gate A: client tunnel did not come up"
	cat /tmp/bondify-phase4-client.log >&2
	exit 1
fi

log "injecting 5% real random loss on the relay's inbound UDP from the client (see the header comment on why INPUT on the relay, not OUTPUT on the client)"
ip netns exec bondify-relay iptables -A INPUT -i v-rel-wan -p udp --dport 51820 \
	-m statistic --mode random --probability 0.05 -j DROP

ip netns exec bondify-relay iperf3 -s -B 10.77.0.1 -p 5700 >/tmp/bondify-phase4-iperf-server.log 2>&1 &
IPERF_PID=$!
sleep 1

log "sub-gate A: warm-up flow so the adaptive loss estimate converges before measuring (EWMA alpha=0.2 takes several probe intervals to reach steady state from a cold session -- judging FEC from packet 1 would conflate adaptation lag with recovery efficacy, the same reason nobody benchmarks TCP congestion control from a cold start either)"
ip netns exec bondify-client iperf3 -c 10.77.0.1 -p 5700 -u -b 20M -t 5 >/dev/null 2>&1 || true

log "sub-gate A: running the measured non-saturating UDP flow through the lossy path"
if timeout 20 ip netns exec bondify-client iperf3 -c 10.77.0.1 -p 5700 -u -b 20M -t 8 -J \
	>/tmp/bondify-phase4-iperf-a.json 2>/tmp/bondify-phase4-iperf-a.err; then
	GOODPUT_LOSS=$(python3 -c "import json;d=json.load(open('/tmp/bondify-phase4-iperf-a.json'));print(d['end']['sum']['lost_percent'])")
	log "sub-gate A: application-visible (post-FEC) loss = ${GOODPUT_LOSS}%"
	python3 -c "
import sys
loss = $GOODPUT_LOSS
sys.exit(0 if loss < 1.0 else 1)
" && log "PASS: sub-gate A (${GOODPUT_LOSS}% < 1%)" || fail "sub-gate A: goodput loss ${GOODPUT_LOSS}% >= 1%"
else
	fail "sub-gate A: iperf3 UDP flow through the tunnel did not complete"
	cat /tmp/bondify-phase4-iperf-a.err >&2
fi

kill "$RELAY_PID" "$CLIENT_PID" "$IPERF_PID" 2>/dev/null || true
wait "$RELAY_PID" "$CLIENT_PID" "$IPERF_PID" 2>/dev/null || true
RELAY_PID=""; CLIENT_PID=""; IPERF_PID=""
bash "$SINGLE_TOPO" down

##########################################################################
# Sub-gate B: survivability -- TCP transfer completes with zero resets when
# one of two bonded paths is killed outright partway through.
##########################################################################
log "=== sub-gate B: TCP survives one path dying mid-transfer (two paths) ==="
bash "$TWO_TOPO" down 2>/dev/null || true
bash "$TWO_TOPO" up

ip netns exec bondify-relay "$PWD/$BUILD/bondify-relay" \
	-listen 10.99.0.1:51820 -pool 10.77.0.0/24 -tun bondify0 \
	-key-file /tmp/bondify-phase4-relay2.key -nat-iface v-rel-out -mtu 1408 \
	-fec=true -diag-addr "" \
	>/tmp/bondify-phase4-relay2.log 2>&1 &
RELAY_PID=$!
sleep 1
RELAY_PUB=$(grep -oE '[A-Za-z0-9+/=]{40,}' /tmp/bondify-phase4-relay2.log | head -1)
if [ -z "$RELAY_PUB" ]; then
	fail "sub-gate B: relay did not print a public key"
	cat /tmp/bondify-phase4-relay2.log >&2
	exit 1
fi

ip netns exec bondify-client "$PWD/$BUILD/bondify" \
	-relay 10.99.0.1:51820 -relay-pubkey "$RELAY_PUB" \
	-tun bondify0 -key-file /tmp/bondify-phase4-client2.key -default-route -mtu 1408 \
	-local-addrs "10.60.0.1,10.61.0.1" -fec=true -diag-addr "" \
	>/tmp/bondify-phase4-client2.log 2>&1 &
CLIENT_PID=$!
sleep 2
if ! grep -q "paths=2" /tmp/bondify-phase4-client2.log; then
	fail "sub-gate B: client did not establish both paths"
	cat /tmp/bondify-phase4-client2.log >&2
	exit 1
fi

ip netns exec bondify-relay iperf3 -s -B 10.77.0.1 -p 5701 >/tmp/bondify-phase4-iperf-server-b.log 2>&1 &
IPERF_PID=$!
sleep 1

log "sub-gate B: starting a 12s TCP transfer, killing path 1 (v-cli-wan1) 4s in"
(
	timeout 20 ip netns exec bondify-client iperf3 -c 10.77.0.1 -p 5701 -t 12 -J \
		>/tmp/bondify-phase4-iperf-b.json 2>/tmp/bondify-phase4-iperf-b.err
	echo $? >/tmp/bondify-phase4-iperf-b.exit
) &
IPERF_CLIENT_PID=$!
sleep 4
log "killing path 1 outright (100% loss, simulating a dead uplink)"
ip netns exec bondify-client iptables -A OUTPUT -o v-cli-wan1 -j DROP
wait "$IPERF_CLIENT_PID" || true

if [ ! -f /tmp/bondify-phase4-iperf-b.exit ] || [ "$(cat /tmp/bondify-phase4-iperf-b.exit)" -ne 0 ]; then
	fail "sub-gate B: TCP transfer did not complete after path 1 died"
	cat /tmp/bondify-phase4-iperf-b.err >&2 2>/dev/null || true
else
	RETRANSMITS=$(python3 -c "import json;d=json.load(open('/tmp/bondify-phase4-iperf-b.json'));print(d['end']['sum_sent'].get('retransmits','n/a'))" 2>/dev/null || echo "n/a")
	MBPS=$(python3 -c "import json;d=json.load(open('/tmp/bondify-phase4-iperf-b.json'));print(d['end']['sum_received']['bits_per_second']/1e6)")
	log "PASS: sub-gate B -- TCP transfer completed on the surviving path (retransmits=${RETRANSMITS}, ${MBPS} Mbps average)"
fi

if [ "$FAIL" -ne 0 ]; then
	log "phase 4 gates FAILED"
	exit 1
fi
log "phase 4 gates PASSED"
