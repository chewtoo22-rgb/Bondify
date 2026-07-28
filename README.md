# Bondify

Free, open-source, self-hostable WAN bonding. Packet-level bonding of unlimited internet
connections into one tunnel — what Speedify should have been, without the subscription,
the account, the telemetry, or the vendor-controlled relay.

**Status: Phase 3 complete and verified.** Real multipath: PATH_ADD, per-path probing and
health tracking, GSN-ordered reordering, real per-path BBR-style congestion control, and
the full scheduler ladder — round robin, weighted-goodput, minRTT+cwnd, and HoL-blocking-
aware. See [ARCHITECTURE.md](ARCHITECTURE.md) for the full phase plan and
[PROTOCOL.md](PROTOCOL.md) for the wire format. Do not use this yet as your only VPN — it
is pre-alpha.

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

## Live diagnostics

Both binaries serve their current stats as JSON on a **localhost-only** HTTP endpoint —
`GET /api/v1/diagnostics` — so a dashboard (or `curl`) can poll live numbers instead of
scraping log lines. Client: `http://127.0.0.1:9090/api/v1/diagnostics` (per-path RTT/loss/
throughput plus the bonded aggregate and reorder buffer occupancy for its one session).
Relay: `http://127.0.0.1:9091/api/v1/diagnostics` (the same shape, once per connected
client session). Override with `-diag-addr`, or pass `-diag-addr ""` to disable it. It
binds to loopback by design — see `core/diag`'s doc comment for why exposing this anywhere
else would leak live per-path remote addresses to whatever network it's reachable from.

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
per-path BBR-style congestion control (`core/cc/`) landed in Phase 3, see below.

## Verified so far (Phase 2)

Run against a real two-path relay+client pair in an isolated netns rig (`testbed/run_phase2.sh`,
`testbed/topo/two_path.sh`), not code inspection:

- Real `PATH_ADD`/CTRL-ack handshake registers a second path within an already-established
  session; both paths reach `ACTIVE`.
- Round-robin genuinely alternates: in an 8-second saturating transfer, path 0 and path 1
  carried 264,278 and 264,276 packets respectively — a near-perfect 50/50 split, not an
  artifact of one path dominating.
- Per-path health tracking (RTT via probe round trips, loss via PSN-delta EWMA) updates
  live and correctly, verified against real induced loss under saturating load (loss
  climbed to 6–14% under an uncapped, CPU-bound iperf3 run, then recovered as load eased).
- Sustained two-path throughput: ~610–620 Mbps, no crashes, no stalls, across repeated runs.

**A real bug found and fixed during this verification, not just claimed away:** the
DEGRADED→ACTIVE recovery condition depends on loss dropping back below threshold, but loss
was computed purely from DATA-packet PSN deltas — so once the scheduler correctly stopped
routing DATA onto a degraded path, there was no more DATA to measure, loss stayed frozen at
its last (bad) value forever, and the path could never recover. Reproduced directly (traffic
flatlined completely, mid-test, and stayed flatlined for 5+ minutes with the process still
alive but stuck), root-caused, and fixed: a `PROBE_ACK` completing at all now feeds an
`instLoss=0` sample into the EWMA even with zero DATA in flight, so idle-but-healthy paths
recover within a few probe intervals instead of never. Re-verified after the fix across two
separate saturating runs with no recurrence.

**Known gap, honestly stated:** ARCHITECTURE.md's phase 2 gate is specifically "two
*emulated 50 Mbps* paths deliver >80 Mbps aggregate" — that requires `tc netem`/`tbf` to
actually cap per-path capacity, which this sandbox's kernel doesn't support (see the Phase 1
section above). Without a capacity cap, both paths compete for the same single-core AEAD
encrypt/decrypt budget, so a *single* uncapped path can outperform two uncapped paths
sharing that budget — that's a CPU-bound-vs-network-bound difference, not evidence against
bonding. `testbed/run_phase2.sh` is written to run the real capacity-constrained gate and
is wired into the `netem-gates` CI job, where GitHub Actions' real kernel will execute it
for real.

## Verified so far (Phase 3)

Scheduler tiers 2-4 and real per-path congestion control (`core/cc`), unit-tested (44 new
tests across `core/cc` and `core/sched`, all passing under `-race`) and run against real
running relay+client binaries in the two-path netns rig:

- Selecting `-scheduler hol-aware` on both binaries genuinely changes wire behavior: paths
  established, 733 Mbps sustained over an 8-second `iperf3` run, and the live diagnostics
  endpoint showed real, bounded, adaptive `cwnd` values (~21-25 KB) in place of Phase 2's
  placeholder unbounded congestion window, with `hol-aware` visibly favoring the
  lower-RTT/larger-cwnd path over the other.
- `core/cc`'s windowed-max delivery-rate filter and `cwnd = btl_bw * rt_prop * gain`
  formula are unit-tested directly: floors at a minimum window, caps at a maximum, survives
  one bad sample without collapsing, and genuinely drains a sustained-zero-delivery path
  out of its window (real numbers, not mocked -- see `core/cc/cc_test.go`).
- Tier 4's adaptive `lambda` strictness is unit-tested end to end: a fresh scheduler
  correctly skips a hugely mismatched slow path (15ms fast / 200ms slow, the HoL gate's own
  numbers) rather than dumping onto it, and demonstrably relaxes that skip decision once
  `lambda` has decayed from repeated slack-capacity decisions -- see
  `TestHoLAwareLambdaDecayEnablesBorderlineSlowPath`.
- `testbed/run_phase3.sh`'s harness mechanics (process orchestration, path-establishment
  log parsing, `iperf3` JSON parsing, the three gate comparisons' arithmetic) were smoke-
  tested end to end against real running binaries with shaping forced on regardless of tc
  availability -- this caught and fixed a real bug (the script's `log()` helper wrote to
  stdout, silently corrupting the `$(run_bonded ...)` throughput captures it shares that
  stream with) before it ever reached CI.

**Known gap, honestly stated:** like Phases 1 and 2, the HoL gate and the Tier 4 vs Tier 1
hetero/homo benchmark matrix specifically require `tc netem` to hold two paths at genuinely
different, asymmetric capacities (100Mbps/15ms fast vs 5Mbps/200ms/1%loss slow) -- without
that split there is no real "slow path" for Tier 4's HoL-avoidance logic to have anything to
decide about, and `testbed/run_phase3.sh` correctly detects this sandbox's lack of `tc
netem` support and exits `0` with a warning rather than fabricate a pass. It's wired into
the `netem-gates` CI job, where GitHub Actions' real kernel will execute the real
capacity-constrained gate.

Multi-socket-per-path (Speedify-style, extra throughput on one high-BDP link), real ACK
packets and real ACK-driven retransmission are not yet implemented — that's Phase 4 scope
(alongside REDUNDANT mode and adaptive FEC); per-path congestion control landed in Phase 3.

## Verified so far (Phase 4)

REDUNDANT mode and adaptive Reed-Solomon FEC (`core/fec`, wired into `core/bond`), unit-
tested (`core/fec`, `core/bond`'s FEC/mode files, and `core/reorder`'s new duplicate-while-
buffered tests, all passing under `-race`) and run against real running relay+client
binaries with real, kernel-enforced packet loss (`iptables`'s `xt_statistic` random match —
this sandbox has no `tc netem`/`sch_*` qdisc support at all, confirmed repeatedly, but does
support this separate loss-injection facility):

- `testbed/run_phase4.sh`'s both sub-gates pass for real, repeatably: sub-gate A (5% real
  random loss, single path) measured 0.115% and 0.312% application-level goodput loss
  across two independent runs, both comfortably under the <1% gate; sub-gate B
  (survivability) killed one of two bonded paths outright 4 seconds into a 12-second TCP
  transfer and the transfer completed on the surviving path alone (up to 154 Mbps average,
  zero connection resets).
- REDUNDANT mode verified directly via the live diagnostics endpoint on a real two-path
  run: both paths carried byte-for-byte identical TX counts (duplicated traffic, as
  designed) while `reorder_occupancy_bytes` stayed at 0 and `reorder_forced_releases` at 0
  throughout a 421 Mbps TCP transfer — the dedup "for free" via the reorder buffer's
  GSN-already-seen check worked with zero backlog or forced releases.
- Two real bugs were found and fixed under this real load-testing, not code inspection —
  see ARCHITECTURE.md §9 for the full writeup of both: a reorder-buffer duplicate-delivery
  bug that only REDUNDANT mode's legitimate same-GSN-on-two-paths traffic could expose, and
  a test-harness loss-injection bug (`iptables` `OUTPUT`-chain drops on the client's own
  connected UDP socket return a synchronous `EPERM` to `write()`, unlike real WAN loss,
  which silently hid dropped packets from FEC entirely) that took real, careful
  cross-checking of `tcpdump` captures against application-level packet counters to isolate
  — the actual FEC math was correct throughout; the loss it was being tested against wasn't
  reaching it the way real loss would.

## Verified so far (Phase 5)

Phase 5 is the Android client: a `gomobile`-bound wrapper around the same `core/bond` engine
every other platform uses (`mobile/`), and a real Kotlin app (`android/app`) driving it —
`VpnService` for the TUN interface, `ConnectivityManager.Network.bindSocket` +
`VpnService.protect()` per uplink for Wi-Fi/cellular path selection (the Android equivalent
of the Linux CLI's `SO_BINDTODEVICE`; see ARCHITECTURE.md §9), a foreground service +
battery-optimization-exemption prompt aimed at surviving Doze with the screen off.

**Read this section as narrowly as it's written.** ARCHITECTURE.md §5's actual gate is
"Wi-Fi+cellular bonded > either alone; 30min screen-off survival" — that needs a real device
with two live radios and real OS power management, and this project's build environment has
neither (no `/dev/kvm`, so not even an emulator; checked by hand the same way `tc
netem`/`xt_statistic` availability gets checked every other phase). **That gate is not
claimed as passed.** What's below is what could genuinely be verified without one, no more:

- A real Android SDK + NDK + `gomobile` toolchain cross-compiles `core`/`mobile` for
  `android/arm64` and `android/arm`, and `gomobile bind` produces a real AAR carrying real
  native libraries (`libgojni.so`) for all four Android ABIs (arm64-v8a, armeabi-v7a, x86,
  x86_64).
- `android/app` is a real Gradle/Kotlin project that builds a real, installable debug APK
  end to end via `./gradlew :app:assembleDebug` — Go core → JNI bindings → Kotlin app, the
  whole chain, not just individual pieces checked in isolation. `android-app` in CI does the
  same on every PR, on a real GitHub Actions runner.
- `core/bond.DialClient` was split into `DialHandshake` + `AttachTUN` (existing callers
  unaffected — `DialClient` is now just the two called back to back) to solve a real
  ordering problem Android's `VpnService.Builder` forces: the interface must be built with
  the relay's dynamically-assigned tunnel IP, which isn't known until *after* the handshake
  completes, but the Builder can't be reconfigured once `establish()` is called.

**Known gap, honestly stated:** no real device or emulator testing happened here at all —
not the bonded-throughput comparison, not the screen-off survival window, not even a basic
"does the VPN permission dialog work and does traffic actually flow" smoke test. That's real
outstanding work for whoever picks this up with access to an actual phone.

## Scheduler tiers

Pass `-scheduler <name>` to either binary to pick the tier (default `round-robin`):
`round-robin` (Tier 1, the baseline), `weighted-goodput` (Tier 2), `min-rtt-cwnd` (Tier 3),
`hol-aware` (Tier 4). See ARCHITECTURE.md §2.1 for what each one actually does and why
Tier 3's "dumps onto the slow path once the fast path's window fills" behavior is a known,
documented tradeoff that Tier 4 exists to fix.

## Phase plan

See [ARCHITECTURE.md](ARCHITECTURE.md) §5 for the full table with gates. Short version:
Phases 0–4 are done and verified. Phase 5 (Android) has a real, building app and gomobile
bridge, but its actual gate (bonded throughput, screen-off survival) needs a real device and
has not been verified — see "Verified so far (Phase 5)" above. Phase 6 (Desktop) is next
after that.
