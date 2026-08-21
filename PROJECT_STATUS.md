# Bondify Project Status

Last updated: 2026-08-21

This file is the current engineering handoff for Bondify. The repository, CI results, release artifacts, and real-device test evidence are authoritative; chat transcripts and historical development branches are not.

## Product identity

The product is **Bondify**. The wire protocol is **BOND/1**. Historical working names and development-agent names are not product branding and should not appear in current product-facing documentation, UI, artifacts, installers, or release metadata.

## Current state

Bondify is a substantial pre-release multi-WAN bonding implementation. It is **not yet production-ready or independently security-audited**. Core networking, resilience, classification, desktop/Android builds, and PairBond now have automated coverage, while real-device acceptance remains the largest outstanding risk.

| Area | Implemented | Verified | Remaining gate |
|---|---|---|---|
| Phase 0: foundation | Repository, BOND/1 docs, licensing, CI | Core CI continuously green | Keep required checks green; external security review before stable release |
| Phase 1: one-path tunnel | Linux client, relay, Noise tunnel, NAT | Netns ping/HTTP/encryption/throughput gates | Release-quality installation and real deployment soak |
| Phase 2: multipath | PATH_ADD/PATH_DROP, probing, GSN reorder, round-robin | Two-path aggregation gate | Broader churn/flapping and heterogeneous real-network coverage |
| Phase 3: scheduling | Weighted-goodput, min-RTT/cwnd, HoL-aware schedulers | Unit tests and shaped benchmark gates | Improve relay-side delivery-rate/pacing model |
| Phase 4: resilience | REDUNDANT mode, adaptive Reed-Solomon FEC, ACK/SACK, bounded retransmission, path-death recovery | Race/unit tests plus real-loss/FEC/retransmission/path-death gates | Real-device/path-flapping breadth and external security review |
| Phase 5: Android | Kotlin app, VpnService, gomobile AAR, runtime path churn | APK/unit build in CI; Go runtime-path integration tests | Real Android Wi-Fi+cellular bonding, protect-loop proof, handover, and 30-minute screen-off/Doze gate |
| Phase 6: Windows | Wintun client, tray support, egress binding, installer | Windows build/vet/cross-compile in CI | Real Windows install-to-bonded test, route runtime validation, one-UAC-prompt target |
| Phase 7: classification/budgets/split tunnel | Tiered routing, BULK pacing/headroom, diagnostics, desktop bypass routes | Privileged mixed-traffic gate passes; loaded RTT remains within gate | Windows route runtime and Android split-tunnel real-device validation |
| Phase 8: PairBond/share mode | Explicit pairing codes, hardened registry, opaque ciphertext peer proxy, runtime peer-path integration, immediate revoke | Dedicated PairBond aggregation + revoke-survival workflow passes | Real phone-to-host contribution test, UX/pairing UI, NAT/roaming breadth |
| Release pipeline | Windows/Linux/relay packages, Android APK, SHA-256 checksums, prerelease workflow | Build configuration present; CI builds green | Signed release artifacts, reproducible release procedure, hardware RC validation |

## Phase 8 acceptance status

PairBond is merged into `main`. The implementation deliberately forwards already-encrypted Bondify packets: the contributing peer does not receive Bondify session plaintext or relay session keys.

Automated acceptance now covers:

- explicit short-lived single-use pairing primitives;
- constant-time pairing-code comparison across active entries;
- bounded paired-peer registry with safe value snapshots and immediate revoke;
- host-to-peer-to-relay opaque UDP forwarding and return traffic;
- runtime attachment of a PairBond peer through Bondify's normal `AddPath` API;
- immediate `DropPath`/authenticated `PATH_DROP` on revoke;
- a shaped aggregation gate proving a contributed peer path adds useful capacity;
- a mid-transfer revoke gate proving the session survives on the remaining direct path.

The dedicated Phase 8 PairBond workflow, full CI workflow, and Android APK workflow were green before merge of PR #11.

## Highest-priority remaining work

### P0 — real-device validation

1. **Android Wi-Fi + cellular bonding:** establish a real VPN session with both transports, verify both carry encrypted Bondify traffic, then kill/recover each transport during sustained transfer.
2. **Android screen-off/Doze:** sustain a bonded transfer or controlled keepalive workload for at least 30 minutes with screen off; verify no routing loop, dead VPN, or unrecoverable path state.
3. **PairBond real-device test:** contribute a phone's cellular path to a host over LAN/Wi-Fi, measure throughput delta, revoke mid-transfer, and verify immediate fallback.
4. **Windows install/runtime:** clean machine -> installer -> Wintun -> bonded session with one elevation flow; validate route cleanup after exit/crash.
5. **Real dual-WAN host:** validate heterogeneous WANs with realistic RTT/bandwidth asymmetry and forced path loss/recovery.

### P0 — security/release hardening

- Move Android client private-key storage to Android Keystore-backed protection before public stable release.
- Perform an external protocol/crypto/data-plane security review; do not claim independent audit before one occurs.
- Sign Windows and Android release artifacts when a release identity is available.
- Define a key-rotation/revocation story for long-lived client identities.
- Treat diagnostics as local-only by default and verify no secrets/private keys are exposed.

### P1 — networking quality

- Feed authenticated delivery-rate/path counters into the controller instead of relying primarily on simplified probe-fed estimates.
- Add long-duration churn/flapping tests and asymmetric-loss/variable-RTT cases.
- Add IPv6 end-to-end data-plane gates, not only address parsing/support scaffolding.
- Add MTU/PMTU stress cases and black-hole detection.
- Add relay overload/backpressure tests with many concurrent sessions.

### P1 — product polish

- PairBond host/peer UI with explicit trust/revoke state and clear path contribution metrics.
- First-run setup that can validate relay reachability, tunnel permissions, and available uplinks before connecting.
- Exportable diagnostic bundle with secrets redacted.
- Stable semantic versioning and changelog/release notes discipline.

## Engineering rules

- Never claim a platform or feature is verified unless a corresponding automated gate or named real-device test has passed.
- No auto-trust or silent discovery for PairBond peers.
- Never log or expose private keys or decrypted tunnel payloads.
- A single physical path failure must not tear down a healthy multipath session.
- Dropped/revoked paths must leave both local and relay scheduling promptly.
- New protocol fields require backward/forward compatibility consideration and deterministic tests.
- Product-facing content must use **Bondify** branding only.

## Next milestone

**Hardware Release Candidate:** produce fresh Windows and Android artifacts from merged `main`, run the P0 real-device matrix, fix every reproducible failure, then cut the first hardware-validated prerelease tag.
