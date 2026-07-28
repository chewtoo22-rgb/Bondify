# Bondify — Architecture

Condensed from the full engineering specification. This document covers domain theory,
scheduler algorithms, and platform guidance. See `PROTOCOL.md` for the normative wire
format (BOND/1) — that document is the contract; this one is design rationale.

**Prime directive.** Packet-level bonding of an unlimited number of internet connections
into a single tunnel. No subscription, no account, no telemetry, no vendor-controlled relay.

## 1. Domain theory

### 1.1 The four tiers of multi-WAN

| Tier | Mechanism | Unit of distribution | Single flow exceeds one link? | Needs remote endpoint? |
|---|---|---|---|---|
| 0 — Failover | One link active, switch on failure | Whole device | No | No |
| 1 — Session load balancing | Round-robin new connections across WANs | TCP/UDP flow | No | No |
| 2 — Packet-level bonding | Split one flow across WANs, reassemble remotely | IP packet | **Yes** | **Yes** |
| 3 — Transport-native multipath | MPTCP / MP-QUIC | Transport segment | Yes | Yes (aware peer) |

Bondify is Tier 2, with Tier 0/1 as degraded fallback (`Local mode`, §3.7).

A TCP connection is a 4-tuple including source IP; packets from one connection can't split
across source IPs without a reassembling remote endpoint. That endpoint — the relay — is
not optional and cannot be engineered away. It presents one public IP to the internet and
reassembles the bonded stream from N client paths, and reverses the process downstream.

### 1.2 Why a custom UDP protocol, not MPTCP or MP-QUIC

- **MPTCP** needs kernel support absent on stock Android; TCP-only; middleboxes strip its
  options; scheduler isn't pluggable from userspace.
- **MP-QUIC** is architecturally right long-term but still an Internet-Draft with immature,
  inconsistent library support, and explicitly leaves scheduling undefined — which is
  exactly where this product's value lives.
- **WireGuard alone** is single-path; duplication-only tricks (Engarde) give redundancy,
  never aggregation.
- **Custom UDP** (chosen) is what Speedify, Peplink SpeedFusion, and Bondix all converged
  on independently: full userspace control of sequencing, scheduling, FEC, probing, and
  transport fallback (UDP/TCP/TLS-shaped).

### 1.3 The seven laws (be honest about these, always)

1. Aggregate throughput is always less than the sum of link capacities. Expect 80–90% on
   well-matched links.
2. Latency increases; it never decreases. A relay adds roughly its own RTT (8–15 ms best
   case). Bonding reduces latency *variance*, not latency.
3. Relay bandwidth is the hard ceiling; relay RTT is the latency floor.
4. Mismatched links hurt — a path 10x slower/higher-latency should be BACKUP, not BONDED.
5. Duplication costs ~100% overhead per extra copy; FEC costs 13–27%. Choose per traffic
   class, never globally.
6. Reordering buffer sizing is the hardest knob: too small drops, too large bufferbloats.
7. Never saturate a single path fully — reserve ~10% headroom or queueing latency spikes
   and drags the whole tunnel (Bondix "Latency Smoothing").

## 2. Algorithms

### 2.1 Scheduler ladder

Implemented in strict order, each tier selectable at runtime, each benchmarked against the
previous. Round-robin is never removed — it's the winner under small receive buffers and
the debugging baseline.

1. **Round robin** — baseline; proves framing/reordering/path management.
2. **Weighted by goodput** — deficit round robin, refilled proportional to EWMA goodput.
   Self-correcting: a stalled path drains out of rotation.
3. **minRTT + cwnd** — classic MPTCP default. Better latency on homogeneous paths, worse on
   heterogeneous (dumps onto the slow path once the fast path's window fills).
4. **HoL-blocking aware (BLEST/ECF hybrid)** — `should_use_slow_path` compares projected
   delivery time on the slow path vs. waiting for the fast path; skip the slow path when
   net-harmful. Adaptive `lambda` strictness, raised on observed HoL blocking, decayed
   otherwise. Deliberate underutilization of a slow path is correct, not a bug.
5. **Traffic-class routing (Latency Smoothing)** — classify packets (LATENCY / REALTIME /
   INTERACTIVE / BULK) and route by class. LATENCY never splits; REALTIME duplicates on the
   two best paths; BULK spreads with hard 90% headroom cap. This is the single biggest
   differentiator against every open-source bonder.

See `PROTOCOL.md` §Appendix for exact constants (`HEADROOM=0.90`, `DEGRADE_LOSS=0.15`, etc).

### 2.2 Reordering buffer

Min-heap keyed by GSN + `next_expected_gsn` + one deadline timer for the head. Released on:
in-order arrival, deadline expiry, `PUSH` flag, or buffer overflow. Deadline =
`clamp(rtt_spread * 1.5 + jitter, 20ms, 400ms)`, recomputed on path churn. Duplicate
suppression for REDUNDANT mode is free — the AEAD replay window already rejects duplicate
GSNs.

### 2.3 FEC

Systematic Reed-Solomon (`klauspost/reedsolomon`), K=10 data shards, M parity shards scaled
`clamp(observed_loss * 2.5, 0, 0.25)`. Parity goes on the **least loaded** path, never the
lossy one. Generations close at K packets or 30ms, whichever first.

### 2.4 Congestion control

Per-path BBR-style: `cwnd = btl_bw * rt_prop * gain` (gain=2.0), not loss-based — cellular
loss is often non-congestive and a Reno/CUBIC-style window collapse would systematically
underuse the LTE/5G paths this product exists to exploit. Uncoupled across paths by default
(different physical bottlenecks); shared-bottleneck pairs detected via RTT/loss correlation
and coupled only then.

### 2.5 Path MTU discovery

Binary search 1200–1500 with DF-set probes, floor 1200. `tunnel_MTU = min(path PMTU) - 52`.
**TCP MSS clamping to `tunnel_MTU - 40` is mandatory** — without it, PMTUD breaks silently
whenever ICMP Frag-Needed is filtered (very common): small requests work, large transfers
hang forever.

### 2.6 Local mode

No relay configured → degrade to Tier 1 per-flow load balancing on-device. Improves
aggregate throughput for parallel workloads only, never single-flow. Labelled in UI as
"Load Balancing — not true bonding."

## 3. Platform notes

### 3.1 Android — the routing loop (bug #1 in every homemade bonder)

`VpnService` installs a default route through the TUN interface, so every socket the app
opens — including uplink sockets to the relay — routes into its own tunnel, livelocking
with zero traffic and no error. `VpnService.protect(fd)` exempts a socket. **Call it on
every uplink socket, every time, including after reconnect/rebind.** There must be an
automated test asserting a protected socket's traffic never appears on the TUN fd.

Hold Wi-Fi and cellular simultaneously via `ConnectivityManager.requestNetwork()` with
retained `NetworkCallback` references (GC'd callbacks silently drop the network — the
classic "works for a few minutes then degrades to one path" bug). Bind uplink sockets with
`network.bindSocket()`: **bind, then protect, then connect** — binding a connected socket
throws.

Build the core with `gomobile bind`; cross the JNI boundary once per packet at most (pass
the TUN fd integer, let Go `os.NewFile` it directly) — copying every packet through JNI
caps throughput well below 100 Mbps on a phone. Socket protection must originate in
Kotlin (only `VpnService` can call `protect`), so Go asks via a callback interface.

### 3.2 Desktop

Windows: `wintun` (same driver WireGuard uses), admin-elevated service + unelevated tray
UI over a named pipe, `IP_UNICAST_IF` to force egress interface (source-IP binding alone is
not reliable on Windows). Linux/macOS: `/dev/net/tun` / `utun`, policy routing
(`ip rule` + `fwmark` + `SO_MARK`) is the robust choice over `SO_BINDTODEVICE` alone.

### 3.3 Relay

Single static Go binary, no database, no cloud dependency. `install.sh` generates a
keypair, writes `/etc/bondify/relay.conf`, installs a systemd unit, opens the firewall port,
prints client config as text and QR. Keyed by client static public key, not source IP.

## 4. Repository layout

```
bondify/
├── ARCHITECTURE.md
├── PROTOCOL.md
├── core/                    # Go — bonding engine, platform agnostic
│   ├── bond/                #   session, path, state machines
│   ├── proto/                #   BOND/1 framing, encode/decode
│   ├── crypto/               #   Noise IK wrapper
│   ├── sched/                #   scheduler tiers 1-5
│   ├── reorder/              #   reordering buffer
│   ├── fec/                  #   Reed-Solomon generations
│   ├── cc/                   #   per-path BBR-style congestion control
│   ├── classify/             #   traffic classification
│   ├── pmtu/                 #   path MTU discovery + MSS clamping
│   └── tun/                  #   platform TUN abstraction
├── relay/                   # Go — server binary
├── android/                 # Kotlin + Compose
├── desktop/                 # Go — wintun/tun, tray UI
├── testbed/                 # tc netem harness, benchmark matrix
└── docs/
```

Go for core/relay/desktop (goroutine-per-path, trivial cross-compilation, gomobile for
Android, and the whole relevant ecosystem — wireguard-go, reedsolomon, wintun — is Go).
Kotlin + Compose for the Android shell only. No Rust, no C, no second runtime.

## 5. Phase plan and gates

Each phase has a gate; do not proceed on a failing gate; never claim a gate from code
inspection alone — run it, capture output.

| Phase | Scope | Gate |
|---|---|---|
| 0 | Scaffold, docs, CI | CI green on all targets |
| 1 | Relay + Linux CLI, one path | ping + 100MB download through tunnel, tcpdump confirms encryption |
| 2 | Multipath + round robin + reorder | 2×50Mbps paths, single flow > 80Mbps |
| 3 | Scheduler tiers 2-4 | HoL gate passes; Tier 4 ≥ Tier 1 on hetero, ≤3% regression on homo |
| 4 | REDUNDANT + adaptive FEC | 5% loss → <1% goodput loss, no TCP breaks on path death |
| 5 | Android | Wi-Fi+cellular bonded > either alone; 30min screen-off survival |
| 6 | Desktop | Windows install-to-bonded < 60s, one UAC prompt |
| 7 | Traffic classification, budgets, split tunnel | loaded bulk download, SSH RTT stays within 25% of unloaded |
| 8 | PairBond, share mode | peer's cellular measurably adds throughput; instant revoke |
| 9 | Release | zero-to-bonded < 10 min from README alone |

## 6. Test harness (mandatory, built in phase 1)

`tc netem` + `tbf` in network namespaces emulate paths (bandwidth/RTT/loss/jitter). CI-fatal
gates:

1. **Aggregation gate** — homogeneous 2×50Mbps ≥ 80% of sum.
2. **HoL gate** (the most important test in the repo) — fast(100Mbps/15ms) + slow(5Mbps/200ms/1%)
   must never underperform the fast path alone.
3. **Protect-loop gate** — no packet from a protected uplink socket ever appears on the TUN fd.
4. **Survivability gate** — path death/birth/flapping complete a 100MB transfer with zero resets.
5. **MTU gate** — 1500-byte payload over a 1400-byte-MTU path completes (MSS clamping works).
6. **Fuzz gate** — `proto` decode survives fuzzing with zero panics.
7. **Nonce gate** — no nonce reused across paths (static/runtime assertion).

## 7. Security threat model

**In scope:** passive observers per-path or on all paths; off-path spoofing against session
or path state; malicious/compromised relay operators (a relay sees plaintext post-decryption,
exactly like any VPN — say so plainly); malicious PairBond LAN peers; replay/reflection.

**Out of scope (stated, not silently ignored):** global passive traffic-analysis adversaries
correlating timing across paths; sophisticated DPI defeating TLS-shaped transport;
compromised client devices.

**Requirements:** authenticate before parsing; rate-limit handshakes per source IP with a
cookie mechanism under load; path address updates only from replay-window-valid packets, ≤1/s;
PairBond peers require an explicit pairing code, never auto-trust; no telemetry, no crash
reporting, no account, no default relay logging; reproducible builds.

## 8. Documentation honesty (first-class in README, not a footnote)

- Bonded throughput is always less than the sum of your links.
- Bonding increases latency by roughly the relay round trip; it reduces variance, not latency.
- Relay bandwidth is a hard ceiling; relay RTT is a latency floor.
- Wildly mismatched links should be BACKUP, not BONDED.
- Bondify looks like a VPN to the internet, with all that implies (geoblocking risk); split
  tunnelling with a curated default bypass list ships enabled.
- REDUNDANT mode multiplies data usage by the duplication factor.
- You need a relay. That's physics, not a business model — the relay software is free.

## 9. Deviations from the source specification (tracked)

- **Noise handshake library**: `github.com/flynn/noise` instead of vendored wireguard-go
  internals — see `PROTOCOL.md` §3 implementation note. Same pattern, same primitives.
- **Header overhead is 56 bytes, not 52**: the spec's own diagram carries a 96-bit
  (12-byte) nonce in the outer datagram, but its prose totals "16 bytes" for the outer
  header and derives `HEADER_OVERHEAD=52`. The nonce's `path_id` component is only known
  after decrypting the inner header, so it cannot be reconstructed before AEAD
  verification — it must be sent explicitly. `core/proto` therefore transmits the full
  12-byte nonce, making the true clear-text prefix 20 bytes (8 fixed + 12 nonce), and
  `HeaderOverhead = 20 + 20 (inner) + 16 (tag) = 56`. `core/pmtu` computes tunnel MTU from
  this corrected constant. See `core/proto/proto.go` for the full reasoning.
- **PROBE/PROBE_ACK/PATH_ADD/PATH_DROP/CTRL skip the inner DATA header.** That header
  carries GSN (data-stream reassembly order) and PSN/Generation (loss attribution, FEC) for
  the tunnelled IP stream specifically. Control packets need neither: every packet type
  already gets replay protection for free from `crypto.Session`'s per-path AEAD nonce
  counter and replay window (independent of GSN, built and unit-tested in phase 1). Reusing
  the DATA-shaped header for control messages would conflate two genuinely different
  counters. See `core/bond/control.go`.
- **Multi-path egress on Linux uses `SO_BINDTODEVICE`, not just source-IP binding.**
  ARCHITECTURE.md §3.2 already notes source-IP binding alone is unreliable for this on
  Windows; the same problem exists on Linux the moment two paths need to reach the *same*
  destination address (the relay's one IP) — plain destination-based routing picks one
  interface for that destination regardless of which local address a socket bound to.
  `core/tun/linux.go`'s `DialUDPViaDevice` pins each path's socket to its physical interface
  via `SO_BINDTODEVICE`, overriding route selection outright; policy routing (`ip rule` +
  per-uplink tables, also spec-endorsed) is the alternative used by the phase 2 test rig
  itself (`testbed/topo/two_path.sh`) since it doesn't require elevated per-socket syscalls.
  Both are legitimate; a real client should offer device pinning as a fallback for uplinks
  the OS hasn't been configured with proper policy routing for.
- **Fixed a livelock in path DEGRADED→ACTIVE recovery, found under real load-testing.**
  Loss was computed only from DATA-packet PSN deltas; once a path degraded and the
  scheduler (correctly) stopped routing DATA onto it, there was no more DATA to measure
  loss from, so the loss estimate froze at its last value and never dropped back below the
  undegrade threshold — the path, and if every path hit this simultaneously the whole
  tunnel, could get stuck forever even after the underlying condition cleared. Fixed by
  feeding an `instLoss=0` sample into the EWMA whenever a `PROBE_ACK` completes with zero
  new DATA sent, since a completed probe round trip is itself evidence the path currently
  works. See `core/bond/path.go`'s `HandleProbeAck` and README.md's Phase 2 section for how
  this was actually reproduced (not just reasoned about) before the fix.
- **Tier 2's "deficit round robin" is implemented as smooth weighted round robin, not
  literal DRR.** Classic DRR drains a per-flow packet queue: a flow's whole backlog gets
  served (one quantum's worth of packets) before moving to the next flow's queue. BOND/1's
  scheduler has no such per-path queue — `Next` is called once per already-dequeued TUN
  packet and must return an immediate single-packet assignment — so there is nothing to
  drain. `core/sched.WeightedGoodput` instead uses the Nginx-style smooth WRR algorithm
  (each path's running current-weight is incremented by its goodput-derived weight every
  call; the highest current-weight path is picked and debited by the sum of all weights),
  which achieves the same long-run-share-proportional-to-weight goal with even
  interleaving instead of DRR's characteristic same-path bursts — a better fit for a single
  shared output stream. See `core/sched/tier2_weighted_goodput.go`'s doc comment.
- **`core/cc`'s congestion control is a simplified BBR, not the full phase-cycling state
  machine.** Real BBR cycles `pacing_gain` through STARTUP/DRAIN/PROBE_BW/PROBE_RTT phases,
  driven by a per-RTT (often per-ACK) delivery-rate sample. BOND/1 now carries per-packet
  ACK/SACK feedback, but `core/cc.Controller` is still fed by the older periodic
  `PROBE`/`PROBE_ACK` samples (`ProbeInterval` = 200ms); the ACK path has not yet been
  promoted into a full BBR delivery-rate sampler. `core/cc.Controller` uses a single
  fixed gain (2.0, matching the spec's formula) over a windowed max-filtered delivery-rate
  estimate instead, which captures the core formula (`cwnd = btl_bw * rt_prop * gain`) and
  the core self-correcting property (a stalled path's rate ages out of the window and its
  cwnd shrinks) without the full phase state machine. Revisit when ACK timing and
  per-original-path delivery accounting are wired into congestion control. See
  `core/cc/cc.go`'s package doc comment.
- **`core/cc.Controller` only receives real delivery-rate samples on the client side.**
  It's fed from
  `HandleProbeAck`'s existing PSN-delta bookkeeping (already used for loss, extended to also
  track bytes sent since the last probe), and only the client runs the probe-driven state
  machine at all (see this file's existing note on that asymmetry, above `core/bond/path.go`
  in the repo). Authenticated ACK telemetry now carries the client's per-path minimum RTT,
  so the relay can distinguish fast and slow paths for return-traffic scheduling even
  without initiating probes itself. A relay-side path's `CWND()` still stays at `core/cc`'s
  generous initial value rather than adapting — acceptable for now since it only
  under-constrains the relay's own return-traffic scheduling, never the client's. Symmetric
  relay-initiated probing or per-original-path ACK timing would close the remaining gap.
- **Fixed two real bugs in Tier 3/4's fastest-path tie-breaking, found by a real CI run, not
  code inspection.** First: a homogeneous two-path CI run showed `hol-aware` at roughly half
  of `round-robin`'s throughput. Root cause: a strict single-winner RTT comparison let one
  path permanently absorb 100% of traffic, because `core/cc`'s congestion window is a
  passive estimate of what a path has actually been asked to carry (see the simplified-BBR
  entry above) — the idle second path's window never signaled "full," so nothing ever gave
  it anything to prove itself against. Fixed by having `fastestTiedSet` treat paths within a
  small RTT tolerance as tied and round-robin among them (`core/sched/tier3_min_rtt_cwnd.go`
  and `tier4_hol_aware.go`), which also fixes the identical latent issue in Tier 3.
  Second, surfaced immediately by the first fix: before authenticated RTT feedback was
  added, the relay side never actively probed, so relay-side paths' `RTTMin()` began at the
  unmeasured sentinel — `fastestTiedSet` returned an *empty* tied set in that case, and the
  fallback
  path's own tie-breaking (`rtt < slowRTT` starting from the same sentinel) meant a
  candidate whose RTT was itself the sentinel could never win either. Together these made
  the relay-side scheduler return `nil` forever, and a real two-path tunnel's return traffic
  stalled completely under `hol-aware`. Fixed by treating "every path is unmeasured" as a
  trivial tie (all of them primary) and by tracking "found a candidate" separately from the
  RTT comparison in the fallback search. Both fixes are covered by unit tests that fail
  without them (`TestHoLAwareSharesLoadAcrossEquallyFastPaths`,
  `TestHoLAwareBothPathsUnmeasuredRTT`, and their Tier 3 equivalents) and were re-verified
  against real running relay+client binaries in the netns rig after the fix.
- **Fixed a real duplicate-delivery bug in the reorder buffer, found by REDUNDANT mode.**
  `Buffer.Push`'s duplicate check only compared an arriving GSN against `nextExpected`,
  which is sufficient when every GSN can only ever arrive once (SPEED mode: AEAD's per-path
  replay window already rejects true wire replays before a packet reaches the buffer at
  all). REDUNDANT mode legitimately delivers the *same* GSN twice, on two different paths,
  and a second copy arriving while the first is still buffered (out of order, waiting on an
  earlier gap) fell through the `nextExpected` check and created a second heap entry with an
  identical GSN. That entry could never drain normally and, if it became the heap's new
  minimum, drove `nextExpected` backwards — corrupting delivery order for everything after
  it. Fixed by tracking an explicit `inHeap` set alongside the heap so a GSN already
  buffered is dropped on arrival, not just a GSN already delivered. See
  `core/reorder/buffer.go`'s `Push` doc comment and `TestDuplicateWhileBufferedDropped`.
  Verified for real: before the fix, a two-path REDUNDANT-mode `iperf3` run stalled
  completely (timeout, zero throughput); after it, 429.7 Mbps with
  `reorder_occupancy_bytes=0` and matching TX counts on both paths.
- **The Phase 4 FEC gate's real loss injection had to move from the client's OUTPUT chain
  to the relay's INPUT chain — a test-methodology bug, not a FEC bug, but one that cost a
  long debugging session before it was found.** The gate originally injected loss with
  `iptables -A OUTPUT ... -j DROP` on the *client's own* outbound chain, matching its
  encapsulated UDP packets to the relay. On this project's kernel, a `DROP` verdict in a
  *local* `OUTPUT` chain is delivered synchronously back to the owning connected UDP
  socket's `write()` call as an immediate `EPERM` — confirmed by hand with a minimal
  Go program doing nothing but `net.Dial("udp", ...)` + repeated `Write()` against the same
  topology, no Bondify code involved. Real WAN loss never gives the sender any such signal:
  `write()` succeeds and the packet just never arrives. `ClientTunnel.sendSpeed` (correctly,
  given the actual contract of a failed write) returns on a write error *before* calling
  `fecSend.Record`, so a packet dropped this way never even enters a FEC generation — FEC
  had zero chance to protect it, and the measured "post-FEC" loss came out roughly equal to
  the raw injected rate no matter how much redundancy was configured. This was diagnosed by
  instrumenting the full send/receive path end to end (temporary, since-removed debug
  logging) and cross-checking `tcpdump` captures against the client's and relay's own
  packet counters at each stage, which showed real DATA-level loss was near zero even
  though iptables was demonstrably dropping ~5% of matching packets — the drops were
  synchronous local rejections the application had already excluded from its own
  bookkeeping. Moving the same `-m statistic --mode random --probability 0.05 -j DROP` rule
  to the *relay's* `INPUT` chain instead (`testbed/run_phase4.sh`) reproduces real loss
  faithfully: the client's `write()` always succeeds, the packet is genuinely recorded for
  FEC, and it simply never arrives. With that fix alone, FEC recovery went from
  indistinguishable-from-off to genuinely reducing 5% wire loss to roughly 1.5% goodput
  loss — still short of the gate.
- **`fec.LossScale` raised from 2.5 to 5.0 to close the remaining gap to the gate.** Once
  loss injection was fixed (above), 5% real loss with the original scale produced `m=2`
  parity shards per `K=10`-packet generation, saturating `FEC_MAX_REDUNDANCY` only at 10%+
  mean loss as originally documented. But a generation is only `K+m` shards on the wire
  (data *and* parity both go through the same lossy relay INPUT rule, each independently
  lost with probability 5%), and reconstruction needs at least `K` of those `K+m` to
  survive — so the *number* of losses in any single generation is binomially distributed
  around that 5% mean, not constant: at `m=2` (12 total shards), roughly 1.96% of
  generations statistically lose more than 2 of the 12 and go unrecovered — measured for
  real at 1.31%–1.59% goodput loss across repeated runs, consistent with that math and
  above the gate's <1% bar. `FEC_MAX_REDUNDANCY=0.25` is a hard PROTOCOL.md constant and
  wasn't touched; `LossScale` is not spec-mandated (only `FEC_K` and `FEC_MAX_REDUNDANCY`
  are, per the Appendix), so it was retuned to saturate the existing 0.25 cap at 5% mean
  loss instead of 10%, giving `m=3` (13 total shards) at the gate's own test condition. The
  same binomial math at `m=3` predicts ~0.31% unrecovered, which is what repeated real runs
  now show (0.115%, 0.312%) — comfortably under 1%, with real headroom rather than a
  coin-flip pass. See `core/fec/fec.go`'s `LossScale` doc comment.
- **`-fec` defaults to `false` on both binaries, found by a real CI regression on this PR's
  own `netem-gates` job.** It originally defaulted to `true` ("costs nothing at zero loss",
  per the flag's own now-corrected help text), but that's not literally true as
  implemented: `fecSender.Record` copies and stores every packet's full inner plaintext
  into the current generation's shard slice regardless of whether any parity will end up
  being computed for it, since the redundancy decision (`m`) isn't known until the
  generation closes. That's real per-packet allocation/copy overhead on the hot send path
  even on a perfectly clean link. `testbed/run_phase2.sh`/`run_phase3.sh` predate the `-fec`
  flag and don't pass it, so a `true` default silently added this cost to their gates too —
  caught for real when this PR's CI ran Phase 2's 2×50Mbit/20ms two-path throughput gate on
  a real GitHub Actions runner and measured 74.9 Mbps against the required >80 Mbps (previously
  passing). `testbed/run_phase4.sh` explicitly passes `-fec=true` on every invocation, so its
  own gate is unaffected by the default.
- **`bond.DialClient` split into `DialHandshake` + `AttachTUN`, for Android.** The Linux CLI
  client opens its TUN device before ever dialing the relay (`tun.Create` picks the device
  name; the IP/routes it gets *after* the handshake are applied separately via
  `tun.ConfigureLinux`), so `DialClient` always had `dev`/`mtu` available up front and just
  stored them on the `ClientTunnel` for `Run` to use later. Android has no such freedom:
  `android.net.VpnService.Builder` is immutable once `establish()` is called, and the
  address it must be built with (`addAddress`) is the relay's dynamically pool-assigned
  tunnel IP -- which isn't known until *after* the handshake completes. `DialHandshake` does
  everything `DialClient` did except touch a TUN device, returning a `*ClientTunnel` whose
  `TunnelIP`/`Prefix`/etc (via the handshake response) the caller can act on immediately;
  `AttachTUN(dev, mtu)` finishes setup once a real device exists. `DialClient` itself is now
  just `DialHandshake` + `AttachTUN` called back to back, so this doesn't change behavior for
  any existing caller -- see `mobile/mobile.go`'s `TunnelBuilder.Handshake` /
  `Tunnel.AttachTUN` and `android/app`'s `BondifyVpnService.kt` for the real caller.
- **Android's `mobile` package binds through `bond.PathSpec.Conn`, not
  `tun.DialUDPViaDevice`.** Choosing which physical network a UDP socket egresses on is
  `SO_BINDTODEVICE` from Go on Linux, but an unprivileged Android app has no equivalent
  syscall access -- the only API for it is `ConnectivityManager.Network.bindSocket`,
  callable from Kotlin/Java only. So unlike every other platform, Android's client doesn't
  let `core/bond` dial its own path sockets: `BondifyVpnService.kt` requests each physical
  network, dials+binds+`VpnService.protect()`s a `DatagramSocket` on it itself, and hands
  the resulting fd to Go via `mobile.TunnelBuilder.AddPathFD`, which adopts it as a
  `*net.UDPConn` (`net.FileConn`) and passes it through the new `PathSpec.Conn` field --
  `dialPath` uses it as-is instead of dialing anything. `core/tun/android.go` similarly
  wraps `golang.zx2c4.com/wireguard/tun`'s `CreateUnmonitoredTUNFromFD` (the same call
  WireGuard's own Android app uses) to adopt `VpnService.Builder.establish()`'s fd, since an
  app can't open `/dev/net/tun` directly either.
- **What phase 5's gate actually needed vs. what this environment could verify.**
  ARCHITECTURE.md §5's gate is "Wi-Fi+cellular bonded > either alone; 30min screen-off
  survival" -- both halves require a real Android device with two live physical radios and
  real OS power management, neither of which exists in this project's build sandbox (no
  `/dev/kvm`, so not even an emulator; confirmed by hand before writing any Android code,
  the same kind of check this project has done for `tc netem`/`xt_statistic` availability
  at every earlier phase). Per this project's own standing rule -- never claim a gate passes
  from code inspection alone -- that gate is **not claimed as passed** here. What *was* done
  for real, not just written and assumed correct: a genuine Android SDK + NDK + `gomobile`
  toolchain was installed and used to cross-compile `core`/`mobile` for `android/arm64` and
  `android/arm`, bind a real AAR (with real native libraries for all four ABIs) via
  `gomobile bind`, and build a real, installable debug APK via `./gradlew
  :app:assembleDebug` end to end -- `android-app` in CI does the same on every PR. That
  proves the whole chain (Go core → JNI bindings → Kotlin app) is real, correctly wired, and
  compiles/links/packages without lying about it; it does not and cannot prove the app
  behaves correctly on a real device, survives Doze, or actually bonds two radios for more
  throughput than either alone. That verification is real future work, not done here.
