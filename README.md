# agentic-driver

A Go library for driving headless coding-agent CLIs.

The library owns the **process**; a provider owns the **dialect**.

- **Process** — argv assembly, context and timeout handling, exit-code
  interpretation, stderr redaction, environment construction. Written once, in
  this package, and no provider touches it.
- **Dialect** — flag spelling, event schema, the environment variables that can
  hijack a given CLI, resume semantics. Declared per provider.

Providers live in their own subpackages (`claudecode`, `codex`) and declare
their capabilities by which interfaces they implement, discovered by type
assertion rather than by a boolean field or a switch on provider ID.

## Usage

On a developer machine, run the CLI that is already installed and already
authenticated:

```go
provider, err := claudecode.NewOnPath()
if err != nil {
    return err
}

driver, err := agentic.New(provider, agentic.WithModel("opus"))
if err != nil {
    return err
}

result, err := driver.Run(ctx, agentic.Request{Prompt: "What does this repo do?"})
if err != nil {
    return err
}
fmt.Println(result.Text, result.Usage.CostUSD)
```

## Where the binary comes from

Two providers, one dialect, differing in the capability that actually separates
them:

- `claudecode.NewOnPath()` runs whichever `claude` is on PATH. It implements no
  `Installer`, because a provider that runs someone else's binary cannot claim
  the guarantee that comes with installing one.
- `claudecode.New(providersRoot)` installs its own copy at a pinned version,
  verified against Anthropic's signed manifest, and executes it by absolute
  path — so no PATH entry and no repointed symlink can substitute a build
  nobody verified.

A vendoring provider is constructed before its binary exists, because `Install`
is how it gets there. `Driver.Ready()` reports whether a run could actually
start, so "configured" and "runnable" are distinguishable without spawning a
process:

```go
if err := driver.Ready(); err != nil {
    if _, err := driver.Install(ctx, ""); err != nil {
        return err
    }
}
```

`Run` returns an error only when the invocation could not be carried out or
could not be understood. A CLI that ran and reported a failure of its own comes
back as a `Result` with `IsError` set and a nil error — that is a verdict, and
reporting it as an outage sends people hunting a problem that is not there.

`Stream` returns the same run as an `iter.Seq2[Event, error]`, ending with a
terminal event whose `Result` is what `Run` would have produced.

## Choosing a model

`agentic.WithModel` sets the model every invocation uses; `Request.Model`
overrides it for one call. `Driver.Model()` reports the resolved model currently
in effect — the concrete name a request that names none would be answered by —
and is empty when no model has been chosen and the CLI's own default applies.

Where a provider implements `ModelResolver`, a family alias resolves to the
newest build in that family:

| Alias | claudecode resolves to |
| --- | --- |
| `opus` | `claude-opus-5` |
| `sonnet` | `claude-sonnet-5` |
| `haiku` | `claude-haiku-4-5` |
| `fable` | `claude-fable-5-1` |

Anything else is passed through untouched, so a concrete ID works and so does a
family newer than this library.

`Result.Model` reports which model actually answered, which is not necessarily
the one that was asked for — and is what the cost beside it in `Usage` was
charged against.

```go
driver, _ := agentic.New(provider, agentic.WithModel("opus"))
driver.Model()                     // "claude-opus-5"
result, _ := driver.Run(ctx, req)
result.Model                       // what answered
```

## Structured output

`Request.Schema` binds a run's final answer to a JSON Schema. It requires a
provider implementing `SchemaConstrainer`: a request carrying a schema for a
provider that does not is refused by the driver with `ErrSchemaUnsupported`,
before a process starts, rather than answering in prose nothing marks as
unconstrained. Assert on the interface to know whether a provider offers it.

The schema must be a JSON object; anything else is `ErrInvalidRequest`.

```go
result, err := driver.Run(ctx, agentic.Request{
    Prompt: "Review this diff and report each finding.",
    Schema: findingsSchema,
})
if err != nil {
    return err
}
if result.IsError {
    return fmt.Errorf("the run reported a failure: %s", result.Text)
}
json.Unmarshal(result.Structured, &findings)
```

`IsError` does not say *why* a run failed, and an unmet constraint is not
distinguishable from any other bad verdict — a rejected credential and a turn
that ran out of turns both arrive the same way. `Text` is the only account a
caller gets, which is why it is worth propagating.

Both CLIs genuinely constrain the answer rather than suggesting a shape: a
prompt arguing against the schema still comes back conforming. They constrain by
different mechanisms — codex constrains the decoder, Claude Code validates a
tool call and retries — and the difference shows up when the model cannot
satisfy the schema at all. Claude Code eventually answers in prose and reports
the run a success; codex generates until it hits its output ceiling and reports
a failed turn.

Either way the outcome is the same to a caller: `IsError` set, `Structured` nil,
and `Text` carrying whatever account there is — the agent's own on Claude Code,
codex's own on a turn it failed, and a line the library supplies when codex
completes a turn having produced no answer at all. That is an **unmet
constraint**: a verdict, not an outage, because the run happened and whatever
account exists is worth reading.

A sandbox refusal on a schema-constrained run is an **unmet constraint** too.
Refusing is a successful verdict about authority, but it still leaves the caller
without the shape it asked for, and the shape is what this outcome reports.

`Result` carries a `json.RawMessage` and so is not comparable with `==`; compare
with `reflect.DeepEqual`.

## Credential modes

Two, chosen by the caller:

- **Ambient** — inherit the environment the process already has. This is what a
  developer's machine wants: use whatever the CLI is already authenticated
  with. It is the default.
- **Isolated** — build the child environment from a fixed allowlist and hand
  the provider a specific token. The environment is *constructed*, never
  filtered from `os.Environ()`, so a variable nobody thought of cannot arrive
  by accident. The provider supplies the vocabulary — which variables carry
  auth, and which ones can redirect the CLI somewhere else.

```go
driver, err := agentic.New(provider,
    agentic.WithCredentials(agentic.Isolated(token)),
    agentic.WithHome(configDir))
```

## Testing

Three layers, and only the third costs money:

1. **Golden envelopes** — `claudecode/testdata` holds raw output captured from
   the real CLI — success, a rejected credential, a turn limit, a usage error, a
   stream, a constrained answer and a run that gave up on producing one — so
   `Parse` is a pure function tested against what it actually has to survive.
2. **A fake binary** — `agentictest` builds a scripted stand-in that records its
   own argv, environment and working directory. Timeouts, cancellation, exit
   codes, process-group kill and credential isolation are all deterministic.
3. **`go test -tags integration ./...`** — drives the real CLIs. Excluded from
   the default suite and from CI, and run by hand.

## Status

Early. The API is not stable. `claudecode` is complete. `codex` drives
single-turn runs: `StreamCommand`, the decoder, `PermissionArgs`, `SchemaArgs`,
`AuthEnv` and `DenyEnv` are written against captured output from the real CLI.
It declares no `TurnLimiter` (codex has no turn bound), no `AgentDefiner` and no
`Installer`,
and its `PermissionArgs` refuses `AllowedTools` outright — codex has no per-tool
allowlist, and accepting one could only mean discarding it.

## License

MIT
