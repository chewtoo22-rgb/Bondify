#!/usr/bin/env bash
set -euo pipefail

candidate=${1:-}
main_ref=${2:-origin/main}

if [[ -z "$candidate" ]]; then
  echo "usage: $0 <candidate-commit> [main-ref]" >&2
  exit 2
fi

if ! git rev-parse --verify "${candidate}^{commit}" >/dev/null 2>&1; then
  echo "Refusing release: candidate '$candidate' does not resolve to a commit." >&2
  exit 1
fi

candidate_commit=$(git rev-parse "${candidate}^{commit}")

# Do not mutate or depend on the caller's shallow boundary. checkout@v4 can
# leave unrelated shallow roots (for example a tag/side commit), which makes
# --unshallow against main fragile. Instead, prove containment in a disposable
# full-history mirror of the configured origin. This keeps release policy
# independent of checkout shape and leaves the working repository untouched.
origin_url=$(git remote get-url origin 2>/dev/null || true)
if [[ -z "$origin_url" ]]; then
  echo "Refusing release: origin remote is unavailable." >&2
  exit 1
fi

verify_repo=$(mktemp -d)
trap 'rm -rf "$verify_repo"' EXIT
if ! git clone --quiet --no-tags --bare "$origin_url" "$verify_repo/repo.git"; then
  echo "Refusing release: could not hydrate origin history." >&2
  exit 1
fi

main_branch=${main_ref#origin/}
main_verify_ref="refs/heads/$main_branch"
if ! git -C "$verify_repo/repo.git" rev-parse --verify "${main_verify_ref}^{commit}" >/dev/null 2>&1; then
  echo "Refusing release: main ref '$main_ref' does not resolve to a commit." >&2
  exit 1
fi
main_commit=$(git -C "$verify_repo/repo.git" rev-parse "${main_verify_ref}^{commit}")

# The candidate may have been checked out by SHA and need not have a named ref
# in the mirror. Fetch that exact object explicitly, then perform the ancestry
# proof entirely inside the complete mirror.
if ! git -C "$verify_repo/repo.git" cat-file -e "${candidate_commit}^{commit}" 2>/dev/null; then
  if ! git -C "$verify_repo/repo.git" fetch --quiet --no-tags "$origin_url" "$candidate_commit"; then
    echo "Refusing release: candidate $candidate_commit is unavailable from origin." >&2
    exit 1
  fi
fi

if ! git -C "$verify_repo/repo.git" merge-base --is-ancestor "$candidate_commit" "$main_commit"; then
  echo "Refusing release: candidate $candidate_commit is not contained in $main_ref ($main_commit)." >&2
  exit 1
fi

echo "release_tag_ancestry=pass"
echo "candidate_commit=$candidate_commit"
echo "main_commit=$main_commit"
