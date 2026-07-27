# Bondify

Free, open-source, self-hostable WAN bonding. Packet-level bonding of unlimited internet
connections into one tunnel — what Speedify should have been, without the subscription,
the account, the telemetry, or the vendor-controlled relay.

**Status: Phase 1 complete and verified.** Single-path relay + Linux CLI client, Noise_IK
handshake, encrypted TUN tunnel, real NAT egress. Multipath bonding (the actual point of
the project) is Phase 2, in progress. See [ARCHITECTURE.md](ARCHITECTURE.md) for the full
phase plan and [PROTOCOL.md](PROTOCOL.md) for the wire format. Do not use this yet as your
only VPN — it is pre-alpha.

## Read this before you expect anything from bonding

Bonding is real, but it is not magic. These are physics, not marketing copy:

- **Bonded throughput is always less than the sum of your links.** Protocol overhead, the
  reordering tax, and the slowest path's drag all cost something. Expect 80–90% of the sum
  on well-matched links, less on mismatched ones.
- **Bonding increases latency; it never decreases it.** A relay adds roughly its own round
  trip — 8–15ms is the honest floor for a well-placed VPS. What bonding buys you is lower
  latency *variance*, not a lower ping. If you want a smaller number on a ping test, this
  is the wrong tool.
- **Your relay's bandwidth is a hard ceiling, and its RTT is a latency floor.** A cheap VPS
  in the wrong country will make everything about your connection worse, not better.
- **Links that differ wildly in speed or latency should be BACKUP, not BONDED.** A path
  10x slower or higher-latency than the rest hurts more than it helps. Bondify will tell you
  when it detects this; believe it.
- **Bondify looks like a VPN to the internet, because it is one.** Streaming services and
  banks may geoblock it. Split tunnelling with a curated default bypass list is provided
  and enabled by default for known-problematic destinations.
- **REDUNDANT mode multiplies your data usage by the duplication factor.** On metered
  links this is expensive by design — you're trading bandwidth for near-zero effective
  loss and instant failover.
- **You need a relay.** There is no way around this — packet-level bonding requires a
  single endpoint the internet sees, full stop. That's physics, not a business model. The
  relay software is free; you supply a $5 VPS.

## What this is

Three components sharing one core:

```
[client core] --N UDP paths--> [relay] --> internet
```

- **`core/`** — Go. The bonding engine: protocol framing, crypto, scheduler, reordering,
  FEC, congestion control, traffic classification, TUN abstraction. Platform-agnostic.
- **`relay/`** — Go. Single static binary + systemd unit + one-line installer (coming in
  the release phase). Runs on the cheapest VPS you can find.
- **`android/`** — Kotlin + Compose `VpnService` shell around a `gomobile`-built AAR of
  `core/`. (Phase 5, not yet started.)
- **`desktop/`** — Go. `wintun`/tray on Windows, TUN + CLI on Linux/macOS. The Linux CLI
  (`desktop/cmd/bondify`) is what Phase 1 is built and verified against today.

## License

Split, deliberately: **AGPLv3** for `relay/` (a modified relay run as a network service
must publish its source — no silent proprietary-relay forks), **Apache-2.0** for
everything else including `core/` (the client side should be free to embed anywhere,
commercially or not, with zero friction). See [LICENSING.md](LICENSING.md).

## Quickstart (Phase 1: single path, Linux)

This gets you a working *single-path* encrypted tunnel — not bonding yet, that's Phase 2.
It's useful today mainly to prove the crypto/tunnel core works end to end.

```sh
go build -o build/bondify-relay ./relay/cmd/bondify-relay
go build -o build/bondify ./desktop/cmd/bondify

# On the relay host (needs a real internet-facing interface, e.g. eth0):
sudo ./build/bondify-relay -listen :51820 -pool 10.77.0.0/24 -tun bondify0 \
     -nat-iface eth0 -key-file /etc/bondify/relay.key
# prints its public key on startup

# On the client:
sudo ./build/bondify -relay <relay-host>:51820 -relay-pubkey <printed-key> \
     -tun bondify0 -default-route
```

`testbed/run.sh` stands up an isolated network-namespace rig (client ns + relay ns, relay
bridged to a real internet-facing egress) and runs the Phase 1 gate end to end — Noise
handshake, ping and HTTP through the tunnel against a real internet host, a `tcpdump`
check that the wire carries no plaintext, and a throughput measurement. Run it with:

```sh
sudo bash testbed/run.sh
```

## Verified so far (Phase 1)

Run against a real relay+client pair in an isolated netns rig with a genuine internet
egress (not mocked), captured output, not code inspection:

- Real `Noise_IK_25519_ChaChaPoly_BLAKE2s` handshake completes; relay assigns a session
  index and tunnel IP.
- ICMP forwards through the tunnel end to end with 0% loss.
- A real HTTP request to a real internet host through the tunnel returns `200`.
- `tcpdump` on the client↔relay link during that request finds zero plaintext HTTP bytes
  — every byte on the wire is AEAD ciphertext.
- The client-side routing-loop guard (pin the relay's own address to the physical uplink
  *before* installing a tunnel-wide route) is in place and verified structurally in the
  rig's routing table.
- Sustained single-path throughput through the tunnel, single core, real per-packet
  ChaCha20-Poly1305: several hundred Mbps to just under 1 Gbps depending on run (see
  `testbed/run.sh` output) — comfortably enough headroom for any real-world WAN link this
  project targets.
- Nonce-reuse-across-paths is structurally impossible by construction (unit-tested); AEAD
  replay/tamper rejection is unit-tested.

**Known gap, honestly stated:** this sandbox's kernel does not have the `sch_netem` (or
even `sch_tbf`) qdisc modules available (no loadable kernel modules), so the
bandwidth-capped "≥90% of raw link" comparison called for by the spec could not be run
here. `testbed/topo/single_path.sh`'s `shape` command applies `tc netem` correctly and
will run for real in CI (GitHub Actions' standard runners ship `sch_netem`) or on any
normal Linux dev box — see the `netem-gates` CI job. Until Phase 2's multipath scheduler
lands, there's also no congestion control or retransmission on the single UDP path, so a
saturating flow currently relies entirely on the tunnelled protocol's own recovery (e.g.
TCP retransmits) rather than anything Bondify does — expected and by design for this phase;
per-path BBR-style congestion control is Phase 3 scope (`core/cc/`, not yet implemented).

## Phase plan

See [ARCHITECTURE.md](ARCHITECTURE.md) §5 for the full table with gates. Short version:
Phase 0 (scaffold) and Phase 1 (this) are done and verified. Phase 2 (multipath +
round-robin + reordering) is next — that's the phase where the product actually starts
being a bonder rather than just an encrypted tunnel.
