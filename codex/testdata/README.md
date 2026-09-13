# Fixture provenance

Every file here is the output a decoder has to survive, and its provenance decides
how much it proves.

**Captured** files are raw output from a real run of the pinned CLI (codex-cli
`PinnedVersion`, see `codex/release.go`). They are the standard, because a fixture
written to match a decoder proves only that the decoder matches itself.

**Derived** files are built from wire tokens read out of a captured artifact, for an
outcome that cannot be produced on demand. They prove that the decoder keys on the
contract; they do not prove the CLI emits exactly these bytes. A derived fixture is
listed below with the artifact it borrows its shape from, and it is replaced by a
capture the first time one is obtainable.

| File | Provenance |
|---|---|
| `rejected-auth.ndjson` | Captured — a run against a stripped bearer token. Codex retries the WebSocket transport five times, falls back to HTTPS, retries that five times too, and only then reports `turn.failed` with the 401 it saw on the wire. |
| `sandbox-refusal.ndjson` | Captured — a run asked to write into a read-only workspace. The agent announces its plan, then reports in prose that the write was refused; the turn still completes, because the CLI ran to a normal conclusion and the refusal is the answer. |
| `structured-invalid-schema.ndjson` | Captured — `--output-schema` pointed at a schema whose `type` is not an object. The API's own JSON error document, with a top-level `status`, arrives once as an `error` event and again verbatim inside `turn.failed`. |
| `structured-preamble.ndjson` | Captured — a structured run whose agent emits a preamble message before the schema-conforming answer. Confirms the decoder keeps the LAST `agent_message`, not the first. |
| `structured-unsatisfiable.ndjson` | Captured — a run whose response is cut off mid-generation (`max_output_tokens`) before ever producing a schema-conforming answer. Retries the same five-then-five WebSocket-then-HTTPS pattern as `rejected-auth.ndjson` before `turn.failed`. |
| `structured-unreadable-schema.stderr` | Captured — `--output-schema` pointed at a file that is not valid JSON. Codex never starts a turn; it exits with this line on stderr before the JSON stream would begin. |
| `structured.ndjson` | Captured — a structured run that answers, on its own, in the required shape after two tool calls. |
| `success-mini.ndjson` | Captured — a plain run against a smaller pinned model, answering a trivial prompt with no tool use. |
| `success.ndjson` | Captured — a plain run against the default pinned model, answering a trivial prompt with no tool use. |
| `tool-use.ndjson` | Captured — a run that shells out once (`cat`) and reports on the result. Each `command_execution` item arrives twice: `item.started` with no output, then `item.completed` with what the command printed. |
| `turn-failed-model.ndjson` | Captured — a run naming a model codex has no metadata for. Codex falls back to default metadata and continues, but the API rejects the model outright; the 400 arrives the same way as `structured-invalid-schema.ndjson`'s. |
| `usage-error.stderr` | Captured — `codex exec` invoked with a flag it does not recognize. Codex exits before ever producing a JSON stream. |
| `blocked-exhausted.ndjson` | Derived — mirrors the envelope of `rejected-auth.ndjson`, with `turn.failed`'s `error.message` replaced by a ChatGPT-plan usage-limit phrase and no embedded status, which is the form the API uses when the transport never produces a status-bearing error document. |
| `blocked-exhausted-status.ndjson` | Derived — the same five-then-five WebSocket-then-HTTPS retry shape as `rejected-auth.ndjson`, with every `401 Unauthorized` replaced by `429 Too Many Requests`. |
| `blocked-exhausted-json.ndjson` | Derived — the same API error document shape as `structured-invalid-schema.ndjson` and `turn-failed-model.ndjson`, with `status: 400` replaced by `status: 429` and a usage-limit message. |

Producing an exhausted run on demand means genuinely spending a subscription window,
which is why the three `blocked-exhausted*` fixtures are derived. Nothing else here is.
