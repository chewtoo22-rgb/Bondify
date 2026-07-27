# Licensing

HYDRA uses a split license, deliberately: the relay is copyleft to keep hosted forks
contributing back; the client core is permissive so it can be embedded anywhere (including
proprietary router firmware) without friction, because the client is the thing that has to
run on a stock, unrooted device and we want zero adoption barriers.

| Path | License | Rationale |
|---|---|---|
| `relay/` | AGPLv3 (`relay/LICENSE`) | A modified relay run as a network service must publish its source. Prevents a silent proprietary-relay fork. |
| `core/`, `android/`, `desktop/`, `testbed/`, everything else | Apache-2.0 (`LICENSE`) | Client-side code is free to embed, including commercially, without copyleft obligations. |

`core/` is compiled into both the AGPLv3 relay binary and the Apache-2.0 clients. Apache-2.0
is compatible with AGPLv3-as-an-aggregate (the relay binary as a whole is distributed under
AGPLv3 terms), so this composition is not a conflict — but if you're re-licensing or
sublicensing any part of this project, keep that origin straight: `core/` itself stays
Apache-2.0 regardless of what links against it.

No CLA, no dual-licensing games, no relicensing without every contributor's consent.
