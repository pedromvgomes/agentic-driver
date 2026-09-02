# Sizing: how the review scales to the change

Blast radius determines depth, not line count. A one-line middleware change outranks a 300-line rename. Sizing combines three inputs —
raw size, criticality signals, and reference fan-in — into a rung on the ladder below. All thresholds here are heuristics: state them,
apply them, and let the user override the result.

## Step 1 — Raw size

From the changed-files list and diff, count reviewable files and changed lines (added + removed, after exclusions; renames and pure
moves count their *edited* lines only, not the mechanical move).

| Size class | Trigger |
|---|---|
| Trivial | ≤ 2 files AND ≤ 40 changed lines |
| Small | ≤ 5 files AND ≤ 150 changed lines |
| Medium | ≤ 20 files AND ≤ 800 changed lines |
| Large | anything bigger |

## Step 2 — Criticality signals

Check the changed files (paths AND diff content) against this list. Each bullet is one signal; count distinct signals hit.

- **Auth**: authentication, authorization, session, token, or permission logic — paths or symbols matching `auth`, `authn`, `authz`,
  `session`, `token`, `permission`, `rbac`, `acl`, or changes to middleware/filter/interceptor chains that gate requests.
- **DB migrations**: files under `migrations/`, `db/migrate/`, `*.sql` DDL, Flyway/Liquibase changesets. Flag specifically: a `NOT NULL`
  column added without a default or a two-phase migration.
- **Shared kernel / libraries**: changes under a directory imported by 3+ other top-level modules (`common/`, `shared/`, `lib/`,
  `pkg/`, `core/`, internal platform libraries).
- **Public API contracts**: exported/public function signatures, REST/gRPC/GraphQL schemas, OpenAPI/proto files, serialized formats,
  wire-visible DTOs.
- **Message consumers**: queue/topic handlers, event consumers, schedulers — code that runs without a request in front of it.
- **CI/CD and release**: `.github/workflows/`, Jenkinsfiles, release/publish scripts, Dockerfiles used in the pipeline.
- **IaC**: Terraform, CloudFormation, Helm charts, Kubernetes manifests.
- **Concurrency/locking**: new or changed mutexes, channels, transactions, optimistic-lock version fields, `synchronized`, atomics.
- **Sensitive data paths**: PII handling, payment/billing code, logging changes near credential or personal data.
- **Crypto**: anything touching key material, hashing for security purposes, TLS configuration, random-token generation.
- **Feature flags / kill switches**: flag definitions, default flips, removal of a guard.
- **Fix-revert risk** (git-blame check): `git log --format='%H %s' -n1 -L"<start>,<end>:<file>" --` (or `git blame` on the pre-image) for
  deleted/modified hunks; if a touched line traces to a commit whose message contains `fix`, `bug`, `security`, `CVE`, `revert`, or
  `hotfix`, count this signal — the change may be undoing a deliberate repair. Run this only on deleted/rewritten lines, not additions,
  and skip it for Trivial changes in docs.

## Step 3 — Reference fan-in (blast radius, measured not guessed)

Path patterns alone are a weak signal — back them with a reference count where it is cheap. For up to ~10 changed exported/public
symbols (prefer ones in files that hit a signal above), count downstream users:

```bash
grep -rn --include='*.<ext>' -l -e '\b<SymbolName>\b' -- <repo-src-dirs> | grep -v -F -e '<defining-file>' | wc -l
```

Quote every interpolated value and terminate options with `--`: under `pr-code-review` the symbol names and paths come from a PR
someone else wrote. Skip any symbol or path containing shell metacharacters rather than interpolating it.

Use `mcp__serena__find_referencing_symbols` instead when the serena tools are available — it is more precise than grep. Skip the count
entirely for Trivial changes and for symbols that are obviously local (unexported, file-private).

| Distinct referencing files | Fan-in class |
|---|---|
| 0–4 | low |
| 5–19 | elevated — counts as one criticality signal |
| ≥ 20 | critical — forces rung 3 regardless of size |

## Step 4 — Map to a rung

| Rung | Agents | Trigger |
|---|---|---|
| 0 — inline | 0 (orchestrator reviews the diff itself) | Trivial size AND zero signals AND low fan-in |
| 1 — solo | 1 | Small size AND zero signals AND low fan-in; or Trivial with exactly one signal |
| 2 — panel | 2–3 per stack | Medium size; or Small/Trivial with 1+ signals; or elevated fan-in |
| 3 — deep | up to 7 per panel | Large size; or any size with 2+ signals; or critical fan-in; or Medium with 1+ signals |

**The triggers overlap by design — take the highest rung whose trigger matches.** A Trivial change with one signal matches both rung 1
and rung 2; it is rung 2. A Medium change with a signal matches both rung 2 and rung 3; it is rung 3.

Mechanical bulk: a change that is Large only because of a rename sweep, formatting, generated files, lockfiles, or docs is sized on
what is left. Exclude the mechanical files from review scope, then re-run Steps 1–4 on the reviewable remainder; that result is the
rung. Do not also demote — the re-sizing already accounts for the bulk.

## Step 5 — What each rung runs

Axis prompts come from `panels.json` (`axes.<stack>.<axis>`); shared prompts from `panels.json` (`shared.*`). A stack without a given
axis (e.g. `general` has no `performance`) simply skips it.

- **Rung 0**: no subagents. The orchestrator reads the diff and files itself and applies the correctness axis scope plus the
  comment-hygiene rule inline. No validation wave. Skip convention extraction unless a convention doc sits in the changed files'
  own directories.
- **Rung 1**: one agent per run (not per stack — fold everything into the dominant stack's prompt). Its prompt concatenates the stack's
  `correctness` and `security` axis bodies plus `comment-hygiene`, with a preamble note that it covers both axes alone (ignore the
  prompts' sibling-agent framing). Model: the security axis's model. No validation wave — instead the orchestrator itself re-checks each
  candidate finding against the code before presenting it.
- **Rung 2**: per significant stack bucket: `correctness` + `security` agents; add `performance` when the diff touches hot paths, loops
  over collections of unbounded size, I/O in request paths, or the user asks. `comment-hygiene` is appended to every correctness agent.
  Validation wave runs (see `references/validation.md`).
- **Rung 3**: the 2×2 core plus specialists, capped at 7 agents **per panel**:
  1. bug-hunter A — stack `correctness` prompt + comment-hygiene, model `opus`, scoped to "obvious bugs in the diff only"
  2. bug-hunter B — same prompt, model `opus`, scoped to "incorrect logic and edge cases in changed code only", run independently
  3. conventions auditor A — `shared.conventions-compliance` prompt, model `sonnet`
  4. conventions auditor B — same prompt, independent duplicate, model `sonnet`
  5. `security` axis agent
  6. `performance` axis agent (skip if no stack in scope defines it)
  7. `shared.tests` agent
  The duplicates are the point: A and B must not be told about each other's findings; convergence is measured at consolidation.
  The cap is per panel, so every significant stack bucket gets its own full rung-3 roster. Announce the total agent count in the
  sizing line before launching — a three-stack rung-3 review is 21 agents, and the user may want to narrow the panels instead.
  Validation wave runs.

## Presenting the decision

Before fan-out, show the user exactly one sizing line and let them override the count:

> Sizing: **medium** (14 files, 420 lines) · signals: **auth, migrations** · fan-in: elevated (JwtFilter → 11 files) → **rung 3, 6 agents**.

The user may reply with a different rung or agent count; honor it without argument.
