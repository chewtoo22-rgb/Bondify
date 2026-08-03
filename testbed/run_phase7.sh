#!/usr/bin/env bash
# Phase 7 gate (ARCHITECTURE.md section 5): a real reverse BULK transfer runs through the
# bonded tunnel while a persistent TCP echo flow on port 22 measures SSH-shaped
# INTERACTIVE RTT. The loaded median must remain within 25% of its unloaded baseline, and
# bulk goodput must stay high enough to prove the link was genuinely loaded.
set -euo pipefail
cd "$(dirname "$0")/.."

TOPO=testbed/topo/two_path.sh
BUILD=build
ARTIFACTS=/tmp/bondify-phase7
RELAY_PID=""
CLIENT_PID=""
RTT_SERVER_PID=""
IPERF_SERVER_PID=""
BULK_PID=""

log() { echo "[phase7] $*" >&2; }
fail() { echo "[phase7] FAIL: $*" >&2; exit 1; }

cleanup() {
	kill "$BULK_PID" "$IPERF_SERVER_PID" "$RTT_SERVER_PID" "$CLIENT_PID" "$RELAY_PID" 2>/dev/null || true
	wait "$BULK_PID" "$IPERF_SERVER_PID" "$RTT_SERVER_PID" "$CLIENT_PID" "$RELAY_PID" 2>/dev/null || true
	bash "$TOPO" down || true
}
trap cleanup EXIT

mkdir -p "$BUILD" "$ARTIFACTS"
log "building relay, client, and real TCP RTT probe"
go build -o "$BUILD/bondify-relay" ./relay/cmd/bondify-relay
go build -o "$BUILD/bondify" ./desktop/cmd/bondify
go build -o "$BUILD/rttprobe" ./testbed/cmd/rttprobe

bash "$TOPO" down || true
bash "$TOPO" up

# Shape both directions. Existing phase 2-4 gates shape client egress because they measure
# uploads; this gate is explicitly a download, so relay egress must carry the same limits.
SHAPE="rate 50mbit delay 20ms"
for side in shape0 shape1 shape0-relay shape1-relay; do
	# shellcheck disable=SC2086
	if ! bash "$TOPO" "$side" $SHAPE; then
		fail "tc netem unavailable; refusing to report an unshaped Phase 7 pass"
	fi
done

ip netns exec bondify-relay "$PWD/$BUILD/bondify-relay" \
	-listen 10.99.0.1:51820 -pool 10.77.0.0/24 -tun bondify0 \
	-key-file "$ARTIFACTS/relay.key" -mtu 1408 -scheduler hol-aware \
	-classify -bulk-limit-bps 70000000 -bulk-queue-packets 2048 \
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
ip netns exec bondify-relay iperf3 -s -B 10.77.0.1 -p 5700 \
	>"$ARTIFACTS/iperf-server.log" 2>&1 &
IPERF_SERVER_PID=$!
sleep 1

log "measuring unloaded SSH-shaped RTT"
ip netns exec bondify-client "$PWD/$BUILD/rttprobe" \
	-addr 10.77.0.1:22 -count 80 -interval 25ms -timeout 2s \
	>"$ARTIFACTS/unloaded.json"

log "starting a saturating reverse BULK transfer and measuring loaded RTT"
ip netns exec bondify-client iperf3 -c 10.77.0.1 -p 5700 -R -t 12 -J \
	>"$ARTIFACTS/bulk.json" 2>"$ARTIFACTS/bulk.err" &
BULK_PID=$!
sleep 2
ip netns exec bondify-client "$PWD/$BUILD/rttprobe" \
	-addr 10.77.0.1:22 -count 160 -interval 25ms -timeout 2s \
	>"$ARTIFACTS/loaded.json"
if ! wait "$BULK_PID"; then
	cat "$ARTIFACTS/bulk.err" >&2
	fail "reverse BULK transfer did not complete"
fi
BULK_PID=""

if ! ip netns exec bondify-relay curl -fsS \
	http://127.0.0.1:9091/api/v1/diagnostics >"$ARTIFACTS/relay-diag.json"; then
	fail "relay pacing diagnostics were unavailable"
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
    raise SystemExit(f"[phase7] FAIL: expected one relay session, got {len(sessions)}")
pacing = sessions[0]["aggregate"].get("bulk_pacing")
if not pacing:
    raise SystemExit("[phase7] FAIL: relay did not expose BULK pacing diagnostics")
if pacing["limiter"]["bytes_per_second"] != 8_750_000:
    raise SystemExit(f"[phase7] FAIL: wrong active limiter: {pacing['limiter']}")
if pacing["sent_packets"] <= 0:
    raise SystemExit("[phase7] FAIL: no BULK packets traversed the pacer")
if mbps < 50:
    raise SystemExit(f"[phase7] FAIL: only {mbps:.1f} Mbps bulk goodput; link was not meaningfully loaded")
if ratio > 1.25:
    raise SystemExit(
        f"[phase7] FAIL: loaded median RTT {under_load:.2f}ms is {ratio:.2f}x "
        f"unloaded {baseline:.2f}ms (limit 1.25x)"
    )

print(
    f"[phase7] PASS: unloaded median={baseline:.2f}ms, "
    f"loaded median={under_load:.2f}ms ({ratio:.2f}x), bulk={mbps:.1f}Mbps, "
    f"queue_drops={pacing['queue_drops']}, scheduler_waits={pacing['scheduler_waits']}"
)
PY
