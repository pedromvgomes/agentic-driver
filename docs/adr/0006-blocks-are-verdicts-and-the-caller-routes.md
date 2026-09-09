# A block is a verdict about the credential, and the caller routes on it

Every bad outcome this library reports is a statement about the request: the agent failed
the task, the sandbox forbade an action, the schema went unsatisfied. One class is not. When
a subscription window is spent or a credential is rejected, the request was never considered
at all — and it is the only class where trying the same request against a DIFFERENT provider
is the correct response.

`IsError` cannot carry that distinction, because it is the same `true` for a run that failed
the task. A caller falling back on it burns a second subscription on work that was going to
fail anyway; a caller not falling back sits blocked with an unused credential in hand. So
`Result.Blocked` names the reason — `BlockExhausted`, which lifts on a clock and carries
`ResetsAt`, or `BlockRejected`, which never lifts without a human — and `BlockReporter`
declares which of them a dialect can actually recognise. The library signals; it does not
route.

## Considered options

**Report a block as an outage.** It is the reading the exit code invites, and it is
backwards. The glossary's test for an outage is that nothing there is a statement about the
request, and "this credential is spent" is the most definitive statement available. An outage
tells a caller "unknown failure, perhaps retry" at the exact moment the truth is "definitively
blocked, go elsewhere" — opposite instructions, from the one outcome where getting it right
is worth the most.

**Model exhaustion alone, as `Result.Exhausted`.** The narrow feature that was actually
asked for. Rejected because the caller's question is not "am I exhausted" but "should I try
the other provider", and a rejected credential answers that identically — while a failed task
answers it in reverse. Since `api_error_status` is already decoded and `rejected-auth.json`
already exists in both providers, the second reason cost almost nothing; a bespoke field for
the first would have grown a sibling within a quarter. A reason earns its place in the
vocabulary only where the caller's correct response differs, which is also why a transient
throttle is not a member: the CLIs absorb their own backoff internally and it never surfaces
as a terminal verdict.

**Let the driver fall back to a second provider.** The feature as originally framed, and the
driver cannot honour it. A `Driver` binds one provider and resolves one binary at `New`, so
`Ready`, `Binary` and `MaxConcurrentRuns` would each start answering for a CLI other than the
one that ran. Worse, a `Request` is not portable: `AllowedTools` and `PermissionMode` are
spelled in the provider's own vocabulary, `SessionID` does not cross providers at all, and
`MaxTurns` is counted in units that are not comparable. Retrying "the same request" elsewhere
would silently run a different one — the exact silent substitution every capability gate in
`prepare` exists to prevent — and reporting only the second attempt's `Usage` would describe
a price nobody was charged. Composition over two drivers belongs above this library, and must
not present itself as a `Driver`.

**A marker interface with no methods.** Satisfied by every type in Go, so the assertion would
succeed for a provider that has never heard of the feature and would assert nothing at all.
`DetectableBlocks` carries the SET rather than a yes for the reason `Installer.SigningIdentity`
carries a name: a dialect commonly reads one reason and not another, and the set is the only
thing the assertion cannot answer by itself.

## Consequences

A caller that wants to route asks `DetectableBlocks` before it builds a chain, because
the two providers answer differently and the difference is not incidental. Claude Code
reports a blocked run the way it reports a rejected token — `subtype: "success"`,
`is_error` set, and the HTTP status in `api_error_status` — so a status is the whole
signal and both reasons are readable. Codex's only event stream is `codex exec --json`,
whose terminal `turn.failed` carries a prose message and nothing else; the error-code
vocabulary the CLI keeps internally never reaches the wire, so codex implements
`BlockReporter` not at all and a chain that depends on it would never fire. That absence
is the capability doing its job, and it is asserted by a test rather than left to be
noticed.

Recognition keys on a wire token — a status code — and never on display prose, which is
localised and rewritten between releases. The rule is what makes the vocabulary
extensible without becoming a substring hunt, and it is also what rules codex out today:
a dialect that matched the English in `error.message` would recognise exactly the wording
it was written against.

Evidence for a reason may come from the pinned artifact rather than a captured run, since
producing an exhausted run on demand means genuinely spending a subscription window.
`testdata/README.md` records which fixtures are derived and from what, and a derived
fixture is replaced by a capture the first time one is obtainable.

A block that arrives with no envelope at all has no home yet. The classification is
settled — "this credential is spent" is a statement about the request, so it is a verdict
wherever it is written — but `Decoder` sees only stdout, and stderr and the exit code
reach `streamEnd`, which can return nothing but an outage. Closing that gap means a second
capability that turns stderr into a `Block`, and it stays unbuilt until a run is observed
taking that path: neither provider has produced one, and a mechanism built for an outcome
nobody has seen would be a dialect written from memory with driver machinery attached.
Until then a block of that shape is reported as an outage, which understates it.
