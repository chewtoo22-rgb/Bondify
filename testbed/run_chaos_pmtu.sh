#!/usr/bin/env bash
# Bondify PMTU black-hole + randomized impairment torture gate.
# Usage: sudo bash testbed/run_chaos_pmtu.sh
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

TOPO=testbed/topo/two_path.sh
BUILD=build
ARTIFACTS=$(mktemp -d)
RELAY_PID=""; CLIENT_PID=""; IPERF_PID=""

log() { echo "[chaos-pmtu] $*" >&2; }
fail() { echo "[chaos-pmtu] FAIL: $*" >&2; exit 1; }
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

# Confirm netem works. This gate is explicitly about dynamic WAN impairment, so a runner
# that cannot shape packets must not claim a pass.
if ! bash "$TOPO" shape0 delay 10ms 2ms loss 0.1%; then
  fail "tc netem unavailable; PMTU/random-churn gate cannot be evaluated"
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

# Simulate a real intermediate PMTU black hole on path 0: packets larger than the path can
# carry disappear silently, and ICMP fragmentation-needed feedback is suppressed. This is
# intentionally done with a length match instead of simply lowering the local interface MTU,
# because a local MTU error gives the sender synchronous feedback and is not a black hole.
# The threshold is below Bondify's 1408-byte tunnel packet size, so path 0 becomes unsafe
# for larger outer datagrams while path 1 remains usable.
log "installing silent PMTU black hole on wan0 (>1280-byte outer IPv4 packets; no ICMP feedback)"
ip netns exec bondify-relay iptables -I INPUT 1 -i v-rel-wan0 -p udp --dport 51820 -m length --length 1281:65535 -j DROP
ip netns exec bondify-client iptables -I INPUT 1 -i v-cli-wan0 -p udp -m length --length 1281:65535 -j DROP
ip netns exec bondify-client iptables -I INPUT 1 -p icmp --icmp-type fragmentation-needed -j DROP

ip netns exec bondify-relay iperf3 -s -B 10.77.0.1 -p 5791 >"$ARTIFACTS/iperf-server.log" 2>&1 &
IPERF_PID=$!
sleep 1

log "starting 30s TCP flow under PMTU black hole and changing WAN profiles"
(
  set +e
  timeout 45 ip netns exec bondify-client iperf3 -c 10.77.0.1 -p 5791 -t 30 -J \
    >"$ARTIFACTS/iperf.json" 2>"$ARTIFACTS/iperf.err"
  echo $? >"$ARTIFACTS/iperf.exit"
) &
FLOW_PID=$!

# Deterministic pseudo-random profile sequence. We vary both directions so ACK and data
# paths see realistic asymmetry, but never intentionally take both links fully offline.
PROFILES=(
  "18ms 7ms 1.0%|55ms 18ms 3.0%"
  "75ms 25ms 4.0%|22ms 6ms 0.5%"
  "35ms 14ms 6.0%|95ms 30ms 2.0%"
  "12ms 4ms 0.2%|70ms 25ms 5.0%"
  "60ms 20ms 3.5%|28ms 9ms 1.5%"
)

for i in "${!PROFILES[@]}"; do
  IFS='|' read -r p0 p1 <<<"${PROFILES[$i]}"
  read -r d0 j0 l0 <<<"$p0"
  read -r d1 j1 l1 <<<"$p1"
  log "profile $((i+1)): wan0 delay=$d0 jitter=$j0 loss=$l0; wan1 delay=$d1 jitter=$j1 loss=$l1"
  ip netns exec bondify-client tc qdisc replace dev v-cli-wan0 root netem delay "$d0" "$j0" loss "$l0"
  ip netns exec bondify-client tc qdisc replace dev v-cli-wan1 root netem delay "$d1" "$j1" loss "$l1"
  ip netns exec bondify-relay tc qdisc replace dev v-rel-wan0 root netem delay "$d0" "$j0" loss "$l0"
  ip netns exec bondify-relay tc qdisc replace dev v-rel-wan1 root netem delay "$d1" "$j1" loss "$l1"
  sleep 5
done

wait "$FLOW_PID" || true
[ -f "$ARTIFACTS/iperf.exit" ] || fail "iperf did not report an exit code"
[ "$(cat "$ARTIFACTS/iperf.exit")" = "0" ] || { cat "$ARTIFACTS/iperf.err" >&2; fail "TCP flow failed under PMTU black hole/random churn"; }

RECEIVED=$(python3 -c "import json; d=json.load(open('$ARTIFACTS/iperf.json')); print(int(d['end']['sum_received']['bytes']))")
RETRANS=$(python3 -c "import json; d=json.load(open('$ARTIFACTS/iperf.json')); print(int(d['end']['sum_sent'].get('retransmits',0)))")
[ "$RECEIVED" -gt 1000000 ] || fail "flow completed but transferred too little data: $RECEIVED bytes"

# Verify the black-hole rule actually matched traffic; otherwise a pass would not prove the
# intended condition was exercised.
BH_HITS=$(ip netns exec bondify-relay iptables -L INPUT -v -n -x | awk '/v-rel-wan0/ && /udp/ {sum += $1} END {print sum+0}')
[ "$BH_HITS" -gt 0 ] || fail "PMTU black-hole rule matched zero packets; test did not exercise oversized outer datagrams"

log "PASS: TCP survived silent PMTU black hole plus dynamic loss/jitter/RTT churn; received=$RECEIVED bytes retransmits=$RETRANS blackhole_hits=$BH_HITS"
