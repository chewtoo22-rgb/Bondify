#!/usr/bin/env bash
set -euo pipefail

DIAG_URL="${BONDIFY_DIAG_URL:-http://127.0.0.1:8080/api/v1/diagnostics/redacted}"
OUT_DIR="${1:-bondify-support-$(date -u +%Y%m%dT%H%M%SZ)}"

umask 077
mkdir -p "$OUT_DIR"

printf 'Bondify support bundle\n' >"$OUT_DIR/README.txt"
printf 'Generated: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >>"$OUT_DIR/README.txt"
printf 'Diagnostics source: redacted localhost endpoint\n' >>"$OUT_DIR/README.txt"
printf 'Review contents before sharing. Do not add raw key files, full diagnostics, or packet captures unless specifically requested and scrubbed.\n' >>"$OUT_DIR/README.txt"

curl --fail --silent --show-error --max-time 5 "$DIAG_URL" >"$OUT_DIR/diagnostics-redacted.json"

if command -v uname >/dev/null 2>&1; then
  uname -a >"$OUT_DIR/platform.txt" 2>&1 || true
fi
if command -v ip >/dev/null 2>&1; then
  # Interface names and link state are useful for support; addresses are intentionally omitted.
  ip -brief link >"$OUT_DIR/interfaces.txt" 2>&1 || true
fi

(
  cd "$OUT_DIR"
  sha256sum ./* >SHA256SUMS 2>/dev/null || true
)

echo "Created redacted support bundle at: $OUT_DIR"
