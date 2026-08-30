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

# checkout@v4 defaults to a shallow clone. A depth-1 fetch of main cannot prove
# that an older release candidate is contained in main because the ancestry
# objects are absent. Deepen the repository before evaluating containment.
if [[ "$(git rev-parse --is-shallow-repository 2>/dev/null || echo false)" == "true" ]]; then
  git fetch --no-tags --prune --unshallow origin main
else
  git fetch --no-tags --prune origin main
fi

if ! git rev-parse --verify "${main_ref}^{commit}" >/dev/null 2>&1; then
  echo "Refusing release: main ref '$main_ref' does not resolve to a commit." >&2
  exit 1
fi

candidate_commit=$(git rev-parse "${candidate}^{commit}")
main_commit=$(git rev-parse "${main_ref}^{commit}")

if ! git merge-base --is-ancestor "$candidate_commit" "$main_commit"; then
  echo "Refusing release: candidate $candidate_commit is not contained in $main_ref ($main_commit)." >&2
  exit 1
fi

echo "release_tag_ancestry=pass"
echo "candidate_commit=$candidate_commit"
echo "main_commit=$main_commit"
