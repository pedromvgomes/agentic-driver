#!/usr/bin/env bash
# Detect the most likely parent (source) branch of the current branch.
# Outputs JSON: {current, parent, base, source, candidates: [{branch, depth, ancestor}]}
#
# `source` says how the parent was determined, so the caller can weigh how much
# to trust it: "gt-metadata" is authoritative, "default-branch" is certain,
# "merge-base" is a ranked guess the user should confirm.
set -euo pipefail

CURRENT=$(git branch --show-current)
if [ -z "$CURRENT" ]; then
  echo '{"error": "detached HEAD or not on a branch"}'
  exit 1
fi

HEAD_SHA=$(git rev-parse HEAD)

emit() {  # emit <parent> <base> <source> <candidates-json>
  printf '{"current":"%s","parent":"%s","base":"%s","source":"%s","candidates":[%s]}\n' \
    "$CURRENT" "$1" "$2" "$3" "${4:-}"
}

# Resolve the repo's default branch, preferring the remote's own pointer over a
# local ref: in a stacked-branch workflow the local default branch is routinely
# many commits behind its remote, and merge-basing against it dates the review
# to whenever the branch was last pulled.
DEFAULT=$(git symbolic-ref --short refs/remotes/origin/HEAD 2>/dev/null || true)
if [ -z "$DEFAULT" ]; then
  for b in main master trunk develop; do
    if git rev-parse --verify --quiet "refs/remotes/origin/$b" >/dev/null; then DEFAULT="origin/$b"; break; fi
    if git rev-parse --verify --quiet "refs/heads/$b" >/dev/null; then DEFAULT="$b"; break; fi
  done
fi

# 1. Graphite records the stack's real parent in a per-branch metadata ref.
#    When it is present it beats every heuristic below.
GT_META=$(git cat-file -p "refs/branch-metadata/$CURRENT" 2>/dev/null || true)
if [ -n "$GT_META" ]; then
  GT_PARENT=$(printf '%s' "$GT_META" \
    | sed -n 's/.*"parentBranchName"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
  if [ -n "$GT_PARENT" ]; then
    # Prefer the remote copy when it exists — same staleness argument as above.
    for ref in "origin/$GT_PARENT" "$GT_PARENT"; do
      if git rev-parse --verify --quiet "$ref" >/dev/null && BASE=$(git merge-base HEAD "$ref" 2>/dev/null); then
        emit "$ref" "$BASE" "gt-metadata"
        exit 0
      fi
    done
  fi
fi

# 2. When the default branch already contains HEAD, the branch has no commits of
#    its own and the base is HEAD itself — the review is worktree-only. This is
#    a definite answer, not a guess, and must be settled before the ranking below
#    (which discards candidates containing HEAD as children).
if [ -n "$DEFAULT" ] && DEF_MB=$(git merge-base HEAD "$DEFAULT" 2>/dev/null) && [ "$DEF_MB" = "$HEAD_SHA" ]; then
  emit "$DEFAULT" "$HEAD_SHA" "default-branch"
  exit 0
fi

# 3. Rank every other branch that shares history with HEAD.
#
# Candidate refs: every local branch except the current one, plus every remote
# branch except this branch's own remote copy (same work, would yield a base of
# the branch's own tip) and <remote>/HEAD, a symbolic alias for the default
# branch. Note that `%(refname:short)` renders refs/remotes/origin/HEAD as bare
# "origin", so the HEAD aliases must be filtered on the full refname.
UPSTREAM=$(git rev-parse --abbrev-ref --symbolic-full-name '@{upstream}' 2>/dev/null || true)
CANDIDATES=$( { git for-each-ref --format='%(refname:short)' refs/heads/ | grep -vx "$CURRENT" || true
                { git for-each-ref --format='%(refname)' refs/remotes/ \
                  | grep -v '/HEAD$' || true; } \
                  | sed 's|^refs/remotes/||' \
                  | while read -r r; do
                      [ -z "$r" ] && continue
                      [ "${r#*/}" = "$CURRENT" ] && continue   # origin/<this branch>
                      [ -n "$UPSTREAM" ] && [ "$r" = "$UPSTREAM" ] && continue
                      printf '%s\n' "$r"
                    done
              } \
  | while read -r b; do
      [ -z "$b" ] && continue
      mb=$(git merge-base HEAD "$b" 2>/dev/null) || continue
      [ "$mb" = "$HEAD_SHA" ] && continue   # b contains HEAD — a child, not a parent
      depth=$(git rev-list --count "$mb..HEAD")
      # The shallowest merge-base wins: it is the most recent point HEAD shares
      # with anything, which is where the branch was cut. Ancestry only breaks a
      # tie — a branch HEAD was cut from is contained in HEAD's history, a sibling
      # that merely shares an ancestor is not. Ancestry cannot be the primary key:
      # a branch merged into the default branch before the fork point is also an
      # ancestor, and would then outrank the default branch itself.
      if git merge-base --is-ancestor "$b" HEAD 2>/dev/null; then rank=0; anc=true; else rank=1; anc=false; fi
      printf '%s %s %s %s\n' "$rank" "$depth" "$b" "$anc"
    done \
  | sort -k2,2n -k1,1n)

PARENT=$(echo "$CANDIDATES" | head -1 | awk '{print $3}')
[ -z "$PARENT" ] && PARENT="$DEFAULT"

if [ -z "$PARENT" ] || ! BASE=$(git merge-base HEAD "$PARENT" 2>/dev/null); then
  printf '{"current":"%s","error":"could not resolve a parent branch sharing history with HEAD; pass a base ref explicitly"}\n' "$CURRENT"
  exit 1
fi

CAND_JSON=$(echo "$CANDIDATES" \
  | awk 'NF {printf "%s{\"branch\":\"%s\",\"depth\":%s,\"ancestor\":%s}", (NR>1?",":""), $3, $2, $4}')

emit "$PARENT" "$BASE" "merge-base" "$CAND_JSON"
