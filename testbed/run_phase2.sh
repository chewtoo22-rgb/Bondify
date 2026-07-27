#!/usr/bin/env bash
# Phase 2 gate (ARCHITECTURE.md §5): "two emulated 50 Mbps paths deliver a single-flow
# download exceeding 80 Mbps." Requires tc netem to actually cap link capacity -- without
# it there is no per-path capacity constraint for bonding to relieve, and a single
# CPU-bound (AEAD-encryption-bound) path can outperform two paths sharing the same core
# budget, which is a meaningless comparison for this gate. See README.md's "Verified so
# far" section for why this sandbox specifically cannot run it, and testbed/topo/two_path.sh
# for the topology this script drives.
#
# Usage: sudo bash testbed/run_phase2.sh
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

TOPO=testbed/topo/two_path.sh
BUILD=build
FAIL=0

log() { echo "[phase2] $*"; }
fail() { echo "[phase2] FAIL: $*" >&2; FAIL=1; }

cleanup() {
	kill "${RELAY_PID:-}" 2>/dev/null || true
	kill "${CLIENT_PID:-}" 2>/dev/null || true
	kill "${IPERF_PID:-}" 2>/dev/null || true
	sleep 1
	bash "$TOPO" down || true
}
trap cleanup EXIT

log "building binaries"
mkdir -p "$BUILD"
go build -o "$BUILD/bondify-relay" ./relay/cmd/bondify-relay
go build -o "$BUILD/bondify" ./desktop/cmd/bondify

log "tearing down any stale topology"
bash "$TOPO" down || true
log "bringing up two-path netns topology"
bash "$TOPO" up

log "shaping both paths to 50mbit/20ms"
SHAPED=1
bash "$TOPO" shape0 delay 20ms rate 50mbit || SHAPED=0
bash "$TOPO" shape1 delay 20ms rate 50mbit || SHAPED=0
if [ "$SHAPED" -eq 0 ]; then
	log "WARNING: tc netem unavailable in this kernel; this gate cannot be meaningfully evaluated here (see script header). Exiting without pass/fail."
	exit 0
fi

log "starting relay (fixed tunnel-facing address, required for multipath -- see two_path.sh)"
ip netns exec bondify-relay "$PWD/$BUILD/bondify-relay" \
	-listen 10.99.0.1:51820 -pool 10.77.0.0/24 -tun bondify0 \
	-key-file /tmp/bondify-phase2-relay.key -nat-iface v-rel-out -mtu 1408 \
	>/tmp/bondify-phase2-relay.log 2>&1 &
RELAY_PID=$!
sleep 1
RELAY_PUB=$(grep -oE '[A-Za-z0-9+/=]{40,}' /tmp/bondify-phase2-relay.log | head -1)
if [ -z "$RELAY_PUB" ]; then
	fail "relay did not print a public key; log follows"
	cat /tmp/bondify-phase2-relay.log
	exit 1
fi

log "starting iperf3 server on the relay's tunnel gateway"
ip netns exec bondify-relay iperf3 -s -B 10.77.0.1 -p 5700 >/tmp/bondify-phase2-iperf-server.log 2>&1 &
IPERF_PID=$!
sleep 1

log "starting two-path client"
ip netns exec bondify-client "$PWD/$BUILD/bondify" \
	-relay 10.99.0.1:51820 -relay-pubkey "$RELAY_PUB" \
	-tun bondify0 -key-file /tmp/bondify-phase2-client.key -default-route -mtu 1408 \
	-local-addrs "10.60.0.1,10.61.0.1" \
	>/tmp/bondify-phase2-client.log 2>&1 &
CLIENT_PID=$!
sleep 2
if ! grep -q "paths=2" /tmp/bondify-phase2-client.log; then
	fail "client did not establish both paths; log follows"
	cat /tmp/bondify-phase2-client.log
	exit 1
fi
log "both paths established: $(grep 'session .* established' /tmp/bondify-phase2-client.log)"

log "gate: single-flow throughput across two 50mbit/20ms paths must exceed 80 Mbps"
if timeout 20 ip netns exec bondify-client iperf3 -c 10.77.0.1 -p 5700 -t 10 -J \
	>/tmp/bondify-phase2-iperf.json 2>/tmp/bondify-phase2-iperf.err; then
	MBPS=$(python3 -c "import json;d=json.load(open('/tmp/bondify-phase2-iperf.json'));print(d['end']['sum_received']['bits_per_second']/1e6)")
	log "measured: ${MBPS} Mbps"
	python3 -c "
import sys
mbps = $MBPS
sys.exit(0 if mbps > 80 else 1)
" && log "PASS: ${MBPS} Mbps > 80 Mbps" || fail "${MBPS} Mbps did not exceed the 80 Mbps gate"
else
	fail "iperf3 through the two-path tunnel did not complete"
	cat /tmp/bondify-phase2-iperf.err
fi

log "informational: per-path split (should be roughly even under round-robin)"
tail -5 /tmp/bondify-phase2-client.log || true

if [ "$FAIL" -ne 0 ]; then
	log "phase 2 gate FAILED"
	exit 1
fi
log "phase 2 gate PASSED"
