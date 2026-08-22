#!/usr/bin/env bash
# Phase 8 gate: a direct 50 Mbps uplink plus a 50 Mbps PairBond peer path must
# exceed 80 Mbps on one flow, then an explicit peer revoke during a transfer
# must leave the transfer alive on the direct path.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

TOPO=testbed/topo/two_path.sh
BUILD=build
FAIL=0

log() { echo "[phase8] $*"; }
fail() { echo "[phase8] FAIL: $*" >&2; FAIL=1; }

cleanup() {
	kill "${PROXY_PID:-}" 2>/dev/null || true
	kill "${CLIENT_PID:-}" 2>/dev/null || true
	kill "${RELAY_PID:-}" 2>/dev/null || true
	kill "${IPERF_PID:-}" 2>/dev/null || true
	sleep 1
	bash "$TOPO" down || true
}
trap cleanup EXIT

mkdir -p "$BUILD"
log "building relay, PairBond proxy, and Phase 8 client harness"
go build -o "$BUILD/bondify-relay" ./relay/cmd/bondify-relay
go build -o "$BUILD/pairbond-proxy" ./testbed/cmd/pairbond-proxy
go build -o "$BUILD/phase8-client" ./testbed/cmd/phase8-client

bash "$TOPO" down || true
bash "$TOPO" up

log "shaping direct and peer WAN paths to 50mbit/20ms"
SHAPED=1
bash "$TOPO" shape0 delay 20ms rate 50mbit || SHAPED=0
bash "$TOPO" shape1 delay 20ms rate 50mbit || SHAPED=0
if [ "$SHAPED" -eq 0 ]; then
	log "WARNING: tc netem unavailable; Phase 8 capacity gate cannot be evaluated"
	exit 0
fi

ip netns exec bondify-relay "$PWD/$BUILD/bondify-relay" \
	-listen 10.99.0.1:51820 -pool 10.77.0.0/24 -tun bondify0 \
	-key-file /tmp/bondify-phase8-relay.key -nat-iface v-rel-out -mtu 1408 \
	>/tmp/bondify-phase8-relay.log 2>&1 &
RELAY_PID=$!
sleep 1
RELAY_PUB=$(grep -oE '[A-Za-z0-9+/=]{40,}' /tmp/bondify-phase8-relay.log | head -1)
if [ -z "$RELAY_PUB" ]; then
	fail "relay public key missing"
	cat /tmp/bondify-phase8-relay.log
	exit 1
fi

ip netns exec bondify-relay iperf3 -s -B 10.77.0.1 -p 5800 >/tmp/bondify-phase8-iperf-server.log 2>&1 &
IPERF_PID=$!
sleep 1

# The proxy is deliberately in the host namespace for deterministic CI. Its
# relay-facing socket is source-bound to path 1, so all path-1 ciphertext still
# traverses the real PeerProxy implementation before reaching the relay.
ip netns exec bondify-client "$PWD/$BUILD/pairbond-proxy" \
	-listen 127.0.0.1:51821 -allowed-host 127.0.0.1 \
	-wan-local 10.61.0.1 -relay 10.99.0.1:51820 \
	>/tmp/bondify-phase8-proxy.log 2>&1 &
PROXY_PID=$!
sleep 1

ip netns exec bondify-client "$PWD/$BUILD/phase8-client" \
	-relay 10.99.0.1:51820 -relay-pubkey "$RELAY_PUB" \
	-direct-local 10.60.0.1 -peer 127.0.0.1:51821 -tun bondify0 -mtu 1408 \
	>/tmp/bondify-phase8-client.log 2>&1 &
CLIENT_PID=$!
sleep 2

if ! grep -q "paths=2" /tmp/bondify-phase8-client.log; then
	fail "PairBond path did not join as the second live Bondify path"
	cat /tmp/bondify-phase8-client.log
	exit 1
fi

log "gate A: direct + PairBond peer must aggregate above 80 Mbps"
if timeout 20 ip netns exec bondify-client iperf3 -c 10.77.0.1 -p 5800 -t 10 -J \
	>/tmp/bondify-phase8-aggregate.json 2>/tmp/bondify-phase8-aggregate.err; then
	MBPS=$(python3 -c "import json;d=json.load(open('/tmp/bondify-phase8-aggregate.json'));print(d['end']['sum_received']['bits_per_second']/1e6)")
	log "aggregate measured: ${MBPS} Mbps"
	python3 -c "import sys; sys.exit(0 if float('$MBPS') > 80 else 1)" \
		&& log "PASS gate A: ${MBPS} Mbps > 80 Mbps" \
		|| fail "aggregate ${MBPS} Mbps did not exceed 80 Mbps"
else
	fail "aggregate iperf3 did not complete"
	cat /tmp/bondify-phase8-aggregate.err || true
fi

log "gate B: explicit revoke mid-transfer must fall back without killing the flow"
ip netns exec bondify-client iperf3 -c 10.77.0.1 -p 5800 -t 12 -J \
	>/tmp/bondify-phase8-revoke.json 2>/tmp/bondify-phase8-revoke.err &
FLOW_PID=$!
sleep 4
kill -USR1 "$CLIENT_PID"

# Signal delivery and log flushing are asynchronous on loaded GitHub runners. The old gate
# killed the proxy immediately and only checked the client log after the 12-second flow,
# which occasionally produced a false negative even though the flow survived and the revoke
# had actually been processed. Require the client to acknowledge local removal within a
# tight bounded window before destroying the peer process. This tests the intended property
# directly and makes a missed/late revoke fail where it happens rather than as a log-timing
# race several seconds later.
REVOKED=0
for _ in $(seq 1 50); do
	if grep -q "peer revoked, paths=1" /tmp/bondify-phase8-client.log; then
		REVOKED=1
		break
	fi
	if ! kill -0 "$CLIENT_PID" 2>/dev/null; then
		break
	fi
	sleep 0.1
done
if [ "$REVOKED" -ne 1 ]; then
	fail "client did not confirm local path removal within 5s of explicit revoke"
	cat /tmp/bondify-phase8-client.log || true
fi

kill -TERM "$PROXY_PID" 2>/dev/null || true

if timeout 20 tail --pid="$FLOW_PID" -f /dev/null; then
	if wait "$FLOW_PID"; then
		if [ "$REVOKED" -eq 1 ]; then
			POST_MBPS=$(python3 -c "import json;d=json.load(open('/tmp/bondify-phase8-revoke.json'));print(d['end']['sum_received']['bits_per_second']/1e6)")
			log "PASS gate B: flow survived explicit revoke; whole-run goodput ${POST_MBPS} Mbps"
		fi
	else
		fail "iperf3 flow failed after peer revoke"
		cat /tmp/bondify-phase8-revoke.err || true
	fi
else
	fail "iperf3 did not finish after peer revoke"
	kill "$FLOW_PID" 2>/dev/null || true
fi

if [ "$FAIL" -ne 0 ]; then
	log "Phase 8 gate FAILED"
	exit 1
fi
log "Phase 8 gate PASSED"
