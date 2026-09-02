---
name: continuation-session
description: Close the current session and compact conversation into a handoff document so a fresh agent can continue the work in a new session
argument-hint: What is the new session goal?
disable-model-invocation: true
---

Produce a handoff document that lets a fresh agent — with no memory of this conversation — continue the work in a new session.

## 1. Establish the goal first

Before writing anything, determine the objective of the next session:
- If the user passed arguments, treat them as the description of what the next session will focus on.
- If no arguments were passed and the goal is not obvious from the conversation, ask before proceeding.

The goal is a filter: include only what is relevant to achieving it, and deliberately omit the rest.

## 2. Leave the current worktree in a stable state

Before writing the handoff, make sure the current worktree is in a clean, recoverable state so the next session never resumes on top of unsaved work:
- There must be no uncommitted or unstaged changes — commit them, or explicitly note in the handoff why they are being left.
- The branch must be pushed to the remote.

If you cannot reach this state (e.g. work is mid-refactor and not yet committable), STOP and tell the user what is blocking a clean handoff rather than writing a doc that points
at a dirty worktree.

## 3. Decide where the next session should resume

The handoff MUST state clearly which of these applies, because it determines where the next agent starts:
- **Start from a NEW worktree** — when the current branch is pushed and either merged or has an open PR, and the next goal is distinct work.
- **Continue on the CURRENT worktree** — when the next goal is a direct continuation of the in-flight branch. Include the worktree path and branch name.

If which one applies is not obvious from the goal and current state, ASK the user before finalising the document.

## 4. Write the handoff document

Save it to the OS temporary directory (e.g. `$TMPDIR` on macOS/Linux, falling back to `/tmp`), NOT the current workspace — this is a throwaway artifact and must not be committed.

Use a descriptive, timestamped filename, e.g. `handoff-<short-goal-slug>-<YYYYMMDD-HHMM>.md`.

Include only the minimum a fresh agent needs to be productive immediately:
- **Objective** — what the next session must accomplish, as a concrete outcome.
- **Resume location** — NEW vs CURRENT worktree per section 3, with the exact path/branch or `gt` command.
- **Current state** — what is done and what remains, scoped to the goal. Note branch, PR link, and merge status.
- **Key context & decisions** — non-obvious constraints, approaches chosen, and dead ends to avoid repeating. Capture reasoning that lives only in this conversation.
- **Next steps** — the concrete first actions.
- **References** — paths/URLs to relevant files, branches, PRs, issues.

Do NOT duplicate content already captured in other artifacts (PRDs, plans, ADRs, issues, commits, diffs). Reference them by path or URL instead.

## 5. Hand off (manual context reset)

You CANNOT clear your own context or start the new session yourself — that is a manual step the user must perform. Do not claim otherwise.

Once the document is saved, output:
1. A short confirmation with the handoff document's full path.
2. A **continuation prompt** the user can paste into a fresh session. It MUST reference the handoff document by full path and briefly state the objective, e.g.:
   `Read the handoff at /tmp/handoff-<...>.md and continue: <objective>.`
3. A one-line instruction telling the user to run `/clear` (or `/new`) and then paste the continuation prompt.
