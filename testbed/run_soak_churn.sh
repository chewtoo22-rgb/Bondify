#!/usr/bin/env bash
# Longer-duration bonded tunnel soak gate with repeated asymmetric impairment and path outages.
# Usage: sudo bash testbed/run_soak_churn.sh
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

TOPO=testbed/topo/two_path.sh
BUILD=build
ARTIFACTS=$(mktemp -d)
RELAY_PID=""; CLIENT_PID=""; IPERF_PID=""

log() { echo "[soak-churn] $*" >&2; }
fail() { echo "[soak-churn] FAIL: $*" >&2; exit 1; }
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

if ! bash "$TOPO" shape0 delay 10ms 2ms loss 0.1%; then
  fail "tc netem unavailable; soak/churn gate cannot be evaluated"
fi
bash "$TOPO" unshape

ip netns exec bondify-relay "$PWD/$BUILD/bondify-relay" \
  -listen 10.99.0.1:51820 -pool 10.77.0.0/24 -tun bondify0 \
  -key-file "$ARTIFACTS/relay.key" -nat-iface v-rel-out -mtu 1408 \
  -fec=true -diag-addr "" >"$ARTIFACTS/relay.log" 2>&1 &
RELAY_PID=$!
sleep 1
RELAY_PUB=$(grep -oE '[A-Za-z0-9+/=]{40,}' "$ARTIFACTS/relay.log" | head -1)
[ -n "$RELAY_PUB" ] || fail "relay did not publish a key"

ip netns exec bondify-client "$PWD/$BUILD/bondify" \
  -relay 10.99.0.1:51820 -relay-pubkey "$RELAY_PUB" \
  -tun bondify0 -key-file "$ARTIFACTS/client.key" -default-route -mtu 1408 \
  -local-addrs "10.60.0.1,10.61.0.1" -fec=true -diag-addr "" \
  >"$ARTIFACTS/client.log" 2>&1 &
CLIENT_PID=$!
sleep 3
grep -q "paths=2" "$ARTIFACTS/client.log" || { cat "$ARTIFACTS/client.log" >&2; fail "two paths did not establish"; }

ip netns exec bondify-relay iperf3 -s -B 10.77.0.1 -p 5795 >"$ARTIFACTS/iperf-server.log" 2>&1 &
IPERF_PID=$!
sleep 1

log "starting 90s TCP soak flow with repeated asymmetric churn"
(
  set +e
  timeout 115 ip netns exec bondify-client iperf3 -c 10.77.0.1 -p 5795 -t 90 -J \
    >"$ARTIFACTS/iperf.json" 2>"$ARTIFACTS/iperf.err"
  echo $? >"$ARTIFACTS/iperf.exit"
) &
FLOW_PID=$!

# Twelve deterministic churn rounds. Every fourth round includes a hard outage on one WAN;
# other rounds vary delay/jitter/loss asymmetrically. At no point are both WANs intentionally
# disabled. This catches state leaks and path bookkeeping bugs that short one-shot gates miss.
for round in $(seq 1 12); do
  case $((round % 4)) in
    1)
      d0="18ms"; j0="5ms"; l0="0.5%"; d1="65ms"; j1="18ms"; l1="2.5%"
      ;;
    2)
      d0="85ms"; j0="22ms"; l0="4.0%"; d1="24ms"; j1="7ms"; l1="0.4%"
      ;;
    3)
      d0="35ms"; j0="12ms"; l0="1.5%"; d1="110ms"; j1="28ms"; l1="5.0%"
      ;;
    0)
      d0="28ms"; j0="8ms"; l0="0.8%"; d1="48ms"; j1="14ms"; l1="1.8%"
      ;;
  esac

  log "round $round: wan0 delay=$d0 jitter=$j0 loss=$l0; wan1 delay=$d1 jitter=$j1 loss=$l1"
  ip netns exec bondify-client tc qdisc replace dev v-cli-wan0 root netem delay "$d0" "$j0" loss "$l0"
  ip netns exec bondify-client tc qdisc replace dev v-cli-wan1 root netem delay "$d1" "$j1" loss "$l1"
  ip netns exec bondify-relay tc qdisc replace dev v-rel-wan0 root netem delay "$d0" "$j0" loss "$l0"
  ip netns exec bondify-relay tc qdisc replace dev v-rel-wan1 root netem delay "$d1" "$j1" loss "$l1"

  if (( round % 4 == 0 )); then
    if (( (round / 4) % 2 == 1 )); then
      dev_c="v-cli-wan0"; dev_r="v-rel-wan0"
    else
      dev_c="v-cli-wan1"; dev_r="v-rel-wan1"
    fi
    log "round $round: hard-dropping $dev_c/$dev_r for 2s"
    ip netns exec bondify-client tc qdisc replace dev "$dev_c" root netem loss 100%
    ip netns exec bondify-relay tc qdisc replace dev "$dev_r" root netem loss 100%
    sleep 2
  fi
  sleep 5

done

wait "$FLOW_PID" || true
[ -f "$ARTIFACTS/iperf.exit" ] || fail "iperf did not report an exit code"
[ "$(cat "$ARTIFACTS/iperf.exit")" = "0" ] || { cat "$ARTIFACTS/iperf.err" >&2; fail "TCP soak flow failed during repeated churn"; }

RECEIVED=$(python3 -c "import json; d=json.load(open('$ARTIFACTS/iperf.json')); print(int(d['end']['sum_received']['bytes']))")
RETRANS=$(python3 -c "import json; d=json.load(open('$ARTIFACTS/iperf.json')); print(int(d['end']['sum_sent'].get('retransmits',0)))")
[ "$RECEIVED" -gt 5000000 ] || fail "soak completed but transferred too little data: $RECEIVED bytes"

# Sanity-check that both processes stayed alive through the entire flow. A completed iperf
# result alone should not hide a relay/client crash immediately after the last payload packet.
kill -0 "$RELAY_PID" 2>/dev/null || fail "relay exited during soak"
kill -0 "$CLIENT_PID" 2>/dev/null || fail "client exited during soak"

log "PASS: 90s TCP soak survived 12 impairment rounds and repeated hard path outages; received=$RECEIVED bytes retransmits=$RETRANS"
