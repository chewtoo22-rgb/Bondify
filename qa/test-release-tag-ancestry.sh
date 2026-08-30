#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
verifier="$repo_root/qa/verify-release-tag-ancestry.sh"
run_verifier() {
  bash "$verifier" "$@"
}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

origin="$tmp/origin.git"
work="$tmp/work"
clone="$tmp/clone"

git init --bare "$origin" >/dev/null
git clone "$origin" "$work" >/dev/null 2>&1
(
  cd "$work"
  git config user.name test
  git config user.email test@example.invalid
  printf 'one\n' > state.txt
  git add state.txt
  git commit -m one >/dev/null
  first=$(git rev-parse HEAD)
  git branch -M main
  git push -u origin main >/dev/null 2>&1

  printf 'two\n' >> state.txt
  git commit -am two >/dev/null
  second=$(git rev-parse HEAD)
  git push origin main >/dev/null 2>&1

  git checkout -b unmerged "$first" >/dev/null 2>&1
  printf 'side\n' > side.txt
  git add side.txt
  git commit -m side >/dev/null
  side=$(git rev-parse HEAD)
  git push origin unmerged >/dev/null 2>&1

  printf '%s\n%s\n%s\n' "$first" "$second" "$side" > "$tmp/shas"
)

readarray -t shas < "$tmp/shas"
first=${shas[0]}
second=${shas[1]}
side=${shas[2]}

git -C "$origin" symbolic-ref HEAD refs/heads/main

git clone --depth=1 "file://$origin" "$clone" >/dev/null 2>&1
(
  cd "$clone"
  # Simulate checkout@v4 on an older tag candidate by fetching only that commit.
  git fetch --depth=1 origin "$first" >/dev/null 2>&1
  output=$(run_verifier "$first")
  grep -q '^release_tag_ancestry=pass$' <<<"$output"

  output=$(run_verifier "$second")
  grep -q '^release_tag_ancestry=pass$' <<<"$output"

  git fetch --depth=1 origin "$side" >/dev/null 2>&1
  if run_verifier "$side" >"$tmp/unmerged.out" 2>"$tmp/unmerged.err"; then
    echo 'expected unmerged candidate to be rejected' >&2
    exit 1
  fi
  grep -q 'is not contained in origin/main' "$tmp/unmerged.err"

  if run_verifier deadbeef >"$tmp/missing.out" 2>"$tmp/missing.err"; then
    echo 'expected malformed candidate to be rejected' >&2
    exit 1
  fi
  grep -q 'does not resolve to a commit' "$tmp/missing.err"
)

echo 'release tag ancestry tests: PASS'
