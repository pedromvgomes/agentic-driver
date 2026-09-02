#!/usr/bin/env bash
# Emit the name-status lists matching capture-diff.sh, section for section.
#
# usage: list-changed.sh <base-ref> [pathspec...]
#
# Pathspecs follow the same rules as capture-diff.sh: a '!' prefix excludes,
# anything else restricts the output to matching files, and a spec with no '/'
# is matched at any depth, as both a file and a directory.
#
# DCR_HEAD_REF behaves as it does in capture-diff.sh, so the two stay in step.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

BASE="${1:?usage: list-changed.sh <base-ref> [pathspec...]}"
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

echo "=== name-status vs ${BASE} (committed, to ${HEAD_REF}) ==="
git diff --name-status "${BASE}...${HEAD_REF}" -- "${PATHSPEC[@]}"

# Matches capture-diff.sh: a commit range excludes the working tree.
if [ -n "${DCR_HEAD_REF:-}" ]; then
  exit 0
fi

echo ""
echo "=== name-status worktree (staged + unstaged, vs HEAD) ==="
git diff --name-status HEAD -- "${PATHSPEC[@]}"
echo ""
echo "=== untracked files ==="
git ls-files --others --exclude-standard --full-name -- "${PATHSPEC[@]}" | awk 'NF {print "A\t" $0}'
