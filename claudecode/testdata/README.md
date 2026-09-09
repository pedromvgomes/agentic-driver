# Fixture provenance

Every file here is the output a decoder has to survive, and its provenance decides
how much it proves.

**Captured** files are raw output from a real run of the pinned CLI. They are the
standard, because a fixture written to match a decoder proves only that the decoder
matches itself.

**Derived** files are built from wire tokens read out of the pinned artifact, for an
outcome that cannot be produced on demand. They prove that the decoder keys on the
contract; they do not prove the CLI emits exactly these bytes. A derived fixture is
listed below with the artifact and version it came from, and it is replaced by a
capture the first time one is obtainable.

| File | Provenance |
|---|---|
| `blocked-exhausted.json` | Derived — Claude Code 2.1.266. `api_error_status` is the envelope's documented HTTP status for an API error; the result envelope's `subtype` vocabulary is closed (`success`, `error_during_execution`, `error_max_turns`, `error_max_budget_usd`, `error_max_structured_output_retries`) and carries no limit member, so a blocked run reports `subtype: "success"` with `is_error` set, exactly as the captured `rejected-auth.json` does for 401. Every field but the status, the message and the identifiers is that captured envelope unchanged. `Usage limit reached` is a message string present in the same artifact, and nothing keys on it. |

Producing an exhausted run on demand means genuinely spending a subscription window,
which is why this one is derived. Nothing else here is.
