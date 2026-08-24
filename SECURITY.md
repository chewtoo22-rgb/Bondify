# Security Policy

Bondify is pre-release networking software. It has extensive automated protocol, fuzzing, abuse-resistance, and network-chaos coverage, but it has not yet received an independent external security audit. Do not represent it as production-audited software until that review exists.

## Reporting a vulnerability

Please report suspected vulnerabilities privately through GitHub's private vulnerability-reporting / Security Advisory mechanism for this repository when available. Do not publish working exploit details, private keys, credentials, session material, or sensitive diagnostic bundles in a public issue.

Include the affected commit/version, platform, minimal reproduction, attacker prerequisites, expected vs. observed behavior, and impact. State whether exploitation requires an authenticated Bondify client/session. Remove credentials, private keys, decrypted user traffic, and identifying network addresses before attaching logs or captures.

If private reporting is unavailable, open a public issue with only a high-level description and request a private contact path.

## Security-sensitive areas

Changes touching these areas require security-focused review and regression coverage:

- BOND/1 wire parsing and authenticated control messages;
- Noise session establishment and pre-handshake abuse limiting;
- AEAD nonce/path identity consistency and replay protection;
- client/relay key storage and PairBond trust/revoke behavior;
- relay session lifetime, tunnel-IP lease reclamation, and global/per-peer resource limits;
- FEC, reorder, retransmit, queue, and pacing bounds for authenticated-but-malicious peers;
- TUN routing, socket protection, route cleanup, and VPN bypass prevention;
- diagnostic/support-bundle redaction, listener exposure, and browser-origin policy;
- release checksums, provenance/attestation, signing state, and publish guards.

## Current implemented hardening

Current `main` includes, among other controls:

- Android client identity wrapping with Android Keystore and plaintext migration;
- per-source plus global relay handshake rate limiting before expensive Noise responder work;
- authenticated NAT-rebinding coverage and control-plane path-ID consistency checks;
- bounded/reclaimed relay sessions and reusable tunnel-IP leases;
- bounded initial and runtime path identity handling within the 8-bit wire-ID space;
- bounded receive-side FEC geometry and immediate retirement of completed generations;
- strict BOND/1 version/reserved-field checks and deterministic CBOR wire decoding;
- atomic replay admission for concurrent duplicate ciphertexts;
- fail-closed relay key loading plus exclusive first-run key creation and permission checks;
- parser fuzzing plus CodeQL and `govulncheck` in CI;
- deterministic MTU/PMTU, path-flap, WAN-churn, soak, PairBond, and relay-overload regression gates;
- loopback-only diagnostics, loopback-origin browser CORS, and redacted diagnostics/support bundles;
- release checksums, provenance attestations, least-privilege publishing, and a guard preventing debug Android APKs from being published as production releases.

These controls reduce risk; they are not a substitute for an independent review.

## Hardware-required trust boundary

A green hosted CI run does **not** prove physical-device behavior. The following remain real-hardware release gates until explicitly tested and recorded as such in `RELEASE_READINESS.md`:

- Android Keystore upgrade/migration behavior on device;
- Android Wi-Fi/cellular failover and `VpnService.protect()` behavior;
- Android screen-off/background survival and permission revocation;
- Windows/Wintun routing, interface cleanup, tray lifecycle, and reboot behavior;
- heterogeneous physical WAN behavior such as cable/fiber + Starlink/cellular.

Never mark those gates passed from namespace simulation or hosted CI alone.

## Secrets and diagnostics

Never commit or attach private keys, signing credentials, access tokens, raw decrypted tunnel traffic, or unreviewed diagnostics containing session identifiers or identifying addresses. Use Bondify's redacted diagnostic/support-bundle path for support cases, and still review the resulting bundle before sharing it.

The full diagnostics server is intentionally unauthenticated but is enforced as loopback-only. Browser CORS is restricted to loopback HTTP(S) origins. Any future change that makes diagnostics remotely reachable must add an explicit authentication and transport-security design rather than weakening those invariants.

## Release artifacts

Only artifacts produced by the repository's release workflow and explicitly intended for release should be treated as publishable binaries. Android debug APKs are CI-only until a real production signing path exists.

Before publishing a release, verify that required CI/security/network workflows are green, checksums verify, provenance/attestation is present where supported, signing status is explicit, and hardware-required gates are not falsely marked complete.

## High-value report scope

Reports are especially valuable for authentication bypasses, nonce/key misuse, replay acceptance, control-plane identity confusion, remote crashes, CPU/memory/session exhaustion, tunnel traffic escape, privilege-boundary failures, release/signing compromise, and diagnostic secret leakage.
