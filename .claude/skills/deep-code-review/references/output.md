# Finding schema, consolidation, and presentation

## Finding schema (every reviewer returns exactly this)

```yaml
- severity: RED | AMBER | GREEN
  category: <axis, e.g. "security:injection", "conventions:comment-hygiene">
  file: path/to/file.kt
  line: <line or range, or "n/a" if cross-cutting>
  issue: <one-sentence description of the problem>
  evidence: <short quote from the diff/file; convention findings also quote the rule and its source>
  proposed_action: <concrete fix, not a vague suggestion>
  confidence: high | medium | low
```

Severity calibration (identical for all reviewers; overrides any conflicting bar in an axis prompt):

- **RED** — must fix before merge: real bug, exploitable vuln, data loss, significant perf regression on a hot path, breaks a
  documented contract.
- **AMBER** — should fix: latent risk, maintainability problem, minor perf issue, convention violation with real downstream cost.
- **GREEN** — nice to have: nit, opportunistic improvement.

Every finding must quote the offending line(s) in `evidence`. A finding without quotable code does not get filed. An empty list is a
valid outcome.

## Consolidation (after the validation wave)

1. Merge duplicates: same file + line + underlying issue → keep the higher severity, merge `proposed_action`, comma-join the
   `category` values, mark confidence `high (N agents)`.
2. In multi-stack runs, categories carry a stack prefix (`kotlin-spring/security:injection`) so converging panels stay distinguishable.
3. Apply validator verdicts: drop `rejected`, apply `downgraded` severities.
4. Demote any surviving RED with `confidence: low` to AMBER, marked `low (demoted)` — unless it was corroborated by convergence.
5. Sort by severity (RED → AMBER → GREEN), then by file path within a severity. Sort last, after all demotions settle.
6. Number the survivors continuously across severities (1, 2, 3, …) so the user can say "fix 1, 4, 7".

## Presentation

Render one markdown table per severity; omit empty sections. Then two short closing sections.

```markdown
## RED — must fix before merge
| # | Category           | Location             | Conf | Issue                                       | Proposed action                            |
|---|--------------------|----------------------|------|---------------------------------------------|--------------------------------------------|
| 1 | security:injection | UserController.kt:42 | high | Unparameterized SQL built from request body | Switch to JdbcTemplate parameterized query |

## AMBER — should fix
| # | ... |

## GREEN — nice to have
| # | ... |

## What's good
<2-4 bullets on genuinely well-done aspects of the change — real observations, not filler. Omit the section rather than pad it.>

## Review record
Sizing: <the one-line sizing decision> · Agents run: <N> · Validation: <dropped X of Y candidates> · Conventions: <M rules from K docs, or "none found">
```

If no findings survive, print the "What's good" and "Review record" sections only, say the review found nothing, and stop — do not
emit the fix offer below, and do not ask the user to choose from an empty list.

Otherwise, after the tables, offer the fix step:

> Tell me which findings to fix (e.g. "all RED", "fix 1, 4, 7", "skip all"). I won't modify code without your explicit selection.

When the run was invoked by another skill (pre-captured input mode), skip the fix offer and end by returning the numbered findings —
the caller owns the next step.
