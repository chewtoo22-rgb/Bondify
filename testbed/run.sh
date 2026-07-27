#!/usr/bin/env bash
# HYDRA test harness entry point. Builds hydra-relay + hydra client, stands up the netns
# topology in testbed/topo/, and runs the gate(s) for the current phase. Extended in phase 2
# with the full tc-netem benchmark matrix from ARCHITECTURE.md §6; today it runs the phase 1
# gate: Noise handshake, TUN, encrypted DATA, NAT egress, ping + HTTP through the tunnel.
#
# Usage: sudo bash testbed/run.sh [--ci]
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

TOPO=testbed/topo/single_path.sh
BUILD=build
FAIL=0

log() { echo "[testbed] $*"; }
fail() { echo "[testbed] FAIL: $*" >&2; FAIL=1; }

cleanup() {
	kill "${RELAY_PID:-}" 2>/dev/null || true
	kill "${CLIENT_PID:-}" 2>/dev/null || true
	sleep 1
	bash "$TOPO" down || true
}
trap cleanup EXIT

log "building binaries"
mkdir -p "$BUILD"
go build -o "$BUILD/hydra-relay" ./relay/cmd/hydra-relay
go build -o "$BUILD/hydra" ./desktop/cmd/hydra

log "tearing down any stale topology"
bash "$TOPO" down || true
log "bringing up netns topology"
bash "$TOPO" up

log "starting relay"
ip netns exec hydra-relay "$PWD/$BUILD/hydra-relay" \
	-listen :51820 -pool 10.77.0.0/24 -tun hydra0 \
	-key-file /tmp/hydra-testbed-relay.key -nat-iface v-rel-out -mtu 1408 \
	>/tmp/hydra-testbed-relay.log 2>&1 &
RELAY_PID=$!
sleep 1
RELAY_PUB=$(grep -oE '[A-Za-z0-9+/=]{40,}' /tmp/hydra-testbed-relay.log | head -1)
if [ -z "$RELAY_PUB" ]; then
	fail "relay did not print a public key; log follows"
	cat /tmp/hydra-testbed-relay.log
	exit 1
fi
log "relay public key: $RELAY_PUB"

log "starting client"
ip netns exec hydra-client "$PWD/$BUILD/hydra" \
	-relay 10.60.0.2:51820 -relay-pubkey "$RELAY_PUB" \
	-tun hydra0 -key-file /tmp/hydra-testbed-client.key -default-route -mtu 1408 \
	>/tmp/hydra-testbed-client.log 2>&1 &
CLIENT_PID=$!
sleep 2
if ! grep -q "session .* established" /tmp/hydra-testbed-client.log; then
	fail "client did not establish a session; log follows"
	cat /tmp/hydra-testbed-client.log
	exit 1
fi
log "session established: $(grep 'session .* established' /tmp/hydra-testbed-client.log)"

log "gate: ping through tunnel to relay's upstream (proves ICMP forwarding, not just TCP)"
if ip netns exec hydra-client ping -c 3 -W 3 10.50.0.1 >/tmp/hydra-testbed-ping.log 2>&1; then
	log "PASS: ping through tunnel, $(grep 'packet loss' /tmp/hydra-testbed-ping.log)"
else
	fail "ping through tunnel did not succeed"
	cat /tmp/hydra-testbed-ping.log
fi

# A local HTTP target in the root namespace, reachable from the client only via
# tunnel -> relay decrypt -> relay's kernel routing/NAT -> root ns. Deliberately not a
# real internet hostname: depending on live DNS/third-party uptime makes this gate flaky
# on CI runners with their own network quirks, and a local target still exercises the
# entire real path (encryption, decryption, forwarding, NAT) end to end -- it just
# doesn't also depend on info.cern.ch being reachable from wherever CI happens to run.
HTTP_TARGET_PORT=18080
if command -v python3 >/dev/null; then
	# 10.50.0.1 is the root namespace's address on v-rel-out-h (see topo/single_path.sh);
	# the server must run in the root ns, not hydra-relay ns, to bind it.
	python3 -m http.server "$HTTP_TARGET_PORT" --bind 10.50.0.1 \
		>/tmp/hydra-testbed-httpd.log 2>&1 &
	HTTPD_PID=$!
	sleep 1

	log "gate: HTTP through tunnel to a real (locally-hosted) target via relay's NAT path"
	if ip netns exec hydra-client curl -sS -m 10 -o /dev/null -w "%{http_code}" \
		"http://10.50.0.1:${HTTP_TARGET_PORT}/" | grep -q "200"; then
		log "PASS: HTTP 200 through tunnel"
	else
		fail "HTTP request through tunnel did not return 200"
	fi

	log "gate: wire encryption (tcpdump on the client<->relay path must show no plaintext)"
	if command -v tcpdump >/dev/null; then
		ip netns exec hydra-relay timeout 4 tcpdump -i v-rel-wan -w /tmp/hydra-testbed-wire.pcap -U >/tmp/hydra-testbed-tcpdump.log 2>&1 &
		TCPDUMP_PID=$!
		sleep 0.5
		ip netns exec hydra-client curl -sS -m 5 -o /dev/null "http://10.50.0.1:${HTTP_TARGET_PORT}/" || true
		wait "$TCPDUMP_PID" 2>/dev/null || true
		if strings -a /tmp/hydra-testbed-wire.pcap 2>/dev/null | grep -qiE "GET / HTTP|HTTP/1\.[01] 200|SimpleHTTP"; then
			fail "plaintext HTTP strings found on the wire -- encryption is broken"
		else
			log "PASS: no plaintext found in $(wc -c </tmp/hydra-testbed-wire.pcap 2>/dev/null || echo 0) bytes of captured wire traffic"
		fi
	else
		log "SKIP: tcpdump not installed"
	fi

	kill "$HTTPD_PID" 2>/dev/null || true
else
	log "SKIP: python3 not installed, cannot host local HTTP gate target"
fi

log "gate: throughput through tunnel (single path, unshaped -- see README for netem-capped numbers where available)"
if command -v iperf3 >/dev/null; then
	# Bind the server to the relay's TUNNEL address (10.77.0.1), not its raw veth address
	# (10.60.0.2) -- targeting the veth IP would silently bypass hydra0 entirely and
	# measure the bare link, not the tunnel. Binding to the tunnel IP forces traffic
	# through the client's default route via hydra0, encryption, and relay decryption.
	( ip netns exec hydra-relay iperf3 -s -1 -B 10.77.0.1 -p 5599 >/tmp/hydra-testbed-iperf-server.log 2>&1 & )
	sleep 1
	if ip netns exec hydra-client iperf3 -c 10.77.0.1 -p 5599 -t 5 -J >/tmp/hydra-testbed-iperf.json 2>/tmp/hydra-testbed-iperf.err; then
		MBPS=$(python3 -c "import json;d=json.load(open('/tmp/hydra-testbed-iperf.json'));print(d['end']['sum_received']['bits_per_second']/1e6)")
		log "PASS: tunneled throughput = ${MBPS} Mbps"
	else
		fail "iperf3 through tunnel did not complete"
		cat /tmp/hydra-testbed-iperf.err
	fi
else
	log "SKIP: iperf3 not installed"
fi

if [ "$FAIL" -ne 0 ]; then
	log "one or more gates FAILED"
	exit 1
fi
log "all phase 1 gates PASSED"
