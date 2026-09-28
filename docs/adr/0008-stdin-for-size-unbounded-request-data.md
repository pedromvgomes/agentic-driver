# Stdin carries the prompt; argv stays flag-shaped

Linux's `execve(2)` caps a single argv element at `MAX_ARG_STRLEN`, 128 KiB. A review
prompt that embeds a full diff routinely exceeds that on its own, and a run built from one
never reaches the CLI at all — it fails at spawn, as `fork/exec ...: argument list too
long`, before a single byte of the request is read. The failure never reproduced on macOS,
whose `execve` caps aggregate argv-plus-env with no limit on a single element, so the bug is
Linux-specific. The fix below is not: it works identically on both platforms, because it
does not depend on which cap a kernel happens to enforce.

The prompt now travels on `Invocation.Stdin`, for both `claudecode` and `codex`. Argv keeps
everything that is genuinely flag-shaped — `-p`, `--model`, a schema path, `--allowedTools`
— and nothing whose size scales with the size of the work being asked about.

The mechanism is in the root package, not either dialect. `Invocation` (provider.go) gained:

```go
// Stdin is piped to the child's standard input, or is nil to leave it on
// the null device. A payload travels here rather than in Args because the
// kernel caps a single argument (128 KiB on Linux), and a prompt carrying
// a whole diff exceeds that and fails at exec.
Stdin []byte
```

`nil` means exactly what it meant before this field existed: the child's stdin stays on the
null device, untouched. Non-nil — including an empty, zero-length slice — means the driver
pipes it through. `Driver.command` is the one place this is wired:

```go
if inv.Stdin != nil {
    cmd.Stdin = bytes.NewReader(inv.Stdin)
}
```

A reader rather than a file, so `exec` copies it through a pipe on a goroutine of its own
rather than handing the child an fd backed by memory; a child that exits without reading it
closes the pipe and the failed write is simply not reported. Every other dialect concern —
which flag introduces the prompt, whether the CLI reads stdin at all when one is present —
stays where it belongs, in `claudecode.StreamCommand` and `codex.StreamCommand`.

## Considered options

**An environment variable.** Rejected because it does not actually solve the problem: on
Linux, `ARG_MAX` bounds argv and environment together, so a prompt too large for one argv
element is not obviously safer inside one environment variable — the ceiling moves, it does
not lift. A secondary concern was raised and set aside: an environment variable is exposed
to another user on the same machine through `/proc/<pid>/environ` under some configurations,
which is not a new leak relative to argv (already readable via `ps`) so much as it is just
as exposed rather than less. Neither of those is the deciding reason. The deciding reason is
that an env var does not solve the actual failure mode — it fails at a slightly different
size, in the same place, for the same reason.

**A temp file, with its path passed as an argv flag.** Rejected for what it costs beyond the
pipe it would replace: filesystem I/O on every run, a cleanup obligation for a file with no
natural owner once the process that wrote it has exited or been killed, and a race if two
concurrent runs picked a predictable path. A pipe needs none of that — there is no artifact
to clean up, because there is nothing on disk to begin with, and the OS reclaims it the
moment the process ends.

Stdin was chosen over both because it has no size cap of its own — bounded only by available
memory and pipe buffering, not a fixed OS constant — needs no cleanup, and every CLI this
library drives already reads a prompt from it: `claude -p` and `codex exec` both fall back
to stdin for the prompt precisely when it is not given as an argument. That behavior was
verified empirically against the real, pinned binaries during planning, not assumed from
documentation.

`claudecode`'s `--allowedTools` is a comma-joined `req.AllowedTools` list that still travels
in argv and has the same theoretical 128 KiB exposure in principle — a tool allowlist large
enough to hit it has never been observed, and fixing it is out of scope here. It is noted,
not fixed.

## Consequences

The prompt is deliberately absent from `Invocation.Args` for both dialects now. A future
change must not "restore" it to argv for either — doing so silently reintroduces the exact
failure this ADR fixes, on Linux only, which makes it the kind of regression that is easy to
miss on a macOS development machine. Nothing else guards against that beyond the regression
tests `claudecode/provider_test.go` and `codex/provider_test.go` both carry under the name
`TestAPromptLargerThanAnArgvElementReachesTheCLIOnStdin`, which build a prompt bigger than a
single Linux argv element and assert it arrives on `agentictest.Fake`'s captured stdin rather
than in captured argv.

`Invocation.Args` no longer containing the prompt is an exported-behavior change: any caller
that inspected `StreamCommand`'s returned argv for the prompt text sees a different shape now.
It is worth a line in release notes the next time one is cut — not done as part of this
change, so `docs/releases/` is untouched here. The known downstream consumer is
`agentic-toolkit`, currently pinning this repository at v0.9.0.
