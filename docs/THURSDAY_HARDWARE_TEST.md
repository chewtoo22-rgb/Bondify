# Thursday hardware validation runbook

This runbook separates what GitHub Actions can prove from what still requires physical Android, Windows/Wintun, Starlink, cellular, screen-off, carrier-NAT, and heterogeneous-WAN testing.

## Ground rules

- Do not mark a hardware row PASS unless it was exercised on the named physical target.
- Record Bondify commit SHA, app/build version, device/OS version, adapters, WAN type, relay location, and timestamp for every run.
- Save diagnostics before restarting or changing configuration after a failure.
- Treat cross-session traffic leakage, tunnel-IP reuse while still live, unauthenticated control acceptance, silent data corruption, or a crash loop as release blockers.
- CI success is a prerequisite, not a substitute for physical validation.

## Preflight

1. Confirm the exact commit under test is contained in `main` and its required GitHub Actions matrix is green.
2. Verify the Android APK or desktop archive came from that exact commit's workflow artifacts.
3. Record SHA-256 hashes of the artifacts being installed.
4. Start with one WAN path and a reachable relay before adding bonding complexity.
5. Enable Bondify diagnostics/log capture and verify timestamps are correct.

## Android install and lifecycle

- Install the CI-built Android APK on the target Android device.
- Launch from a clean install and confirm the app reaches its normal idle/configuration state.
- Grant only the permissions actually required by the build.
- Establish a single-WAN tunnel over Wi-Fi and confirm ordinary browsing plus a sustained TCP transfer.
- Repeat over cellular only.
- Move the app to background and return to foreground without dropping the session unexpectedly.
- Turn the screen off long enough to cross at least several keepalive intervals, then restore it and record whether the tunnel survived or recovered.
- Force-stop/relaunch and verify no stale session or tunnel-IP ownership remains on the relay.
- Capture Android version, OEM power-management settings, transport type, relay diagnostics, and logs for any failure.

## Windows and Wintun

- Install/run the Windows artifact from the exact tested commit.
- Verify Wintun adapter creation and cleanup across start/stop cycles.
- Confirm DNS and default-route behavior during the tunnel and after shutdown.
- Run a sustained TCP transfer and a UDP flow on one WAN.
- Restart Bondify repeatedly and verify no orphaned Wintun adapters or stale routes accumulate.
- Record Windows build, Wintun version, interface names, MTU, routes, DNS state, and diagnostics.

## Multi-WAN and heterogeneous-path tests

Run these only after the corresponding single-path cases are stable.

1. Wi-Fi + cellular on Android, or two genuinely independent Windows uplinks.
2. Verify both paths register and diagnostics show stable, deterministic path IDs/order.
3. Run sustained TCP and UDP traffic while both paths are active.
4. Drop one path physically and verify traffic continues over the surviving path within the expected recovery window.
5. Restore the dropped path and verify it rejoins without creating a duplicate live session or duplicate tunnel-IP lease.
6. Repeat with asymmetric bandwidth/latency so one path is materially slower than the other.
7. Where available, test Starlink plus a terrestrial/cellular WAN as the heterogeneous pair.

## NAT and rebinding

- Establish a session, then change the client's source tuple naturally by switching networks or forcing a NAT mapping change.
- Verify authenticated probe traffic updates the relay return path once the new tuple is proven.
- Confirm stale/replayed traffic cannot steer the return path.
- Record old/new source tuples from diagnostics where available.

## MTU and PMTU

- Record each physical interface MTU and Bondify's negotiated tunnel MTU.
- Verify normal browsing and sustained transfers with default MTU.
- Exercise a lower-MTU path where practical (for example VPN/hotspot/encapsulated uplink) and watch for stalls or black-hole behavior.
- Capture packet sizes, interface MTUs, path state, and relay/client diagnostics on failure.

## Relay/session resource checks

- Reconnect the same client repeatedly and verify only the current session owns its tunnel IP.
- Confirm stale session cleanup cannot release a live replacement session's lease.
- Exercise several clients if available and verify tunnel addresses remain unique.
- After clients disconnect, verify leases become reusable rather than leaking permanently.
- Watch relay memory, goroutine count, session count, path count, and logs during churn.

## Release blocker checklist

A candidate is blocked if any physical test shows:

- cross-client traffic or ownership leakage;
- duplicate simultaneously-live tunnel IPs;
- session/NAT state that can be redirected by replayed unauthenticated/stale traffic;
- persistent route/DNS/Wintun corruption after exit;
- repeatable crash loops;
- sustained-transfer corruption;
- unrecoverable path loss when a healthy alternate path exists;
- resource growth that does not settle after churn;
- hardware behavior that contradicts the release notes or configuration documentation.

## Evidence to keep

For every PASS or FAIL, keep:

- exact Git commit SHA;
- workflow run/artifact name and artifact SHA-256;
- device/OS or Windows build;
- WAN types and providers;
- relay version/location;
- Bondify configuration with secrets removed;
- deterministic client/relay diagnostics snapshots;
- relevant logs and timestamps;
- throughput/loss/latency observations;
- clear PASS/FAIL/NOT TESTED status.

Anything not physically exercised remains **NOT TESTED**, regardless of how persuasive the simulation or CI result looks.