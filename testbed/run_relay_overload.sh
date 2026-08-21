#!/usr/bin/env bash
# Relay overload/backpressure gate.
#
# The relay already uses a bounded BULK pacer. This test proves that under intentional
# overload the queue hits its configured hard bound, reports explicit drops, continues to
# drain, keeps INTERACTIVE traffic responsive, and leaves both relay and client alive.
set -euo pipefail
cd "$(dirname "$0")/.."

TOPO=testbed/topo/two_path.sh
BUILD=build
ARTIFACTS=$(mktemp -d)
RELAY_PID=""
CLIENT_PID=""
RTT_SERVER_PID=""
IPERF_SERVER_PID=""
BULK_PID=""

log() { echo "[relay-overload] $*" >&2; }
fail() { echo "[relay-overload] FAIL: $*" >&2; exit 1; }

cleanup() {
  kill "$BULK_PID" "$IPERF_SERVER_PID" "$RTT_SERVER_PID" "$CLIENT_PID" "$RELAY_PID" 2>/dev/null || true
  wait "$BULK_PID" "$IPERF_SERVER_PID" "$RTT_SERVER_PID" "$CLIENT_PID" "$RELAY_PID" 2>/dev/null || true
  bash "$TOPO" down 2>/dev/null || true
  rm -rf "$ARTIFACTS"
}
trap cleanup EXIT

mkdir -p "$BUILD"
log "building relay, client, and RTT probe"
go build -o "$BUILD/bondify-relay" ./relay/cmd/bondify-relay
go build -o "$BUILD/bondify" ./desktop/cmd/bondify
go build -o "$BUILD/rttprobe" ./testbed/cmd/rttprobe

bash "$TOPO" down 2>/dev/null || true
bash "$TOPO" up

# Keep the physical paths fast enough that the intentional 1 Mbps application pacing limit,
# not the emulated WAN, is the bottleneck.
for side in shape0 shape1 shape0-relay shape1-relay; do
  if ! bash "$TOPO" "$side" rate 100mbit delay 15ms; then
    fail "tc netem unavailable; refusing to report an unshaped overload pass"
  fi
done

# Eight packets is intentionally tiny. A reverse TCP download should overwhelm it quickly;
# queue-full drops are expected and are the behavior under test, not a failure by themselves.
ip netns exec bondify-relay "$PWD/$BUILD/bondify-relay" \
  -listen 10.99.0.1:51820 -pool 10.77.0.0/24 -tun bondify0 \
  -key-file "$ARTIFACTS/relay.key" -mtu 1408 -scheduler hol-aware \
  -classify -bulk-limit-bps 1000000 -bulk-queue-packets 8 \
  -diag-addr 127.0.0.1:9091 \
  >"$ARTIFACTS/relay.log" 2>&1 &
RELAY_PID=$!
sleep 1

RELAY_PUB=$(grep -oE '[A-Za-z0-9+/=]{40,}' "$ARTIFACTS/relay.log" | head -1 || true)
if [ -z "$RELAY_PUB" ]; then
  cat "$ARTIFACTS/relay.log" >&2
  fail "relay did not publish its key"
fi

ip netns exec bondify-client "$PWD/$BUILD/bondify" \
  -relay 10.99.0.1:51820 -relay-pubkey "$RELAY_PUB" \
  -tun bondify0 -key-file "$ARTIFACTS/client.key" -default-route -mtu 1408 \
  -local-addrs 10.60.0.1@v-cli-wan0,10.61.0.1@v-cli-wan1 \
  -scheduler hol-aware -classify -diag-addr "" \
  >"$ARTIFACTS/client.log" 2>&1 &
CLIENT_PID=$!
sleep 2
if ! grep -q "paths=2" "$ARTIFACTS/client.log"; then
  cat "$ARTIFACTS/client.log" >&2
  fail "client did not establish both physical paths"
fi

ip netns exec bondify-relay "$PWD/$BUILD/rttprobe" -mode server -addr 10.77.0.1:22 \
  >"$ARTIFACTS/rtt-server.log" 2>&1 &
RTT_SERVER_PID=$!
ip netns exec bondify-relay iperf3 -s -B 10.77.0.1 -p 5703 \
  >"$ARTIFACTS/iperf-server.log" 2>&1 &
IPERF_SERVER_PID=$!
sleep 1

log "measuring unloaded interactive RTT"
ip netns exec bondify-client "$PWD/$BUILD/rttprobe" \
  -addr 10.77.0.1:22 -count 80 -interval 25ms -timeout 2s \
  >"$ARTIFACTS/unloaded.json"

log "overloading the relay BULK queue while measuring interactive RTT"
ip netns exec bondify-client iperf3 -c 10.77.0.1 -p 5703 -R -t 18 -J \
  >"$ARTIFACTS/bulk.json" 2>"$ARTIFACTS/bulk.err" &
BULK_PID=$!
sleep 2
ip netns exec bondify-client "$PWD/$BUILD/rttprobe" \
  -addr 10.77.0.1:22 -count 200 -interval 25ms -timeout 2s \
  >"$ARTIFACTS/loaded.json"
if ! wait "$BULK_PID"; then
  cat "$ARTIFACTS/bulk.err" >&2
  fail "reverse BULK transfer did not complete"
fi
BULK_PID=""

kill -0 "$RELAY_PID" 2>/dev/null || fail "relay died under overload"
kill -0 "$CLIENT_PID" 2>/dev/null || fail "client died under overload"

if ! ip netns exec bondify-relay curl -fsS \
  http://127.0.0.1:9091/api/v1/diagnostics >"$ARTIFACTS/relay-diag.json"; then
  fail "relay diagnostics were unavailable after overload"
fi

python3 - "$ARTIFACTS/unloaded.json" "$ARTIFACTS/loaded.json" \
  "$ARTIFACTS/bulk.json" "$ARTIFACTS/relay-diag.json" <<'PY'
import json
import sys

unloaded = json.load(open(sys.argv[1], encoding="utf-8"))
loaded = json.load(open(sys.argv[2], encoding="utf-8"))
bulk = json.load(open(sys.argv[3], encoding="utf-8"))
diag = json.load(open(sys.argv[4], encoding="utf-8"))

baseline = float(unloaded["median_ms"])
under_load = float(loaded["median_ms"])
ratio = under_load / baseline if baseline > 0 else float("inf")
mbps = float(bulk["end"]["sum_received"]["bits_per_second"]) / 1e6

sessions = diag.get("sessions", [])
if len(sessions) != 1:
    raise SystemExit(f"[relay-overload] FAIL: expected one relay session, got {len(sessions)}")
pacing = sessions[0]["aggregate"].get("bulk_pacing")
if not pacing:
    raise SystemExit("[relay-overload] FAIL: relay did not expose BULK pacing diagnostics")
if pacing["queue_capacity"] != 8:
    raise SystemExit(f"[relay-overload] FAIL: queue capacity={pacing['queue_capacity']}, want 8")
if pacing["queue_depth"] > pacing["queue_capacity"]:
    raise SystemExit(f"[relay-overload] FAIL: queue escaped its hard bound: {pacing}")
if pacing["queue_drops"] <= 0 or pacing["queue_drop_bytes"] <= 0:
    raise SystemExit(f"[relay-overload] FAIL: overload did not produce observable bounded-queue drops: {pacing}")
if pacing["sent_packets"] <= 0 or pacing["sent_bytes"] <= 0:
    raise SystemExit(f"[relay-overload] FAIL: pacer stopped draining under overload: {pacing}")
if mbps <= 0.05:
    raise SystemExit(f"[relay-overload] FAIL: only {mbps:.3f} Mbps bulk goodput; relay made no useful progress")
# INTERACTIVE bypasses the BULK pacer. Give hosted runners generous jitter headroom while
# still catching a relay that globally wedges behind the saturated BULK queue.
if ratio > 2.0:
    raise SystemExit(
        f"[relay-overload] FAIL: loaded interactive median {under_load:.2f}ms is {ratio:.2f}x "
        f"unloaded {baseline:.2f}ms (limit 2.0x)"
    )

print(
    f"[relay-overload] PASS: queue_drops={pacing['queue_drops']}, "
    f"drop_bytes={pacing['queue_drop_bytes']}, sent_packets={pacing['sent_packets']}, "
    f"bulk={mbps:.2f}Mbps, interactive={baseline:.2f}->{under_load:.2f}ms ({ratio:.2f}x)"
)
PY
