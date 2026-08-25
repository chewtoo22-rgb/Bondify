# Bondify Release Readiness

This document separates what hosted CI can prove from what must be verified on real hardware before a public release.

## CI-backed gates

A release candidate should not ship unless the current commit is green for:

- Core CI: build, vet, lint, race-enabled unit tests, Android app build.
- Protocol Fuzz.
- Go Vulnerability Scan (`govulncheck`).
- Networking regression gates: phases 1, 2, 3, 4, and 7.
- PairBond gate.
- PMTU / WAN churn chaos gate.
- Long-duration Soak Churn gate.
- Relay Overload / backpressure gate.
- CodeQL.
- Release artifact checksum generation and provenance attestation.

## Support bundle

When collecting a support bundle from a running desktop client, use:

```bash
bash scripts/collect-support-bundle.sh
```

The collector intentionally uses only `/api/v1/diagnostics/redacted`, records platform/link-state metadata without interface addresses, sets a restrictive umask, and creates SHA-256 hashes for the collected files. Review the directory before sharing it. Never add private keys, raw full diagnostics, or packet captures by default.

## Real Android validation — required before release

Hosted CI cannot prove Android Keystore behavior, radio switching, OEM power-management behavior, or true Wi-Fi + cellular concurrency. Record device model, Android version, app build/commit, carrier, and test date for each run.

- Install an older build that still stores the Bondify client identity in legacy preferences, note the displayed public identity, then upgrade in place to the release candidate. Verify the public identity is unchanged and the tunnel connects.
- Force-stop/relaunch and reboot the phone. Verify the Keystore-backed identity remains usable and unchanged.
- Connect with both Wi-Fi and cellular available. Verify two paths join when the device/carrier permits simultaneous transports.
- While transferring TCP traffic, disable Wi-Fi and verify cellular carries the existing tunnel without an application-visible reset; restore Wi-Fi and verify the path can rejoin.
- Repeat by disrupting cellular while Wi-Fi remains available.
- Lock the screen for at least 30 minutes with the documented battery-optimization exemption applied. Verify the tunnel remains usable afterward.
- Revoke VPN permission from Android Settings. Verify the service tears down cleanly and reconnect requires normal consent.
- Generate a redacted diagnostics snapshot and inspect it manually for private keys, tokens, session identifiers, tunnel addresses, and public/WAN IP addresses before sharing.

## Real Windows validation — required before release

Hosted cross-compilation does not prove Wintun installation, routing changes, tray lifecycle, upgrade behavior, or coexistence with real adapters/VPN software.

- Test on a clean Windows 11 machine with no Bondify state. Install/run the candidate and establish a tunnel.
- Verify Wintun creation/configuration, DNS/default-route behavior, and clean restoration after disconnect/exit.
- Exercise at least two real uplinks when available (for example Ethernet + Wi-Fi or Wi-Fi + tethering) and verify path loss does not reset an active TCP flow.
- Run connect/disconnect cycles repeatedly and verify no stale routes, adapters, processes, or DNS configuration remain.
- Reboot while Bondify is disconnected and while configured for normal startup behavior; verify there is no broken network state.
- Exercise the tray UI lifecycle including connect, disconnect, exit, and relaunch.
- Generate a redacted support bundle and manually confirm it contains no secrets or network addresses that the redactor promises to remove.

## Heterogeneous WAN validation

Before a release intended for real WAN bonding, perform at least one test with genuinely different access networks (for example cable/fiber + cellular/Starlink), not two interfaces behind the same upstream router.

Record throughput, RTT, packet loss, path failover behavior, and whether either ISP uses CGNAT. Include one sustained transfer and one interactive workload during path churn.

## Release decision

A candidate is **CI-ready** when all automated gates are green. It is **release-ready** only after the Android and Windows hardware sections above have documented passes for the exact release commit. Any unavailable hardware gate stays explicitly `NOT TESTED`; it must never be converted into a pass based on simulation or hosted CI.
