# Bondify Project Status

This is the shared handoff for Matt, Claude, Codex, and future contributors. Update it in
every pull request that changes a phase gate, closes a tracked gap, or discovers a new one.
The repository and captured test output are authoritative; a chat transcript is not.

Last updated: 2026-07-31

## Naming

The product is **Bondify**. HYDRA is the former working name and should only appear when
preserving historical context. The wire protocol remains **BOND/1**; product renaming alone
must not cause an incompatible protocol change.

## Honest current state

| Area | Implemented | Verified | Remaining gate |
|---|---|---|---|
| Phase 0: foundation | Repository, BOND/1 docs, split licensing, CI | Core CI has passed | Keep all required gates continuously green |
| Phase 1: one-path tunnel | Linux client, relay, Noise tunnel, NAT | Netns ping/HTTP/encryption/throughput gate | Release-quality installation and external security review |
| Phase 2: multipath | PATH_ADD, probing, GSN reorder, round robin | Two-path netns runs and CI gate | More churn/flapping coverage |
| Phase 3: scheduling | Weighted, minRTT+cwnd, HoL-aware schedulers | Unit tests, shaped benchmark gate in CI | Relay-side measurement/pacing remains simplified |
| Phase 4: resilience | REDUNDANT mode, adaptive Reed-Solomon FEC, ACK/SACK and bounded retransmission | Race/unit tests plus real-loss FEC, FEC-off retransmission, and path-death CI gates | Real-device/path-flapping breadth and external security review |
| Phase 5: Android | Kotlin app, VpnService shell, gomobile AAR build, runtime AddPath/DropPath path churn (see below) | APK compilation in CI; runtime path API covered by real client+relay Go integration tests | No real-device VPN, bonding, churn, or 30-minute screen-off gate has passed |
| Phase 6: Windows desktop | wintun/tray client, `IP_UNICAST_IF` egress binding, self-elevating installer script | Builds/vets/cross-compiles clean in CI | The actual gate (install-to-bonded < 60s, one UAC prompt) needs a real Windows machine; none available in any sandbox so far |
| Phase 7+: product | Specifications only or partial scaffolding | Not verified | Traffic classification/split tunnel, PairBond/share mode, installer polish, signed releases |

Do not describe Bondify as production-ready or independently audited. It is a substantial
pre-alpha networking implementation with important real-device and security work remaining.

## Current protocol sprint

Branch: `agent/ack-sack-retransmission`

### Implemented and CI-verified

- Authenticated ACK payloads carry cumulative GSN, up to 32 SACK ranges, per-path receive
  counters and minimum RTT telemetry, reorder occupancy, and reorder deadline in both
  directions.
- A GSN tracker stays independent of reorder delivery so a deadline-forced release cannot
  falsely acknowledge a packet that never arrived.
- The first observation of each cumulative gap triggers immediate SACK feedback; packets
  above the same persistent gap return to the specified 8-packet/20 ms cadence, preventing
  slow-path reordering from creating an ACK storm. ACKs use the lowest-RTT ACTIVE path.
- Delayed-ACK accounting subtracts the snapshot that was actually transmitted instead of
  clearing concurrent arrivals; a regression test covers a second path receiving while an
  ACK is being sent.
- A client emits authenticated RTT telemetry after `PROBE_ACK`, letting the relay select
  the correct return path before application traffic. Scheduler RTT reads use a 200 ms
  atomic cache rather than locking and scanning the RTT sample window per packet.
- Packet scheduling reuses immutable path views and scheduler-owned scratch slices, and
  reads congestion windows atomically. Tier 3/4 steady-state allocation tests protect the
  packet fast path from regressing back to per-packet slice allocation.
- Senders retain at most 4096 packets / 8 MiB, retry at most three times, and process at
  most 64 retransmissions per maintenance tick.
- Fast retransmission requires three SACK reports of the same hole. It waits 10 ms for a
  single-path session or when a SACKed successor has the same original path ID as the
  missing packet; cross-path, mixed, or unavailable attribution keeps a conservative 1 s
  grace for skew/FEC recovery. Timeout recovery uses a 500 ms–1 s RTO and the multipath
  floor collapses to that normal RTO as soon as only one path remains ACTIVE.
- Retransmissions receive a fresh PSN/AEAD nonce, carry `RTX`, and do not rejoin an old FEC
  generation.
- Unit tests cover cumulative/SACK construction, the GSN-0 boundary, delayed-ACK version
  safety, selective fast retransmit, retry exhaustion, and queue bounds.
- The Phase 4 harness now has a FEC-disabled 5% loss sub-gate that requires ACK/SACK
  retransmission alone to keep application-visible UDP loss below 1%.

### Still to extend

- ACK path counters are transported but not yet used as the delivery-rate input for the
  simplified BBR controller.
- Queue eviction/retry exhaustion are bounded and observable through counters, but a
  user-facing diagnostic for dropped-unacknowledged packets is still desirable.
- Timeout-only recovery still uses a 1-second multipath floor. A tail loss with no SACKed
  successor can therefore wait that long; per-original-path ACK delivery timing could
  shorten that fallback safely.

## Completed stabilization sprint

Branch: `agent/android-path-lifecycle`

### Fixed in this sprint

- A failed `VpnService.protect()` now rejects that uplink instead of allowing a routing-loop
  socket into the tunnel.
- Initial Wi-Fi/cellular callbacks can no longer mutate `TunnelBuilder` after the collection
  window closes.
- Repeated `onAvailable` callbacks for the same transport no longer add duplicate initial
  paths.
- Java and duplicated Parcel file descriptors are closed or transferred deliberately.
- Disconnect during the initial wait cannot continue directly into VPN establishment.
- Duplicate CONNECT intents are ignored while a connection is active.
- The VPN advertises the actual acquired Wi-Fi/cellular networks through
  `setUnderlyingNetworks`.
- Relay parsing now accepts `host:port` and bracketed `[IPv6]:port` forms.
- Connection errors remain visible as `Failed` instead of being immediately overwritten by
  `Disconnected`.
- The incorrect claim that the Android service already owns a wake lock was removed.
- Phase 4's real-loss/FEC/path-death test script is now in CI. Its first CI execution on
  PR #4 failed, proving the previous manual pass was not yet a reliable continuous gate.
- The Phase 4 failure was isolated: FEC passed at 0.23% application-visible loss, but the
  relay kept scheduling return traffic onto a killed path until its 10-second DEAD timeout.
  Relay paths now leave ACTIVE scheduling after three missing 200 ms client probes, remain
  recoverable until the existing DEAD threshold, and return to ACTIVE on a valid
  authenticated probe. Deterministic transition/recovery tests were added, and the complete
  Phase 4 CI gate now passes.

### Still not fixed by this sprint

- A handshake already blocked inside Go can take up to its current retry/deadline window to
  observe Android cancellation.
- Android client private keys still use ordinary private `SharedPreferences`; migrate them
  to Android Keystore-backed storage before public release.
- There is no automated Android protect-loop test yet. Compilation is not evidence that
  sockets bypass the TUN on a real device.
- No real Android device gate has passed.

## Runtime path API sprint

Branch: `claude/summary-next-phase-am0sp0`

Closes the top item of the former P0 backlog: a safe runtime path API across `core/bond` ->
`mobile` -> Kotlin, so Android's `NetworkCallback.onLost`/`onAvailable` (network replacement,
NAT rebinding, Wi-Fi/cellular return) can add or drop an uplink from an already-running
session instead of that uplink being fixed for the session's whole lifetime.

### Implemented and test-verified (Go integration tests, real client+relay, `-race` clean)

- `ClientTunnel.AddPath(ctx, id, spec)` and `ClientTunnel.DropPath(id, reason)`: thread-safe,
  callable concurrently with `Run`, each other, and repeatedly. `AddPath` completes a real
  PATH_ADD handshake against the relay and, if `Run` has already started, immediately spawns
  the new path's read/probe loops; called before `Run`, the path is simply picked up by
  `Run`'s own startup loop, matching the pre-existing `DialHandshake` behavior. `DropPath`
  removes the path from the schedulable pool before touching its socket (so no in-flight
  scheduler decision can hand it out afterward), best-effort tells the relay via a real
  `PATH_DROP` control packet, then closes the socket.
- A real, previously-latent bug fixed as part of this: `Run` funneled every path's read-loop
  error into the same fatal channel that `ctx.Done()`/TUN errors use, so *any* single path's
  UDP socket erroring (a NIC going down, an ICMP port-unreachable, or `DropPath`'s own
  intentional close) tore down the *entire* tunnel, including every other still-healthy path.
  Runtime path replacement is meaningless without fixing this first. Only `t.dev` (TUN
  device) and TUN-pump errors are fatal now; a path-level read failure removes just that path
  (`ClientTunnel.spawnPathLoops`) and leaves the rest of the session running.
- `spawnPathLoops` is guarded by a per-`Path` `atomic.Bool` CAS so a path added concurrently
  by both `Run`'s own startup loop and a racing `AddPath` call only starts its goroutines
  once, not twice.
- Relay-side `PATH_DROP` handling (`handlePathDrop`/`relaySession.removePath`): previously
  defined on the wire (`proto.TypePathDrop`, `PathDropPayload`) but never sent or handled by
  either side. Without it, a dropped path lingered in the relay's schedulable pool for up to
  `PathDeadTimeout` (10s), during which the relay could keep routing return traffic onto a
  socket the client had already closed.
- `mobile.Tunnel.AddPathFD(fd, label)` / `Tunnel.DropPathLabel(label)`: the gomobile-bindable
  wrapper Kotlin calls, addressing paths by the same human-readable label (`"wifi"`,
  `"cellular"`) `TunnelBuilder.AddPathFD` already uses, so the Kotlin side never has to track
  raw `core/bond` path IDs itself.
- `android/app`'s `BondifyVpnService.kt`: `NetworkCallback.onAvailable`/`onLost` for both
  Wi-Fi and cellular now call the runtime API once the initial handshake-gathering window has
  closed, instead of the callbacks going inert after that window (as the code and its own
  comment previously stated explicitly).
- Verified: `go build ./...`, `go vet ./...`, `go test ./... -race` (all packages green, no
  regressions), `GOOS=android GOARCH=arm64 CGO_ENABLED=0 go build/vet ./mobile/...` (clean).
  New tests in `core/bond/client_runtime_path_test.go` and `relay_path_drop_test.go` run a
  real `*Relay` (genuine `ServeUDP`, `handlePathAdd`, `handlePathDrop`) against a real
  `DialHandshake`'d client -- not mocked -- covering: add-before-Run doesn't spawn loops yet,
  add-after-Run spawns immediately and the relay genuinely registers the new path, duplicate
  IDs are rejected, drop removes the path from both sides and a fresh `AddPath` still works
  afterward (proving the session survives), and dropping an unregistered ID errors.

### Not verified, and not claimed as passed

- No real Android device: the Kotlin changes compile against the pattern gomobile already
  uses elsewhere in this file (`builder.addPathFD` -> the new `tunnel.addPathFD`/
  `tunnel.dropPathLabel` follow the same lowercase-first-letter binding convention and
  Go-`error`-becomes-Kotlin-exception convention already proven by every other call in this
  file), but this sandbox has no Android SDK/emulator to run `gomobile bind` or Gradle
  itself -- CI's `android-app` job (gomobile bind + `assembleDebug` + unit tests) is the
  first real compiler for the Kotlin side of this change, same limitation every prior
  Android-touching session in this repo has had.
- The actual Wi-Fi<->cellular handover-survives-mid-transfer behavior this API exists to
  enable is still gated on Phase 5's real-device acceptance gate (P0.1 below), which no
  session has been able to run.

## Priority backlog

### P0 - complete before calling Phase 5 done

1. Run the APK on a physical phone:
   - permission and one-path traffic smoke test;
   - Wi-Fi-only, cellular-only, and bonded throughput comparison;
   - kill and restore each path during one TCP transfer (the runtime `AddPath`/`DropPath`
     API above is what this now exercises -- unverified on real hardware);
   - 30-minute screen-off/Doze survival;
   - reconnect after airplane mode and network roaming.
2. Add the protect-loop, MTU/MSS, fuzz, and full survivability gates specified by
   `HYDRA_Spec`/`ARCHITECTURE.md`.
3. Resolve Android key storage, log redaction, notification permission behavior, and
   foreground-service policy before distributing a release APK.

### P1 - desktop and deployability

1. ~~Windows service/tray split using Wintun and `IP_UNICAST_IF`.~~ Implemented in Phase 6
   (`c2cd353`); the actual install-to-bonded gate still needs a real Windows machine (see the
   Phase 6 row above).
2. Relay allow-list/authentication policy, handshake rate limiting/cookies, config file,
   systemd unit, firewall setup, one-line installer, and QR onboarding.
3. Signed artifacts, SBOM, reproducible-build work, dependency/security scanning, and a
   documented upgrade/rollback path.
4. Correct the repository default branch to `main` in GitHub settings if it still points at
   a Claude work branch.

### P2 - differentiating features

1. Tier 5 traffic classification and STREAM/CUSTOM modes.
2. Split tunnel/bypass rules and metered-link budgets.
3. Multi-socket-per-path experiments for high-BDP links.
4. PairBond and desktop share mode.
5. Full live diagnostics UI and accessible onboarding.

AI integration is intentionally deferred until the tunnel, lifecycle, and release gates are
dependable. Likely later uses include configuration guidance, anomaly explanation, scheduler
recommendations, and diagnostics summarization; AI must never sit in the packet fast path or
be required for basic connectivity.

## Working agreement for Claude and Codex

1. Start each task from current `main` and use a dedicated branch/worktree.
2. Claim explicit file ownership before concurrent write work. Do not edit the same files
   concurrently.
3. Update this document with:
   - what was found;
   - what changed;
   - exact commands/gates run;
   - what remains unverified.
4. Never mark a phase complete from code inspection or compilation alone.
5. Preserve BOND/1 compatibility unless a versioned protocol change is intentionally
   designed, documented, and tested at both endpoints.
6. Prefer small reviewable pull requests. Do not mix UI redesign, protocol changes, and
   lifecycle fixes into one unreviewable change.

## Verification log

Add concise entries here; link the pull request or commit when available.

- 2026-07-28 - Baseline repository inspection: latest Phase 5 CI run was green, but its
  Android evidence was build-only. Real-device Phase 5 remains open.
- 2026-07-28 - Android lifecycle stabilization started on
  `agent/android-path-lifecycle`; draft PR:
  https://github.com/chewtoo22-rgb/Bondify/pull/4
- 2026-07-28 - PR #4 CI: lint, Go race tests, all cross-builds, Android endpoint unit
  tests, gomobile AAR, and debug APK succeeded. Network Phases 1-3 succeeded. The newly
  enabled Phase 4 FEC sub-gate passed at 0.23% post-FEC loss; the path-death sub-gate
  failed because relay-side scheduling waited the full 10-second DEAD timeout before
  excluding the killed path.
- 2026-07-28 - Relay fast-degrade/authenticated-recovery fix and unit tests added to PR #4;
  GitHub Actions run 30372670159 passed lint, Go race tests, all cross-builds, Android unit
  tests/AAR/debug APK, and every network gate including Phase 4 FEC recovery and TCP
  survival after a path dies.
- 2026-07-28 - PR #4 squash-merged to `main` at `f02a98f`. ACK/SACK retransmission work
  started from that verified commit on `agent/ack-sack-retransmission`; implementation and
  the new FEC-off loss gate were added in PR #5.
- 2026-07-28 - PR #5 validation found and fixed an ACK storm on a persistent gap, an
  ACK-cadence concurrency race, overly aggressive retry feedback behind shaped queues,
  missing relay-side RTT knowledge, and a scheduler hot-path RTT lock/scan. CI run
  30408966633 passed race tests, lint, all cross-builds, the Android unit/AAR/APK job, and
  every Phase 1–4 network gate. Phase 3 measured 88.704 Mbps fast-path baseline and
  88.919 Mbps HoL-aware heterogeneous throughput; Phase 4 measured 0.03390% loss with FEC,
  0.71874% with FEC disabled and retransmission enabled, and completed the path-death TCP
  transfer at 302.696 Mbps.
- 2026-07-28 - PR #5 CI run 30409215021 re-verified the final scheduler RTT-cache head:
  race tests, lint, Linux/Windows/Android cross-builds, Android unit/AAR/APK, and every
  Phase 1–4 network gate passed. Phase 2 reached 85.375 Mbps; Phase 3 measured
  88.610 Mbps fast-path baseline, 88.902 Mbps heterogeneous HoL-aware throughput, and
  84.694 Mbps homogeneous HoL-aware versus 84.686 Mbps round-robin. Phase 4 measured
  0.04068% loss with FEC, 0.84757% with FEC disabled and retransmission enabled, and
  completed the path-death TCP transfer at 213.715 Mbps.
- 2026-07-28 - A documentation-only rerun (30409506517) exposed Phase 3 benchmark drift:
  HoL-aware correctly sent every measured packet on the 100 Mbps fast path with zero
  Bondify retries, but runner throughput fell from the baseline's 88.604 Mbps to
  75.093 Mbps during the four intervening benchmark scenarios. The harness now retains
  its initial sample as a drift signal and takes the acceptance baseline immediately
  before the HoL sample. The HoL threshold remains unchanged.
- 2026-07-28 - Run 30409751351 proved the adjacent baseline was stable at 88.603 Mbps
  while heterogeneous HoL-aware scheduling reached only 75.847 Mbps despite correctly
  selecting the fast path. Repeated path-view/tied-set allocations and a
  congestion-controller mutex were removed from the packet path, CWND reads became atomic,
  and allocation regression tests were added. Later same-scheduler controls showed this
  was useful hardening but not the complete throughput diagnosis.
- 2026-07-29 - Optimization runs caught two correctness regressions before merge: the
  immutable scheduler view was initially empty after the first-path handshake, and an
  unmeasured startup RTT tie was incorrectly cached. The view is seeded during handshake,
  unmeasured classifications are never reused, and race/unit regression tests cover both.
- 2026-07-29 - Run 30411286782 isolated the remaining Phase 3 failure: one-path
  round-robin and one-path HoL-aware both reached 88.60 Mbps, while the identical HoL-aware
  scheduler fell to 75.84 Mbps only after an idle 200 ms path joined. The one-path run
  recovered roughly 150 dropped outer packets; the multipath run recovered none because a
  blanket one-second retry grace suppressed same-path fast retransmission.
- 2026-07-29 - The retransmission queue now retains original path attribution and applies
  the 10 ms fast grace when the missing packet and SACKed successor share a path, while
  keeping one second for genuine cross-path/mixed/unknown evidence. GitHub Actions run
  30411651918 passed race tests, lint, all cross-builds, Android unit/AAR/APK, and every
  Phase 1–4 network gate. Phase 3 measured 88.607 Mbps heterogeneous HoL-aware versus an
  88.603 Mbps same-scheduler fast-only control, and 86.080 Mbps homogeneous HoL-aware
  versus 85.207 Mbps round-robin. Phase 4 measured 0.02712% loss with FEC, 0.86791% with
  FEC disabled and retransmission enabled, and completed the path-death TCP transfer at
  184.200 Mbps.
- 2026-07-31 - Baseline inspection of `main` at `6e46242`: Phases 0-4 verified as above;
  Phase 5 (Android) and Phase 6 (Windows, merged since in PR #7 at `c2cd353`) both build in
  CI but have no real-device gate evidence -- no session so far has had access to the
  physical hardware either requires. Started `claude/summary-next-phase-am0sp0` to close the
  top P0 backlog item (Android runtime path API) since it, unlike the hardware gates
  themselves, is fully implementable and testable without real hardware.
- 2026-07-31 - Implemented `ClientTunnel.AddPath`/`DropPath`, relay-side `PATH_DROP` handling,
  and the `mobile`/Kotlin bridge (`Tunnel.AddPathFD`/`DropPathLabel`,
  `BondifyVpnService.kt`'s `NetworkCallback` wired to them post-handshake) -- see "Runtime
  path API sprint" above for the full writeup, including a real pre-existing bug found and
  fixed along the way (one path's socket error tore down the whole tunnel). `go build ./...`,
  `go vet ./...`, and `go test ./... -race` all clean; new real-relay integration tests in
  `core/bond/client_runtime_path_test.go` and `relay_path_drop_test.go` pass under `-race`.
  `GOOS=android GOARCH=arm64 CGO_ENABLED=0 go build/vet ./mobile/...` clean. The Kotlin side
  is unverified locally (no Android SDK in this sandbox, same limitation every prior
  Android-touching session has had) and depends on CI's `android-app` job (gomobile bind +
  Gradle) for its first real compile.
