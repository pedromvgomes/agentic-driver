# agentic-driver

A Go library for driving headless coding-agent CLIs. The library owns the **process**
(argv assembly, timeouts, exit-code interpretation, stderr redaction); a **Provider**
owns the **dialect** (flag spelling, event schema, credential variables). Providers live
in their own subpackages — `claudecode`, `codex` — and declare capabilities by which
interfaces they implement, discovered by type assertion rather than a boolean field or a
switch on provider ID.

Full usage docs are in [README.md](README.md). Vocabulary that means one specific thing
in this repo (Provider, Capability, Verdict, Outage, Block, Refusal, etc.) is fixed in
[CONTEXT.md](CONTEXT.md) — read it before naming something, since several of these terms
have a different everyday meaning that is deliberately avoided here.

## Commands

- **Build**: `go build ./...`
- **Vet**: `go vet ./...` and `go vet -tags integration ./...`
- **Unit tests**: `go test -race -count=1 ./...`
- **Integration tests** (drives real CLIs, costs money, not run in CI): `go test -tags integration ./...`

## Project structure

- Root package (`agentic-driver`) — the `Driver`, `Provider` interface, `Request`/`Result`/`Event`, environment and process handling. Identical for every provider.
- `claudecode/` — the Claude Code CLI dialect. Complete: implements every optional capability interface it can (`Pinner`, `Installer`, `ModelResolver`, `SchemaConstrainer`, `BlockReporter`, ...).
- `codex/` — the Codex CLI dialect. `codex.New` vendors a pinned binary on darwin/linux; `codex.NewOnPath` runs whichever is on PATH (Windows always uses this). Declares no `TurnLimiter`, `AgentDefiner`, or `Installer`; `PermissionArgs` refuses `AllowedTools` outright.
- `agentictest/` — a scripted fake binary used by unit tests to assert argv, environment, stdin, and working directory without spawning a real CLI.
- `docs/adr/` — architecture decision records. Read the relevant ADR before changing behavior it governs; each one records the rejected alternative, which is the reasoning a diff alone won't show.

## Conventions

- **Capabilities, not flags.** A provider that can do something implements the interface for it; a provider that cannot simply doesn't implement it. Never add a boolean "supports X" field or a switch on provider identity — callers assert on the interface (see `driver.DetectableBlocks()`, `driver.MaxConcurrentRuns()` in README.md for the pattern).
- **Compile-time capability proof.** Every provider file ends with a block of `var _ agentic.SomeInterface = (*Type)(nil)` assertions. When you add a capability to a provider, add its assertion there too — for both the vendoring type and its `*PathProvider` counterpart when applicable.
- **Golden fixtures over hand-rolled ones.** `claudecode/testdata` and `codex/testdata` hold real captured CLI output (see each dialect's `testdata/README.md` for provenance). Decoders are tested against what they actually have to survive, not against JSON invented for the test.
- **Never key on prose to answer a supported question**, except where an ADR has recorded a specific, narrow exception (see ADR 0006 and ADR 0007). If you find yourself adding a new prose-parsing branch, check whether the question already has a modelled field elsewhere first, and write the ADR if it genuinely doesn't.

## Git workflow

- Conventional commits (`feat:`, `fix:`, `docs:`, `test:`, ...), scoped to the package touched, e.g. `feat(codex): ...`.
- A release is an annotated tag on `main`; the tag message's subject is the version and everything after it is the release notes. The notes are committed first as `docs/releases/<version>.md` in the pull request that makes the release, and the tag body is cut from that file (see README.md "Releasing").

## Boundaries

### Always do
- Add compile-time interface assertions when a provider gains a capability.
- Add or update golden fixtures under `<dialect>/testdata` when decoder behavior changes, and record their provenance in that directory's `README.md`.
- Write or update an ADR when introducing a new exception to an existing documented rule (e.g., a new dialect that must read a status out of prose).

### Ask first
- Adding a new capability interface to the root package (it's a contract every provider is measured against).
- Changing the credential/environment allowlist for either dialect.

### Never do
- Compare `agentic.Result` with `==` (it carries a `json.RawMessage`); use `reflect.DeepEqual`.
- Commit secrets, tokens, or captured CLI output that embeds real credentials.
- Run `go test -tags integration ./...` as part of an automated check — it drives real, billed CLIs.
