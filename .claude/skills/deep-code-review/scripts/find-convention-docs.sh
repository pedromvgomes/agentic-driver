#!/usr/bin/env bash
# Find convention docs at the repo root and at any module roots passed as
# arguments. Emits a JSON array of paths (relative to repo root), deduped.
set -euo pipefail

REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT"

PATHS=("." "$@")
DOCS=(
  "docs/ARCHITECTURE.md"
  "docs/CODE_STANDARDS.md"
  "AGENTS.md"
  "CLAUDE.md"
  ".claude/CLAUDE.md"
  "CONTRIBUTING.md"
)

found=()
for root in "${PATHS[@]}"; do
  for doc in "${DOCS[@]}"; do
    candidate="${root%/}/$doc"
    candidate="${candidate#./}"
    [ -f "$candidate" ] || continue
    # A module arg may resolve to the repo root, or repeat across args.
    for seen in ${found[@]+"${found[@]}"}; do
      [ "$seen" = "$candidate" ] && continue 2
    done
    found+=("$candidate")
  done
done

if [ ${#found[@]} -eq 0 ]; then
  echo "[]"
else
  printf '['
  for i in "${!found[@]}"; do
    [ "$i" -gt 0 ] && printf ','
    printf '"%s"' "${found[$i]}"
  done
  printf ']\n'
fi
