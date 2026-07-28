#!/usr/bin/env bash
# Builds mobile/'s gomobile-bindable Go package into an Android AAR
# (android/app/libs/bondifymobile.aar), which android/app/build.gradle.kts depends on and
# invokes automatically via its generateMobileAar task -- this script is not normally run by
# hand, but is kept standalone (rather than inlined into Gradle) so it can also be run
# directly for local iteration without going through a full Gradle build.
#
# Requires: Go 1.25+ (matching go.mod's `go` directive, since gomobile bind is registered as
# a tool dependency there), the Android SDK (ANDROID_HOME) with platform 34 and build-tools
# installed, and the Android NDK (ANDROID_NDK_HOME) for gomobile's cgo cross-compilation.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

: "${ANDROID_HOME:?ANDROID_HOME must point at an Android SDK (platform 34 + build-tools installed)}"
: "${ANDROID_NDK_HOME:?ANDROID_NDK_HOME must point at an installed Android NDK (gomobile bind needs it for cgo)}"

OUT=android/app/libs/bondifymobile.aar
mkdir -p android/app/libs

GOBIN="$(go env GOPATH)/bin"
export PATH="$GOBIN:$PATH"
if ! command -v gomobile >/dev/null 2>&1; then
	echo "[mobile] installing gomobile/gobind" >&2
	go install golang.org/x/mobile/cmd/gomobile@latest
	go install golang.org/x/mobile/cmd/gobind@latest
fi

echo "[mobile] gomobile bind -> $OUT" >&2
gomobile bind -target=android -androidapi 24 -o "$OUT" ./mobile
echo "[mobile] done: $(du -h "$OUT" | cut -f1)" >&2
