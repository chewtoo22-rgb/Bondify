#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
VALIDATOR="$ROOT/qa/release-asset-completeness.sh"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

make_valid() {
  local dir=$1 name=${2:-bondify-linux-amd64}
  mkdir -p "$dir"
  printf 'payload-%s\n' "$name" > "$dir/$name.tar.gz"
  (cd "$dir" && sha256sum "$name.tar.gz" > "$name.tar.gz.sha256")
}

expect_fail() {
  local label=$1 dir=$2
  if bash "$VALIDATOR" "$dir" >/dev/null 2>&1; then
    echo "expected failure: $label" >&2
    exit 1
  fi
}

ok="$TMP/ok"
make_valid "$ok" bondify-linux-amd64
make_valid "$ok" bondify-windows-amd64
bash "$VALIDATOR" "$ok" >/dev/null
grep -q 'bondify-linux-amd64.tar.gz' "$ok/SHA256SUMS"
grep -q 'bondify-windows-amd64.tar.gz' "$ok/SHA256SUMS"

missing="$TMP/missing"
make_valid "$missing"
rm "$missing/bondify-linux-amd64.tar.gz.sha256"
expect_fail "missing checksum" "$missing"

orphan="$TMP/orphan"
make_valid "$orphan"
rm "$orphan/bondify-linux-amd64.tar.gz"
expect_fail "orphan checksum" "$orphan"

bad="$TMP/bad"
make_valid "$bad"
printf 'tampered\n' >> "$bad/bondify-linux-amd64.tar.gz"
expect_fail "digest mismatch" "$bad"

apk="$TMP/apk"
make_valid "$apk"
printf 'debug apk\n' > "$apk/bondify-android.apk"
expect_fail "apk boundary" "$apk"

unexpected="$TMP/unexpected"
make_valid "$unexpected"
printf 'notes\n' > "$unexpected/notes.txt"
expect_fail "unexpected file" "$unexpected"

mismatch="$TMP/mismatch"
make_valid "$mismatch"
sed -i 's/bondify-linux-amd64.tar.gz/other.tar.gz/' "$mismatch/bondify-linux-amd64.tar.gz.sha256"
expect_fail "checksum filename mismatch" "$mismatch"

symlink="$TMP/symlink"
make_valid "$symlink"
ln -s bondify-linux-amd64.tar.gz "$symlink/alias.tar.gz"
expect_fail "symlink asset" "$symlink"

echo "release asset completeness tests: PASS"
