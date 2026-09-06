# Structured output is a capability, and a missing payload is a verdict

Both CLIs can bind a run's final answer to a JSON Schema, and both genuinely constrain it
rather than asking nicely: given a schema requiring `count` between 1000 and 2000 and a
prompt insisting on `4`, each returns a conforming document. So `Request.Schema` is real
across providers, and a caller can fan out N reviewer runs over a diff and unmarshal what
comes back.

It is a **capability**, gated by `SchemaConstrainer` and refused with `ErrSchemaUnsupported`
before a process starts, for the reason every gate here exists: a dropped schema fails
silently and in the direction that looks like success. The run answers the prompt in prose,
competently, and nothing in the reply marks the constraint as never applied — the caller
finds out wherever it tries to unmarshal that prose, which is somewhere else entirely.

The two CLIs constrain by different mechanisms, and the mechanisms fail differently.
Codex constrains the decoder itself: `--output-schema` yields a final `agent_message` that
cannot be invalid JSON, though it can be schema-valid nonsense — padding a `minItems: 7`
array with `":["` — and a schema nothing satisfies makes the run generate until it hits its
output ceiling, reconnect ten times, and report `turn.failed`. Claude Code offers the model
a tool, `StructuredOutput`, validates each call against the schema, feeds
`Output does not match required schema` back and lets it retry. When it gives up it answers
in prose on **exit 0**, with `is_error: false`, `subtype: "success"`, and no
`structured_output` field.

That last case is the one this record exists for. Taken at the CLI's word it is a clean
success whose `Text` happens not to be JSON, and every downstream caller inherits the
problem. So a run given a schema that produces no payload is reported as a bad verdict:
populated `Result`, nil error, `IsError` set, `Structured` nil, and `Text` carrying the
agent's own account of why it could not answer in the required shape. It is the only
verdict the library reaches on its own, and it is deliberately not an outage — the run
happened, it cost money, and the agent's explanation is worth reading.

Judging it needs the request, so `NewDecoder()` becomes `NewDecoder(Request)`. This amends
ADR 0001, which established the mandatory interface. A decoder is already documented as
per-run and stateful; what it lacked was the run's terms. Each provider then reads its own
authoritative signal rather than a shared guess: claudecode checks whether the envelope
carries `structured_output`, codex whether the final agent message is a document.

The schema reaches each CLI differently — claude takes it inline, codex takes a path — so
the codex provider writes the file itself, named for the SHA-256 of its contents under one
directory in `TMPDIR`. The digest is what keeps a file from being a per-run side effect:
the same schema always renders the same argv, two runs sharing a schema share the file, and
there is nothing per-run left behind for anyone to reclaim.

## Considered options

**A plain `Request.Schema` every provider honours, with no interface.** Smaller, and
nothing speculative: both providers implement the capability today, so the gate never fires.
Rejected because the gate is not for today. A provider that cannot constrain output and
merely drops the field produces the one failure this library is organised around — an
answer that is confidently wrong about having followed instructions — and leaving that to
per-provider discipline is what a type could have prevented.

**The driver deciding, by testing whether `Result.Text` parses as JSON.** No interface
churn at all. Rejected because it throws away the signal Claude Code actually gives.
`structured_output` present-or-absent is the CLI's own statement about whether the
constraint held; whether some text parses is an inference about it. Preferring the
inference is how a dialect gets written from assumption rather than from what the CLI said,
and it would put that judgement in `driver.go`, which owns process and not content.

**A per-run temporary directory plus an `Invocation.Cleanup` hook the driver defers.**
The obvious shape, and the one that binds the file's life to the run's. Rejected on two
counts: it makes argv nondeterministic, so the same `Request` no longer renders the same
command and `Run` and `Stream` each mint a different one; and it leaks whenever
`StreamCommand` is called and no run follows — an environment that fails to build, or a
test asserting on argv. A content-addressed file has no per-run identity to leak.

**Validating the schema properly, with a JSON Schema library.** It would make both CLIs
behave identically on a bad schema, and would catch codex's expensive failure before it
was paid for. Rejected for the dependency, and for the second opinion that comes with it:
a validator here can disagree with the CLI that has to honour the document, in both
directions. `Driver.prepare` checks `json.Valid` and stops — that much is stdlib, and it
makes the *malformed* case symmetric without anyone claiming to know what a usable schema
is.

## Consequences

`Result` grows `Structured`, and stops being comparable with `==`; tests that compared two
`Result` values now use `reflect.DeepEqual`.

The two CLIs still disagree about what an unusable schema IS, and the library does not
hide it. A document that is well-formed JSON but not a usable schema is an outage on
claudecode, which validates locally and exits before spawning, and a verdict on codex,
which sends it and receives a 400 inside a turn that starts and then fails. Both readings
are correct about their own CLI, and normalising them would mean overriding one vendor's
account of its own behaviour.

`StructuredOutput` is exempt from Claude Code's `--allowedTools`, so a run restricted to
`Read` still answers in the required shape. `PermissionArgs` and `SchemaArgs` therefore do
not reconcile, and a test pins that: refusing the combination would refuse requests the CLI
honours perfectly well.
