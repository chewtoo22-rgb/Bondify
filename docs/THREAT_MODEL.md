# Bondify Threat Model

This document records the security assumptions that automated tests and reviews are expected to preserve. It is intentionally narrower than a formal external audit.

## Assets

Bondify must protect:

- confidentiality and integrity of tunneled user traffic;
- client and relay private keys;
- authenticated session/control state;
- tunnel IP lease correctness;
- host routing and VPN escape boundaries;
- relay availability under untrusted Internet traffic;
- release artifact integrity and provenance;
- support diagnostics from leaking secrets or identifying network state.

## Adversaries

### Unauthenticated Internet attacker

Can send arbitrary UDP datagrams to the relay, rotate/spoof source addresses where the network permits it, replay captured outer packets, and attempt parser/CPU/session exhaustion.

Expected controls include bounded parsing, pre-Noise global/per-source handshake budgets, authenticated session creation, replay protection, and bounded relay state.

### Authenticated but malicious client

Owns valid client key material and can send authenticated but pathological control/data/FEC traffic. It may intentionally choose strange packet ordering, retransmission behavior, FEC metadata, path churn, or reconnect patterns.

Expected controls include path identity consistency, replay windows, bounded queues/FEC/reorder state, session expiry/reclamation, lease validation, and resource ceilings that do not depend solely on authentication.

### On-path network attacker

Can drop, delay, duplicate, reorder, or replay packets and can create PMTU black holes or NAT tuple changes. Without endpoint keys it must not be able to modify authenticated traffic undetected or inject valid control operations.

Expected controls include AEAD authentication, replay protection, retransmission/FEC logic, NAT-rebinding authentication, and conservative MTU/PMTU behavior.

### Compromised client host

A fully compromised endpoint can read its own plaintext traffic and client credentials. Bondify does not claim to protect data from the operating system that terminates the tunnel.

Platform key stores and least-privilege routing still reduce accidental exposure but are not a defense against a fully compromised kernel/root context.

### Compromised relay host

The relay terminates Bondify session cryptography and therefore is inside the trust boundary for tunneled traffic. Bondify is not an end-to-end application-layer encryption system against its own relay operator. Applications requiring relay-blind confidentiality must provide their own end-to-end encryption (for example TLS).

## Primary attack surfaces

### Handshake path

Risk: CPU exhaustion, session-table growth, malformed Noise traffic.

Required properties:

- rate limits are applied before expensive Noise responder work;
- both per-source and aggregate budgets exist;
- invalid handshakes do not create durable session/lease state.

### Session and tunnel-IP lifecycle

Risk: permanent resource exhaustion or lease corruption.

Required properties:

- inactive/dead sessions are reclaimed;
- IP leases are returned exactly once;
- duplicate/foreign lease releases cannot poison the free list;
- reconnect cleanup cannot race and release a replacement session's lease.

### Multipath control plane

Risk: path identity confusion, replay, stale-path scheduling, unauthorized path add/drop.

Required properties:

- authenticated nonce/path identity matches decrypted payload identity;
- duplicate runtime path IDs are rejected;
- dropped paths leave the schedulable set promptly;
- initial configured path count cannot exceed the 8-bit wire identity space.

The last property is tracked separately until the client configuration guard is merged.

### Data plane and recovery machinery

Risk: attacker-driven memory growth or work amplification via FEC, reorder, ACK/SACK, retransmit, pacing, or queues.

Required properties:

- wire-supplied geometry and indices are bounded to protocol-supported values;
- completed state is retired promptly;
- queues have hard capacities;
- overload is observable rather than silently unbounded;
- loss recovery cannot create unlimited retained packet state.

### Diagnostics

Risk: support artifacts leak keys, session identifiers, addresses, or decrypted user traffic.

Required properties:

- support-facing diagnostics use the redacted path;
- private keys and credentials are never serialized;
- redaction tests fail if representative secret/address values survive.

### Release pipeline

Risk: debug/unsigned binaries are mistaken for production releases, artifacts are replaced, or publish credentials are over-privileged.

Required properties:

- debug Android APKs are not release assets;
- checksums are verified before publish;
- provenance/attestation is generated where supported;
- workflow permissions are least privilege;
- signing status is explicit rather than implied.

## Out of scope / non-claims

Bondify does not currently claim:

- resistance to a fully compromised client or relay operating system;
- anonymity against the relay or network provider;
- censorship resistance;
- post-quantum cryptography;
- physical-device correctness proven solely by hosted CI;
- production security certification or an independent external audit.

## Validation mapping

Automated coverage currently includes unit/race tests, CodeQL, `govulncheck`, protocol fuzzing, PairBond tests, relay-overload/backpressure gates, MTU/PMTU and path-flap chaos gates, randomized WAN churn, and longer soak testing.

Physical Android, Windows/Wintun, screen-off, cellular/Starlink, and heterogeneous-WAN behavior remains a separate hardware-required release boundary documented in `RELEASE_READINESS.md`.