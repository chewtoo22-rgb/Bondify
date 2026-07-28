# BOND/1 — Bondify Wire Protocol Specification

**Version 1. Status: normative.** This document defines the wire format, session and path
lifecycle, and control semantics for Bondify's bonding transport. It is the contract every
other component depends on. Changes here are sequential and reviewed; no component may
modify this format unilaterally.

See `ARCHITECTURE.md` for domain theory, scheduler algorithms, and platform guidance.

---

## 1. Design goals

1. Run entirely in userspace on unmodified Android and Windows.
2. Carry arbitrary IP packets (layer 3 tunnel), not just TCP.
3. Distribute a single flow across N paths and restore order at the far end.
4. Survive path death, path birth, NAT rebinding, and IP change without dropping the session.
5. Support duplication, FEC, and pure aggregation on a per-packet basis.
6. Be transportable over plain UDP, over TCP, or shaped as TLS for hostile networks.
7. Impose fixed, small per-packet overhead.

## 2. Terminology

| Term | Meaning |
|---|---|
| **Session** | One client-to-relay tunnel. Identified by the client's Noise static public key. Owns exactly one tunnel IP. |
| **Path** | One UDP 4-tuple between a specific client uplink and the relay. A session has 1..255 paths. |
| **Path ID** | 8-bit identifier, client-assigned, unique within a session. |
| **GSN** | Global Sequence Number. 64-bit, monotonic, per direction, per session. Assigned before path selection. |
| **PSN** | Path Sequence Number. 32-bit, monotonic, per path, per direction. Used for per-path loss measurement. |
| **Generation** | A group of K data packets over which FEC parity is computed. |

The GSN/PSN split is the single most important structural decision. GSN restores application order; PSN measures path health. Conflating them makes loss attribution impossible.

## 3. Cryptography

- Handshake: **Noise_IK_25519_ChaChaPoly_BLAKE2s**. Reuse the implementation from `wireguard-go` rather than writing new crypto.
- IK is chosen because the client knows the relay's static public key in advance (it is in the config), giving 1-RTT establishment with identity hiding for the client.
- Data packets: ChaCha20-Poly1305 by default; AES-256-GCM when the platform reports hardware AES support. Negotiated in the handshake.
- Nonce: 96-bit, constructed as `path_id (8 bits) || counter (88 bits)`. **Never reuse a nonce across paths.** Embedding the path ID in the nonce makes reuse structurally impossible.
- Rekey after 2^60 messages or 120 seconds, whichever first, matching WireGuard's discipline.
- Replay protection: sliding window of 8192 on GSN, per direction. This also performs duplicate suppression for REDUNDANT mode at zero extra cost.

**Rule: authenticate first, parse second.** Never act on any header field before AEAD verification succeeds. Silently drop packets that fail authentication; never respond, never log at high rate.

> **Implementation note (deviation, documented here per the protocol's own review discipline):** `golang.zx2c4.com/wireguard` does not export its Noise handshake state machine as a reusable public API — it is private to the `device` package and tightly coupled to full WireGuard session semantics. `core/crypto` therefore implements this handshake with `github.com/flynn/noise`, a generic, widely used implementation of the Noise Protocol Framework, configured for the exact pattern specified here: IK, Curve25519 DH, ChaChaPoly AEAD, BLAKE2s hash. No custom cryptographic primitives are written; only composition of audited building blocks, matching the spirit of "reuse, don't invent" even though the letter (literally the wireguard-go package) was not achievable. AES-256-GCM hardware-accelerated fallback is implemented via `crypto/aes` + `crypto/cipher` (Go stdlib, constant-time, hardware-accelerated via AES-NI/ARMv8 crypto extensions transparently).

## 4. Packet format

All integers are big-endian (network order).

### Outer datagram

```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|  Type (8)     |  Version (8)  |         Reserved (16)         |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                     Session Index (32)                        |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                                                               |
+                        Nonce (96)                             +
|                                                               |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                                                               |
+              Encrypted Payload (variable)                     +
|                                                               |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                     Auth Tag (128)                            |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

Outer header: 16 bytes. Auth tag: 16 bytes. Fixed overhead before inner header: **32 bytes**.

`Session Index` is a random 32-bit value assigned by the relay at handshake completion. It allows O(1) session lookup without trial decryption and is what makes NAT rebinding cheap: a packet arriving from a new source address with a known session index and valid AEAD is simply a path that moved.

### Type values

| Value | Name | Direction | Purpose |
|---|---|---|---|
| 0x01 | `HANDSHAKE_INIT` | C→R | Noise IK initiator message |
| 0x02 | `HANDSHAKE_RESP` | R→C | Noise IK responder message |
| 0x10 | `DATA` | both | Tunnelled IP packet |
| 0x11 | `FEC` | both | Parity packet for a generation |
| 0x20 | `PROBE` | both | Path measurement request |
| 0x21 | `PROBE_ACK` | both | Path measurement response |
| 0x30 | `ACK` | both | Cumulative + selective acknowledgement |
| 0x40 | `PATH_ADD` | C→R | Register a new path in an existing session |
| 0x41 | `PATH_DROP` | both | Retire a path |
| 0x50 | `CTRL` | both | Control channel (config, stats, MTU, mode) |
| 0x60 | `KEEPALIVE` | both | NAT pinhole maintenance |

### Inner DATA header (inside AEAD)

```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                                                               |
+                   Global Sequence Number (64)                 +
|                                                               |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                 Path Sequence Number (32)                     |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|   Path ID (8) |   Flags (8)   |      Payload Length (16)      |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                    Generation ID (16)         | Gen Index (8) |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|              Inner IP packet (variable)                       |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

Inner header: 19 bytes, padded to 20. **Total Bondify overhead per data packet: 52 bytes.**

Therefore: `tunnel_MTU = min(path_PMTU across active paths) - 52`. For a typical 1500-byte path this yields 1448. For paths behind PPPoE (1492) it yields 1440. Always compute from the *minimum*, and recompute whenever a path joins.

### DATA flags

| Bit | Name | Meaning |
|---|---|---|
| 0 | `DUP` | This packet is a duplicate copy sent on another path |
| 1 | `RTX` | Retransmission of a previously sent GSN |
| 2 | `LATENCY` | Latency-sensitive class; bypass reorder deadline extension |
| 3 | `BULK` | Bulk class; eligible for aggressive spreading |
| 4 | `FEC_PROTECTED` | Member of a FEC generation |
| 5 | `PUSH` | Release from reorder buffer immediately on arrival |
| 6-7 | reserved | Must be zero |

## 5. Session and path lifecycle

### Establishment

```
Client                                       Relay
  |                                            |
  |-- HANDSHAKE_INIT (Noise IK, path 0) ------>|
  |                                            | derive keys, alloc Session Index
  |<-- HANDSHAKE_RESP (session idx, cfg) ------|
  |                                            |
  |== path 0 established, tunnel IP assigned ==|
  |                                            |
  |-- PATH_ADD (path 1) from uplink 2 -------->|  authenticated with session keys
  |<-- CTRL ack -------------------------------|
  |-- PATH_ADD (path 2) from uplink 3 -------->|
  |<-- CTRL ack -------------------------------|
```

Only path 0 performs a Noise handshake. Additional paths are authenticated by the session keys, which is why `PATH_ADD` must be replay-protected — it carries a unique GSN like any other packet.

### Path states

```
        +---------+  PATH_ADD acked   +--------+
        | JOINING |------------------>| ACTIVE |
        +---------+                   +--------+
             |                          |    ^
             | timeout                  |    | 3 consecutive PROBE_ACK
             v                     loss/RTT  | within threshold
        +---------+   spike or 3 missed      |
        |  DEAD   |<---- probes -------- +-----------+
        +---------+                      | DEGRADED  |
             ^                           +-----------+
             |  10s no PROBE_ACK               |
             +---------------------------------+
```

- **JOINING** — PATH_ADD sent, awaiting confirmation. No data scheduled.
- **ACTIVE** — eligible for scheduling per its role (BOND / BACKUP / DISABLED).
- **DEGRADED** — still alive but excluded from BOND scheduling; probes continue. Enter on sustained loss > 15% or RTT > 3x session median. Leave after 3 consecutive healthy probes. Hysteresis is mandatory; flapping paths are worse than dead ones.
- **DEAD** — removed from scheduling. Unacked packets assigned to it are immediately re-queued to other paths. The session survives.

**Session survives while at least one path is ACTIVE or DEGRADED.** Losing every path pauses but does not tear down the session; the client retries `PATH_ADD` with backoff for `session_grace` (default 60 s) before giving up. This is what makes a phone survive a tunnel walk between Wi-Fi and cellular.

### NAT rebinding

A DATA packet arriving at the relay with a known Session Index and valid AEAD, but from a source address differing from the recorded one for that Path ID, updates the recorded address immediately. No renegotiation. Rate-limit updates to at most one per path per second to blunt off-path spoofing, and only accept updates from packets whose GSN passes the replay window.

## 6. Probing and measurement

Every path sends a `PROBE` every **200 ms** while active, backing off to 1 s after 30 s of zero data. `PROBE` carries the sender's monotonic timestamp and its cumulative sent-packet counter for that path. `PROBE_ACK` echoes both plus the responder's received counter.

Derived per path:

| Metric | Derivation |
|---|---|
| `rtt_srtt` | EWMA, alpha = 0.125 (RFC 6298 convention) |
| `rtt_var` | EWMA of deviation, beta = 0.25 |
| `rtt_min` | Windowed minimum over 10 s. Use for scheduling, not srtt. |
| `loss` | 1 − (delta received counter / delta sent counter), EWMA alpha = 0.2 |
| `goodput` | Bytes ACKed per second, EWMA alpha = 0.1 |
| `jitter` | Mean absolute deviation of inter-arrival delta |
| `inflight` | Bytes sent minus bytes ACKed |
| `cwnd` | Per-path congestion window (see §3.5) |

**Use `rtt_min`, not `rtt_srtt`, for scheduling decisions.** Smoothed RTT includes self-induced queueing delay; scheduling on it creates a feedback loop where a path that you load appears slow, so you unload it, so it appears fast, so you load it. Oscillation. Minimum RTT is a stable estimate of the path's true propagation delay.

## 7. Acknowledgement

`ACK` packets carry:

- 64-bit cumulative GSN (all GSNs at or below this are received).
- A SACK-style block list, up to 32 ranges, of received GSNs above the cumulative point.
- Per-path received counters for every known path ID, enabling loss attribution.
- The receiver's current reorder buffer occupancy and configured deadline, so the sender can adapt.

ACKs are sent on the **lowest-RTT ACTIVE path**, not on the path data arrived on. ACK loss is far more damaging than data loss; give ACKs the best path. Send an ACK every 8 received data packets or every 20 ms, whichever comes first.

The BOND/1 v1 implementation encodes the authenticated ACK body as CBOR with these keys:

| Key | Type | Meaning |
|---|---|---|
| `has` | bool | Whether a cumulative GSN exists yet. False represents the initial gap before GSN 0. |
| `cum` | uint64 | Highest contiguously received GSN; meaningful only when `has` is true. |
| `sack` | array | Up to 32 inclusive `{s, e}` GSN ranges above `cum`. |
| `paths` | array | `{pid, recv}` received-packet counters for known paths. |
| `rbuf` | uint32 | Current reorder-buffer occupancy in bytes. |
| `rms` | uint16 | Current reorder deadline in milliseconds. |

Detecting a gap sends an ACK immediately rather than waiting for the delayed-ACK limit.
An unacknowledged GSN becomes eligible for fast retransmission after three ACKs report a
SACKed successor, preventing ordinary multipath reordering from being mistaken for loss.
A short 10 ms grace period then lets an in-flight FEC shard repair it first. Every
retransmission uses a fresh path PSN and AEAD nonce and sets `RTX`; it is not inserted back
into the original FEC generation.

Senders retain at most 4096 packets / 8 MiB, retransmit at most 64 packets per maintenance
tick, and abandon a packet after three retries. Timeout retransmission uses twice the
lowest active path's minimum RTT, clamped to 100 ms–1 s (200 ms while unmeasured). These
bounds are part of the denial-of-service surface and must not be removed without replacing
them with equally strict accounting.

## 8. Transport fallbacks

The same BOND/1 framing is carried over three transports, selected per path and switchable at runtime without dropping the session:

1. **UDP (default).** Lowest overhead, best performance.
2. **TCP.** For networks that block or heavily police UDP. Length-prefix each datagram with a 16-bit length. Accept the TCP-over-TCP penalty; it beats no connectivity. Disable Nagle.
3. **TLS-shaped.** Wrap the TCP transport in real TLS to port 443 with a plausible SNI. For censored networks. Never claim this defeats sophisticated DPI.

Fallback is per-path and automatic: a path that fails to complete `PATH_ADD` over UDP within 3 attempts retries over TCP, then TLS. Record which transport succeeded and try it first next time on the same network SSID/carrier.

## 9. Control channel

`CTRL` carries length-prefixed CBOR (compact, schema-light, good library support in Go and Kotlin). Message kinds:

| Kind | Direction | Contents |
|---|---|---|
| `cfg_push` | R→C | Tunnel IP, DNS, MTU ceiling, keepalive interval |
| `mode_set` | C→R | SPEED / REDUNDANT / STREAM / CUSTOM |
| `path_role` | C→R | Per-path role: BOND / BACKUP / DISABLED, weight hint |
| `mtu_report` | both | Discovered PMTU per path |
| `stats` | both | Periodic counters for the diagnostics UI |
| `fec_set` | C→R | FEC policy: off / fixed / adaptive, with bounds |
| `bye` | both | Graceful teardown |

Version negotiation: the handshake exchanges a protocol version byte. Relay and client must agree on major version; minor versions must be forwards-compatible via unknown-kind skipping. **Unknown CTRL kinds are ignored, never fatal.**

## Appendix — Default constants

| Constant | Default | Notes |
|---|---|---|
| `PROBE_INTERVAL` | 200 ms | 1 s after 30 s idle |
| `PROBE_TIMEOUT` | 3 missed | → DEGRADED |
| `PATH_DEAD_TIMEOUT` | 10 s | no PROBE_ACK |
| `SESSION_GRACE` | 60 s | all paths down before teardown |
| `REORDER_DEADLINE_MIN` | 20 ms | |
| `REORDER_DEADLINE_MAX` | 400 ms | |
| `REORDER_DEADLINE_FACTOR` | 1.5 | × RTT spread |
| `REORDER_BUF_MIN` | 256 KB | |
| `REORDER_BUF_MAX` | 16 MB | |
| `FEC_K` | 10 | data shards per generation |
| `FEC_MAX_REDUNDANCY` | 0.25 | |
| `FEC_GEN_TIMEOUT` | 30 ms | close partial generation |
| `DUP_FACTOR` | 2 | REDUNDANT mode |
| `HEADROOM` | 0.90 | max path utilisation for bulk |
| `DEGRADE_LOSS` | 0.15 | → DEGRADED |
| `DEGRADE_RTT_MULT` | 3.0 | × session median |
| `REKEY_TIME` | 120 s | |
| `REPLAY_WINDOW` | 8192 | |
| `KEEPALIVE` | 15 s | NAT pinhole |
| `MAX_PATHS` | 255 | protocol limit; no artificial cap |
| `HEADER_OVERHEAD` | 52 bytes | outer 16 + inner 20 + tag 16 |
| `PMTU_FLOOR` | 1200 bytes | |
| `BBR_GAIN` | 2.0 | |
