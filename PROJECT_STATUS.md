# Bondify Project Status

This is the shared handoff for Matt, Claude, Codex, and future contributors. Update it in
every pull request that changes a phase gate, closes a tracked gap, or discovers a new one.
The repository and captured test output are authoritative; a chat transcript is not.

Last updated: 2026-07-28

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
| Phase 4: resilience | REDUNDANT mode and adaptive Reed-Solomon FEC | CI passes the real-loss FEC and mid-transfer path-death gates | ACK/SACK retransmission from the original specification remains missing |
| Phase 5: Android | Kotlin app, VpnService shell, gomobile AAR build | APK compilation in CI | No real-device VPN, bonding, churn, or 30-minute screen-off gate has passed |
| Phase 6+: product | Specifications only or partial scaffolding | Not verified | Windows app, intelligence, sharing, installer, signed releases |

Do not describe Bondify as production-ready or independently audited. It is a substantial
pre-alpha networking implementation with important real-device and security work remaining.

## Current stabilization sprint

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

- Android cannot dynamically add a replacement socket to a running Go tunnel after
  `NetworkCallback.onLost`/`onAvailable`. This requires a thread-safe runtime `AddPath` API
  in `core/bond` and `mobile`, not reuse of the one-shot handshake builder.
- A handshake already blocked inside Go can take up to its current retry/deadline window to
  observe Android cancellation.
- Android client private keys still use ordinary private `SharedPreferences`; migrate them
  to Android Keystore-backed storage before public release.
- There is no automated Android protect-loop test yet. Compilation is not evidence that
  sockets bypass the TUN on a real device.
- No real Android device gate has passed.

## Priority backlog

### P0 - complete before calling Phase 5 done

1. Implement BOND/1 ACK/SACK packets and bounded retransmission of unacknowledged GSNs,
   especially when a path dies. The original Phase 4 specification includes this even
   though REDUNDANT/FEC landed without it.
2. Add a safe runtime path API across `core/bond` -> `mobile` -> Kotlin. Handle Android
   `onLost`, network replacement, NAT rebinding, and Wi-Fi/cellular return without killing
   the session.
3. Run the APK on a physical phone:
   - permission and one-path traffic smoke test;
   - Wi-Fi-only, cellular-only, and bonded throughput comparison;
   - kill and restore each path during one TCP transfer;
   - 30-minute screen-off/Doze survival;
   - reconnect after airplane mode and network roaming.
4. Add the protect-loop, MTU/MSS, fuzz, and full survivability gates specified by
   `HYDRA_Spec`/`ARCHITECTURE.md`.
5. Resolve Android key storage, log redaction, notification permission behavior, and
   foreground-service policy before distributing a release APK.

### P1 - desktop and deployability

1. Windows service/tray split using Wintun and `IP_UNICAST_IF`.
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
