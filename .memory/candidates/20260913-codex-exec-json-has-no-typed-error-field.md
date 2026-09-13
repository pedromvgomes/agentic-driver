---
about: codex/parse.go
saw: codex-cli 0.153.4, `codex exec --json` stream
---

`codex exec --json`'s `turn.failed` event carries only a single prose `error.message`
string — no structured error field. Codex-cli's typed error vocabulary
(`CodexErrorInfo`: `usage_limit_exceeded`, `unauthorized`, ...) exists only on its
app-server JSON-RPC surface, which this library does not use. Any dialect code that
wants to classify a `turn.failed` failure (e.g. `blocked()` in `codex/parse.go`) has to
recover a signal from inside that one string — there is no field to add a case for
instead.

Two shapes of embedded status are wire tokens rather than prose: a JSON body with a
top-level `status` field (the API's own error document, quoted verbatim into the
message), and the substring `unexpected status <code>` (codex's transport layer
reporting what it saw on the wire when no document came back). `blocked()` tries both
before ever touching prose, and only falls back to a case-insensitive `"usage limit"`
phrase match when neither status form is present — see `docs/adr/0007-codex-reads-a-block-out-of-a-prose-message.md`
for why this is a deliberate, narrow exception to `docs/adr/0006-blocks-are-verdicts-and-the-caller-routes.md`'s
"never key on prose" rule.

`ResetsAt` on a codex-reported `Block` is always zero: the exec stream carries no reset
time in any form. (Contrast with `$CODEX_HOME/sessions` rollout files, which do carry a
`rate_limits` snapshot, but that is a different stream this dialect does not read.)
