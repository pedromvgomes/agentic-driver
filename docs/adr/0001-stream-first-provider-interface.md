# Every provider streams, and Run is a fold over Stream

*Amended by ADR 0003: `NewDecoder` takes the `Request`, so a per-run decoder knows what its
run was required to produce.*

Codex has no result envelope: `codex exec --json` emits JSONL from the first line to the
last, and `-o/--output-last-message` writes bare text to a file rather than a document to
stdout. A `Result` for codex therefore exists only as a fold over a whole run — the session
id arrives on the first line, the answer on the last `agent_message`, the token usage on
the terminal line. Rather than let one provider be envelope-shaped and the other
stream-shaped, streaming becomes mandatory on `Provider`: `StreamCommand` is the single
argv builder, a stateful `Decoder` consumes the run, and `Run` is `Stream` folded to its
terminal `Result`. One code path serves both a caller that wants only an answer and a UI
that wants to watch the work.

## Considered options

**A stateless `ParseEvent(line)` plus a surviving `Parse(stdout)`.** Rejected because the
two would be separate authorities for the same `Result` and could drift. It was also the
shape that made codex awkward: a stateless per-line decoder cannot build codex's `Result`,
because its terminal `turn.completed` line carries usage and nothing else — no thread id,
no text.

**Keeping two invocations, one per mode, sharing a decoder.** Rejected because `Run` would
then emit no intermediate events on Claude Code, and each provider would maintain two argv
builders free to diverge. The cost of rejecting it is real and accepted: Claude Code's
`Run` now spends `--output-format stream-json --verbose` where it previously spent
`--output-format json`. The two carry identical field sets, so no information is lost.

## Consequences

A terminal result outranks the exit code **and** the timeout, in `Stream` as it already did
in `Run`. Without this the fold would destroy the library's central distinction: codex
reports a rejected credential and an unsupported model as exit 1 with a perfectly
well-formed stream, and Claude Code exits non-zero on a rejected credential while still
printing a complete envelope. Under the old `streamEnd` rule — any non-zero exit is
`ErrProviderUnavailable` — every one of those verdicts would have become a spurious outage.
`streamEnd` now errors only when no terminal result arrived at all.
