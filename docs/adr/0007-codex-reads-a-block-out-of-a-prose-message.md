# Codex reads a block out of a prose message

Codex implements `BlockReporter` and claims both reasons, `BlockExhausted` and
`BlockRejected`. It reads them out of the single `error.message` string on `turn.failed`,
which narrows the rule stated in
[0006](0006-blocks-are-verdicts-and-the-caller-routes.md) — recognition keys on a wire
token and never on display prose — for this dialect alone. 0006's reasoning stands as
written and stays correct for claudecode, which has a modelled field to read and no reason
to look anywhere else.

The narrowing is forced by the surface. `codex exec --json` carries no structured field for
the outcome: `turn.failed` has a message and nothing beside it, and the CLI's typed error
vocabulary — `CodexErrorInfo`, with `usage_limit_exceeded`, `unauthorized` and the rest —
exists only on its app-server JSON-RPC surface, which this library does not use. The choice
is not between prose and a field; it is between prose and reporting a spent subscription
window as an ordinary failed turn, which is the one outcome where that mistake costs a
caller a whole fallback chain.

## Considered options

**Key on the message as a whole.** What "matching the prose" usually means, and the thing
0006 rules out for good reason. Rejected in favour of a priority order that treats most of
the message as untrustworthy and looks for the two places a real wire token is stamped into
it:

1. The message parses as JSON with a top-level integer `status`. This is the API's own
   error document, quoted verbatim — the `status` is the status of the request that was
   refused, not English about it.
2. The message contains `unexpected status <code>`. This is codex's transport layer
   reporting the code it saw on the wire when no document came back to quote. The digits
   are the token; the sentence around them is scenery.

A status recovered either way decides alone: 429 is `BlockExhausted`, 401 is
`BlockRejected`, and any other status is not a block at all. A 400 for an unsupported model
or a malformed schema is a verdict about the REQUEST, and routing the same request
elsewhere would only reach the same refusal.

Only the third step is a genuine prose match: a case-insensitive `usage limit`, the phrase
a ChatGPT-plan allowance produces when the transport never yielded a status-bearing
document. It runs last and only when NEITHER status form matched, because that ordering is
what keeps it safe — "usage limit" appearing inside the explanation of a 400 would
otherwise promote a bad request into a spent allowance.

**Also match "out of credits".** A phrasing codex-cli can emit for a spent allowance, and
deliberately left unmatched. No captured run of it exists, and without one it fits neither
reason's contract: a credit balance at zero does not obviously lift on a clock the way
`BlockExhausted` promises, nor is it an invalid credential a human must replace the way
`BlockRejected` promises. Guessing would put a reason in front of a caller that routes on
it, so the outcome stays unmodelled until evidence says which one it is.

**Read a transport 429 as something other than exhaustion.** A 429 on the wire can mean the
CLI exhausted its own retry budget rather than that the subscription window is spent, and
the dialect reports `BlockExhausted` for both. The conflation is accepted rather than
overlooked: claudecode already makes exactly the same one for its own `api_error_status`
429, so codex is following existing precedent instead of introducing a second judgment
about what a 429 means. A caller that routes on either one routes the same way.

## Consequences

`ResetsAt` is always zero. The exec stream carries a reset time in no form at all. The
rollout files under `$CODEX_HOME/sessions` do hold a `rate_limits` snapshot, but they are a
different stream, written to disk rather than to stdout, and this dialect reads only what
the run prints. `Reason` already says whether the block lifts on a clock, which is the part
a caller routes on.

The three `blocked-exhausted*` fixtures in `codex/testdata` are derived rather than
captured, because producing an exhausted run on demand means genuinely spending a
subscription window. Each borrows its envelope from a captured artifact and changes only
the tokens under test; `codex/testdata/README.md` records which artifact each one borrows
from. This is the provenance rule 0006 already sets out, and each fixture is replaced by a
capture the first time one is obtainable.

A phrase match is only as current as the phrase. An integration-tagged test scans the
pinned binary for the wording the fallback depends on, so a release that rewrites it fails
a test rather than silently reporting spent allowances as ordinary failed turns. That test
is the price of the narrowing, and it is why the narrowing is confined to one phrase
reached last.
