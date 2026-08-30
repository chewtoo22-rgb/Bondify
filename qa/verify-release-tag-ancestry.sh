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

# checkout@v4 normally leaves a shallow repository. Hydrate the complete main
# ancestry into the exact remote-tracking ref we verify. An explicit refspec
# avoids relying on the checkout's remote fetch configuration, while the very
# large depth works for both shallow and already-complete repositories without
# the brittle --unshallow state transition.
git fetch --no-tags --prune --depth=2147483647 origin \
  '+refs/heads/main:refs/remotes/origin/main'

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
