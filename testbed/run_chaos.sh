#!/usr/bin/env bash
# Bondify deterministic WAN torture gate.
# Exercises heterogeneous physical MTUs plus repeated path flapping during one TCP flow.
# Usage: sudo bash testbed/run_chaos.sh
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

TOPO=testbed/topo/two_path.sh
BUILD=build
ARTIFACTS=$(mktemp -d)
RELAY_PID=""; CLIENT_PID=""; IPERF_PID=""

log() { echo "[chaos] $*" >&2; }
fail() { echo "[chaos] FAIL: $*" >&2; exit 1; }
cleanup() {
  kill "${RELAY_PID:-}" "${CLIENT_PID:-}" "${IPERF_PID:-}" 2>/dev/null || true
  bash "$TOPO" down 2>/dev/null || true
  rm -rf "$ARTIFACTS"
}
trap cleanup EXIT

mkdir -p "$BUILD"
go build -o "$BUILD/bondify-relay" ./relay/cmd/bondify-relay
go build -o "$BUILD/bondify" ./desktop/cmd/bondify

bash "$TOPO" down 2>/dev/null || true
bash "$TOPO" up

# Deliberately make the two WANs disagree about L2 MTU. The tunnel MTU stays below both,
# proving Bondify can operate safely across a low-MTU path without relying on fragmentation.
ip netns exec bondify-client ip link set dev v-cli-wan1 mtu 1280
ip netns exec bondify-relay  ip link set dev v-rel-wan1 mtu 1280
ip netns exec bondify-client ip link set dev v-cli-wan2 mtu 1500
ip netns exec bondify-relay  ip link set dev v-rel-wan2 mtu 1500

ip netns exec bondify-relay "$PWD/$BUILD/bondify-relay" \
  -listen 10.99.0.1:51820 -pool 10.77.0.0/24 -tun bondify0 \
  -key-file "$ARTIFACTS/relay.key" -nat-iface v-rel-out -mtu 1200 \
  -fec=true -diag-addr "" >"$ARTIFACTS/relay.log" 2>&1 &
RELAY_PID=$!
sleep 1
RELAY_PUB=$(grep -oE '[A-Za-z0-9+/=]{40,}' "$ARTIFACTS/relay.log" | head -1)
[ -n "$RELAY_PUB" ] || fail "relay did not publish a key"

ip netns exec bondify-client "$PWD/$BUILD/bondify" \
  -relay 10.99.0.1:51820 -relay-pubkey "$RELAY_PUB" \
  -tun bondify0 -key-file "$ARTIFACTS/client.key" -default-route -mtu 1200 \
  -local-addrs "10.60.0.1,10.61.0.1" -fec=true -diag-addr "" \
  >"$ARTIFACTS/client.log" 2>&1 &
CLIENT_PID=$!
sleep 3
grep -q "paths=2" "$ARTIFACTS/client.log" || { cat "$ARTIFACTS/client.log" >&2; fail "two paths did not establish"; }

# Sanity-check a near-MTU packet with DF set. This catches accidental tunnel/outer MTU
# assumptions before the longer churn transfer begins.
log "checking near-MTU DF traffic through heterogeneous WAN MTUs"
ip netns exec bondify-client ping -c 5 -W 2 -M do -s 1100 10.77.0.1 >/dev/null \
  || fail "1100-byte DF ping failed across the low-MTU path set"

ip netns exec bondify-relay iperf3 -s -B 10.77.0.1 -p 5790 >"$ARTIFACTS/iperf-server.log" 2>&1 &
IPERF_PID=$!
sleep 1

log "starting 24s TCP flow and repeatedly flapping alternating uplinks"
(
  timeout 35 ip netns exec bondify-client iperf3 -c 10.77.0.1 -p 5790 -t 24 -J \
    >"$ARTIFACTS/iperf.json" 2>"$ARTIFACTS/iperf.err"
  echo $? >"$ARTIFACTS/iperf.exit"
) &
FLOW_PID=$!

sleep 3
for cycle in 1 2 3 4 5 6; do
  if (( cycle % 2 == 1 )); then iface=v-cli-wan1; else iface=v-cli-wan2; fi
  log "cycle $cycle: dropping $iface for 2s"
  ip netns exec bondify-client iptables -I OUTPUT 1 -o "$iface" -j DROP
  ip netns exec bondify-client iptables -I INPUT 1 -i "$iface" -j DROP
  sleep 2
  ip netns exec bondify-client iptables -D OUTPUT -o "$iface" -j DROP
  ip netns exec bondify-client iptables -D INPUT -i "$iface" -j DROP
  sleep 1
done

wait "$FLOW_PID" || true
[ -f "$ARTIFACTS/iperf.exit" ] || fail "iperf did not report an exit code"
[ "$(cat "$ARTIFACTS/iperf.exit")" = "0" ] || { cat "$ARTIFACTS/iperf.err" >&2; fail "TCP flow did not survive repeated path flaps"; }

RECEIVED=$(python3 -c "import json; d=json.load(open('$ARTIFACTS/iperf.json')); print(int(d['end']['sum_received']['bytes']))")
[ "$RECEIVED" -gt 1000000 ] || fail "flow completed but transferred too little data: $RECEIVED bytes"

log "PASS: heterogeneous-MTU traffic and repeated alternating path flaps survived; received $RECEIVED bytes"
