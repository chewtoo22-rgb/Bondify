# Security Policy

Bondify is pre-release networking software and has not yet received an independent security audit. Do not treat it as production-hardened until that changes.

## Reporting a vulnerability

Please report suspected vulnerabilities privately through GitHub's security-advisory mechanism for this repository rather than opening a public issue when the report could expose users, keys, protocol weaknesses, or a practical exploit.

Include the affected commit/version, platform, reproduction steps, impact, and any logs or packet captures that can be shared safely. Remove private keys, credentials, public IPs that should remain private, and decrypted user traffic before attaching diagnostics.

## Security-sensitive areas

Changes touching the BOND/1 wire protocol, Noise session establishment, AEAD nonces, replay protection, key storage, PairBond trust/revoke behavior, relay authentication, route protection, TUN bypass, or diagnostic redaction require explicit security-focused review and regression tests.

## Current release posture

Before a stable public release, Bondify should complete all of the following:

- external protocol/cryptography review;
- Android Keystore-backed client-key protection;
- real-device route/protect-loop validation;
- signed release artifacts when a signing identity is available;
- confirmation that diagnostics and crash output never expose private keys or decrypted tunnel payloads.
