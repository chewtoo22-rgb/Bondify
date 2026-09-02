#!/usr/bin/env bash
set -euo pipefail

SCRIPT="$(cd "$(dirname "$0")/.." && pwd)/scripts/collect-support-bundle.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

mkdir -p "$TMP/bin"
cat >"$TMP/bin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '{"ok":true}\n'
EOF
chmod +x "$TMP/bin/curl"

if PATH="$TMP/bin:$PATH" BONDIFY_DIAG_URL='http://10.0.0.7:9090/api/v1/diagnostics/redacted' bash "$SCRIPT" "$TMP/rejected" >/dev/null 2>&1; then
  echo 'non-loopback diagnostics URL was accepted' >&2
  exit 1
fi

PATH="$TMP/bin:$PATH" BONDIFY_DIAG_URL='http://127.0.0.1:9090/api/v1/diagnostics/redacted' bash "$SCRIPT" "$TMP/accepted" >/dev/null
[[ -s "$TMP/accepted/diagnostics-redacted.json" ]]

echo 'support bundle loopback guard passed'
