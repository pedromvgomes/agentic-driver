#!/usr/bin/env bash
# Emit the full set of changes to review: committed-vs-base, all worktree
# changes (staged and unstaged together), and untracked files.
#
# usage: capture-diff.sh <base-ref> [pathspec...]
#
# Each pathspec is a path or glob relative to the repo root. A pathspec
# prefixed with '!' excludes matching files; any other pathspec restricts the
# output to matching files. Excludes always win.
#
# A pathspec containing no '/' is treated as a basename and matched at any
# depth, so '!package-lock.json' drops every lockfile in a monorepo rather than
# only the one at the root. Anchor to the root by writing a path with a slash.
#
#   capture-diff.sh main '!package-lock.json' '!vendor/**'   # everything but those
#   capture-diff.sh main '**/*.kt' '**/*.kts'                # only Kotlin sources
#   DCR_HEAD_REF=def456 capture-diff.sh abc123               # a commit range
set -euo pipefail

# Run from the repo root so every emitted path — including the untracked ones
# from git ls-files, which are printed relative to the cwd — is repo-relative.
cd "$(git rev-parse --show-toplevel)"

BASE="${1:?usage: capture-diff.sh <base-ref> [pathspec...]}"
shift
HEAD_REF="${DCR_HEAD_REF:-HEAD}"

INCLUDES=()
EXCLUDES=()
for spec in "$@"; do
  case "$spec" in
    !*) raw="${spec#!}"; kind=exclude ;;
    *)  raw="$spec";     kind=include ;;
  esac
  # A spec containing '/' is a path anchored at the root. A bare name is matched
  # at any depth, as both a file and a directory — '!vendor' must drop the whole
  # vendor/ tree, not just a file literally named "vendor".
  case "$raw" in
    */*) forms=("$raw") ;;
    *)   forms=("**/$raw" "**/$raw/**") ;;
  esac
  for form in "${forms[@]}"; do
    if [ "$kind" = exclude ]; then
      EXCLUDES+=(":(top,exclude,glob)$form")
    else
      INCLUDES+=(":(top,glob)$form")
    fi
  done
done
[ ${#INCLUDES[@]} -eq 0 ] && INCLUDES=(":(top)")
PATHSPEC=("${INCLUDES[@]}" ${EXCLUDES[@]+"${EXCLUDES[@]}"})

echo "=== diff vs ${BASE} (committed, to ${HEAD_REF}) ==="
git diff "${BASE}...${HEAD_REF}" -- "${PATHSPEC[@]}"

# A commit range is a historical artifact: the working tree is not part of it.
if [ -n "${DCR_HEAD_REF:-}" ]; then
  exit 0
fi

echo ""
echo "=== diff worktree (staged + unstaged, vs HEAD) ==="
git diff HEAD -- "${PATHSPEC[@]}"
echo ""
echo "=== untracked files ==="
while IFS= read -r f; do
  [ -z "$f" ] && continue
  # --no-index exits 1 whenever it finds a difference, which is always here.
  git diff --no-index -- /dev/null "$f" || true
done < <(git ls-files --others --exclude-standard --full-name -- "${PATHSPEC[@]}")
