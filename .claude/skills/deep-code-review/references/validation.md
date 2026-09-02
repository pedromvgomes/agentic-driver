# Validation wave and false-positive control

False positives erode trust and waste reviewer time. Every candidate finding on rungs 2 and 3 is independently re-checked before the
user sees it. Reviewers get their own copy of this list via `prompts/shared/preamble.md`; keep the two in step when editing either. On rung 1 the orchestrator performs the same re-check itself; rung 0 findings are already the orchestrator's own reads.

## The do-not-flag list (applies to every validator, and to the orchestrator on rungs 0–1)

Never surface, at any severity:

- Pre-existing issues on lines the diff did not touch (mention at most once, outside the findings table, as a one-line aside).
- Code that *looks* wrong but is actually correct — trace the logic before filing.
- Pedantic nitpicks and subjective style preferences.
- Anything a linter or formatter would catch, unless the linter was actually run and does not catch it.
- General quality or security concerns not grounded in this diff's code or the repo's own written rules.
- Issues explicitly silenced in the code via a lint-ignore/suppress annotation.
- Potential issues that depend on specific inputs or state the validator cannot show are reachable.
- Unless the repo's rules demand them: DoS/rate-limiting concerns, memory/CPU exhaustion, generic "validate this input" advice with no
  proven impact, open redirects. The user can opt these back in by asking for them.

When uncertain whether an issue is real, do not flag it.

## Wave mechanics

1. Collect every candidate finding from the review agents; dedupe first (same file + line + underlying issue → one candidate, noting how
   many agents converged on it).
2. Drop, without validation: GREEN findings with `confidence: low`.
3. Skip validation for: findings reported by two or more agents **running different axis prompts**, and GREEN findings generally
   (they are suggestions, not claims). Cross-axis agreement is corroboration — mark those `high (N agents)`.

   Agreement between the rung-3 duplicate lanes is **not** corroboration and does not earn the exemption: bug-hunters A/B and
   conventions auditors A/B run the same prompt on the same input, so their errors are correlated by construction and a shared
   hallucination would otherwise reach the user labelled `high (2 agents)` — the exact failure this wave exists to prevent. Validate
   those normally; they may still be marked `high (N agents)` once confirmed.
4. Batch the rest into validation subagents, grouped by file, at most 5 findings per validator:
   - bug/logic/security/performance claims → `model: "opus"` validators
   - convention/comment-hygiene claims → `model: "sonnet"` validators
   Launch all validators in a single message so they run in parallel.
5. Each validator returns a verdict per finding: `confirmed`, `rejected`, or `downgraded` (real but overstated — new severity attached).
6. Only `confirmed` and `downgraded` findings reach the user. Report the rejection count in one line ("validation dropped 3 of 11
   candidates") — never the rejected findings themselves.

## Validator prompt (assemble per batch)

> You are a validation reviewer. Other agents flagged the candidate issues below; your sole job is to decide, for each one, whether it
> is real — by rereading the actual code, not the claim. All tools are functional and will work without error; do not test tools or
> make exploratory calls.
>
> For each candidate: `Read` the cited file around the cited lines (and any code it calls or is called by, if reachability matters to
> the claim). Then verdict it:
> - `confirmed` — you independently verified the issue exists as described. The bar: the code will fail to compile or parse, will
>   definitely produce wrong results regardless of inputs, has a concretely exploitable flaw, or unambiguously violates a written repo
>   rule you can quote.
> - `downgraded: <severity>` — the issue is real but the severity is overstated; say why in one sentence.
> - `rejected` — you could not verify it, it depends on inputs or state not shown to be reachable, it matches the do-not-flag list, or
>   the code is actually correct.
>
> Apply this do-not-flag list: [paste the list above verbatim].
>
> Return one line per candidate: `<candidate-id>: <verdict> — <one-sentence justification citing the code you read>`. Nothing else.
> If you are not certain an issue is real, reject it.

Append the candidate findings (id, file, line, issue, evidence, severity, category) and the conventions summary (validators of
convention claims need the rule text to quote).
