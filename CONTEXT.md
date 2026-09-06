# Agentic Driver

A Go library that drives headless coding-agent CLIs. The library owns the **process**;
a **Provider** owns the **dialect**. This glossary fixes the words that mean different
things in each vendor's CLI, so that a term in this repo means one thing regardless of
which CLI is behind it.

## Language

**Provider**:
The dialect of one CLI — its flag spelling, its event schema, its credential variables.
_Avoid_: adapter, backend, driver (the **Driver** is the process side).

**Driver**:
The process side: argv assembly, environment construction, timeouts, cancellation,
stderr redaction. Written once and identical for every **Provider**.

**Capability**:
An optional behaviour a **Provider** declares by implementing an interface, discovered
by type assertion. A capability that is absent is absent from the type, so the **Driver**
answers for it before spawning anything.
_Avoid_: feature flag, option, supports-X boolean.

**Invocation**:
The argv after the executable plus the non-secret environment the dialect requires.

**Schema**:
A JSON Schema the final answer must conform to. It is a **Capability**: a provider that
cannot constrain its output does not implement `SchemaConstrainer`, and the **Driver**
refuses the request rather than returning prose nothing marks as unconstrained.
_Avoid_: format, output format (the latter is a CLI flag about the transcript, not the answer).

### Outcomes

**Verdict**:
The CLI ran, was understood, and an outcome was determined — including an outcome it
considers a failure. A verdict is a populated **Result** with a nil error, and `IsError`
distinguishes a bad verdict from a good one. The outcome is usually the CLI's own; an
**unmet constraint** is the one the library determines for itself.
_Avoid_: failure, error (both are ambiguous between the two outcomes).

**Outage**:
The invocation could not be carried out or could not be understood — a missing binary, a
cancellation, or output that is not decodable. An outage is `ErrProviderUnavailable` and
carries no **Result**.
_Avoid_: crash, failure.

**Unmet constraint**:
A run given a **Schema** that produced no structured payload. It is a **Verdict**: the
`Result` is populated, the error is nil, `IsError` is set and `Structured` is nil, and
`Text` carries the agent's own account of why it could not answer in the required shape.
The CLI may have called the run a success — Claude Code does, on exit 0 — but the shape
the caller required is the library's to judge, because a caller that asked for JSON and
received prose has not been answered.
_Avoid_: schema error, validation failure (both suggest an **Outage**).

**Refusal**:
The agent declining to do something because its sandbox forbids it. A refusal is a
**Verdict** of success: the CLI did exactly what it was configured to do. It is not an
**Outage** and it does not set `IsError`.

### The stream

**Event**:
One decoded item from a run, projected onto the provider-neutral `EventKind` vocabulary.
The raw line is carried alongside for a caller that wants a **Provider**'s own detail.

**Decoder**:
A per-run, stateful object that consumes the lines of one run and produces **Event**s and,
at the end, the **Result**. Stateful because a **Result** is a fold over the whole run, not
a projection of its last line.

**Result**:
What the run said and what it cost. Deliberately thin: a field the **Provider** has no
counterpart for is left zero.

**Turn**:
**Ambiguous across providers — see Flagged ambiguities.** In this repo, `Result.Turns` is
whatever the CLI itself counted as a turn, in its own units.

## Flagged ambiguities

**"Turn"** — the two CLIs count different things under this word.
Claude Code's `num_turns` counts *iterations of the agent loop*: one turn per
model round-trip, so a run that calls three tools reports several. Codex's
`turn.started` fires once for the *entire* agent loop, so a plain `codex exec`
always reports one however many tools it ran.
Resolution: `Result.Turns` is reported in the provider's own unit and is **not**
comparable across providers. Nothing in the library derives behaviour from it. A caller
wanting a portable bound on a run uses `Request.Timeout`, which the **Driver** enforces
identically everywhere.

**"Envelope"** — used loosely for both a single terminal JSON document and the terminal
line of a stream.
Resolution: reserve **envelope** for a whole document that a **Provider** emits as its
final word, and **Event** for one item of a stream. Codex has no envelope at all; its
`--json` is a stream throughout, and its **Result** exists only as a fold.

**"Permission mode"** — Claude Code has one axis (`--permission-mode`); Codex has two
(`sandbox_mode` and `approval_policy`).
Resolution: `Request.PermissionMode` is spelled in the **Provider**'s own vocabulary and
each **Provider** maps it to the axis that actually constrains authority. For Codex that
is `sandbox_mode`; `approval_policy` is left alone because `codex exec` never prompts.

**"Structured output"** — one word for two mechanisms that fail differently.
Codex constrains the decoder itself: the final `agent_message` cannot be invalid JSON, but
it can be schema-valid nonsense, and a schema nothing satisfies makes the run generate
until it hits its output ceiling and reports a failed turn. Claude Code offers the model a
tool, validates each call against the schema, feeds a rejection back and lets it retry —
and when it gives up it answers in prose, on exit 0, calling the run a success.
Resolution: the library reports the OUTCOME, never the mechanism. A payload is
`Result.Structured`; its absence on a run that required one is an **unmet constraint**.
Nothing in the library derives behaviour from which of the two produced it.

**"Tool allowlist"** — assumed to exist everywhere because Claude Code has one.
Resolution: it is a **Capability**, not a given. Codex has no per-tool allowlist of any
kind, so it refuses `AllowedTools` rather than accepting and discarding it.

## Example dialogue

> **Dev:** Codex exited 1 and the stream ends in `turn.failed`. Is that an outage?
> **Domain expert:** No — it ran, we understood it, and it told us it failed. That's a
> **Verdict**. Populated **Result**, `IsError` true, nil error.
> **Dev:** And the run where the agent said "I couldn't create out.txt, this workspace is
> read-only"?
> **Domain expert:** Exit 0, `turn.completed`. That's a **Refusal**, and a refusal is a
> successful verdict — the sandbox did its job. `IsError` stays false.
> **Dev:** So when *is* it an outage?
> **Domain expert:** When there's no stream to read. Pass a flag codex doesn't know and you
> get exit 2, empty stdout, and a usage message on stderr. Nothing there is a statement
> about the request, so that's an **Outage**.
> **Dev:** Last one — both reported one turn. Same thing?
> **Domain expert:** Different units entirely. Claude counted one loop iteration; codex
> counted one whole loop. Don't compare them.
