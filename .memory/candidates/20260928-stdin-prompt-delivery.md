---
about: no ADR governs stdin transport; Invocation is a plain struct with one producer (StreamCommand) and one consumer (Driver.command); WaitDelay already bounds a stdin-copy goroutine; both dialects hard-code Prompt as the last argv element
saw:
  - provider.go
  - driver.go
  - stream.go
  - process_unix.go
  - process_other.go
  - claudecode/provider.go
  - codex/provider.go
  - agentictest/fake.go
  - docs/adr/0001-stream-first-provider-interface.md
---

Researched for a plan to move `Request.Prompt` from an argv element to the
child's stdin (Linux execve caps one argv element at 128 KiB; prompts embed
whole diffs). No ADR in `docs/adr/` governs stdin at all — 0001
(stream-first) only settles that `StreamCommand` is the single argv
builder and `Run` folds `Stream`; 0002–0007 are all about capability
interfaces (turn limits, schema, pinning, concurrency, blocks), not the
process/transport layer. Adding stdin support is new territory, not an
exception to a documented rule, so it doesn't fall under the
narrow-exception clause that requires an ADR — but it changes something
0001 didn't anticipate (a second producer of child input alongside argv),
which is itself a reason to write one when the change lands.

`Invocation` (provider.go:429) is plain data — `Args []string` and
`Env map[string]string` — built exactly once per run by
`Provider.StreamCommand` (provider.go:36) and consumed exactly once by
`Driver.command` (driver.go:427) and `Driver.Stream` (stream.go:50, passes
`inv.Args` positionally into `d.command`). Nothing compares or copies an
`Invocation` value anywhere in the tree (only `Result` has the `==`-forbidden
`json.RawMessage`, per CLAUDE.md — `Invocation` carries no such field). A new
field is additive for every existing caller: Go call sites use keyed
struct literals (`Invocation{Args: ..., Env: ...}`, see driver_test.go:857)
so an unset new field just zero-values. This is a data-type extension, not
a new capability interface — the ask-first rule in CLAUDE.md
("Adding a new capability interface to the root package") is textually
about optional-behaviour interfaces discovered by type assertion, and a
struct field on `Invocation` is neither optional nor discovered that way.

`Driver.command` (driver.go:427-448) never sets `cmd.Stdin`, so today every
child's stdin is Go's default: the null device. Confirmed no dialect ever
reads stdin (grepped both provider.go files — no os.Stdin / Fd(0) use).

Go's `os/exec.Cmd.Wait` already has to cope with a non-`*os.File` `Stdin`:
it starts a copy goroutine and `Wait` does not return until that goroutine
sees EOF or an error, in addition to the process exiting. `cmd.WaitDelay`
is already set to `killGrace` (5s) at driver.go:445-445, and per the
stdlib's own contract WaitDelay bounds ALL of a Cmd's I/O-completion
goroutines (stdout, stderr AND a non-file stdin's copy goroutine) after the
process's wait-status is ready, forcibly closing the pipes once it
expires. So a stdin write that blocks because a child never reads its
prompt is already covered by the existing kill-grace mechanism — nothing
new needs to be built for that failure mode, it falls out of the field
already in place for the stdout-holds-open case the comment at driver.go
describes.

Both dialects append `req.Prompt` as the LAST positional argv element —
`claudecode/provider.go:191` and `codex/provider.go:291` — and several
tests assert that position explicitly (`codex/provider_test.go:253-258`,
similar patterns in `claudecode/scripted_test.go`/`parse_test.go`). Moving
Prompt to stdin means rewriting every test that indexes `inv.Args[last]`
as the prompt.

`agentictest.Fake` (agentictest/fake.go) writes a real `#!/bin/sh` script
and runs it via `exec.Command` (fake.go:122), so it goes through a genuine
execve — an oversized-argv test against it would reproduce Linux's E2BIG
for real, not a simulation. It does not currently capture stdin; the
`record()` shell snippet (fake.go:205-212) already builds up a small
here-doc-style block per invocation, so adding a `cat > stdin.txt` (or
`wc -c`) line to capture what arrived on fd 0 fits its existing design —
no restructuring needed.
