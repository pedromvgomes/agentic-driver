---
about: cmd.Stdin's copy goroutine is released by EPIPE when the direct child exits or is killed; WaitDelay only bounds a stdin write pending against a surviving descendant that inherited stdin and never reads it
saw:
  - driver.go
  - driver_test.go
---

Verified empirically while adding `Invocation.Stdin` and wiring it into `Driver.command`
(`cmd.Stdin = bytes.NewReader(inv.Stdin)` when non-nil). The obvious assumption — that a
child which never drains a large piped stdin is bounded by `cmd.WaitDelay` (`killGrace`,
5s, driver.go) the same way a child that holds stdout open is — is only half right, and the
half that's wrong matters for how fast a run returns.

When the *direct* child process exits (cleanly or via the timeout's kill), the kernel closes
the read end of the stdin pipe immediately. The pending write in Go's stdlib copy goroutine
then fails with EPIPE, `exec` discards that specific error on the stdin copy, and `cmd.Wait`
returns right away — `WaitDelay` is never actually invoked on this path. A test that runs a
fake ignoring a >64KiB stdin payload and asserts on the RESULT (success or the right error)
cannot tell this apart from the WaitDelay-expiring case; only asserting on ELAPSED TIME
(should be near-instant, not near the 5s grace) proves which path fired. See
`TestAChildThatIgnoresStdinStillFinishes` and `TestATimeoutKillsAChildStillOwedStdin` in
driver_test.go, which assert `elapsed < killGrace` for exactly this reason.

`WaitDelay` genuinely is needed, but only for a narrower case: a DESCENDANT that inherited
the pipe's write... no — inherited the read side is irrelevant; what matters is a background
job (e.g. spawned via `SpawnChild`/`sh -c '... &'`) that inherits the parent's stdin fd and
outlives the direct child, without ever reading it. In that case the pipe's read end stays
open after the direct child exits, so the copy goroutine's write doesn't get EPIPE — it stays
blocked until WaitDelay's deadline forcibly closes the pipes. This was confirmed with a
throwaway (unmerged) script during development: `exec 3<&0; sleep 8 0<&3 3<&- &`, which
measurably took the full grace period whereas a plain non-reading child returned in
milliseconds. `agentictest.Fake`'s `IgnoreStdin` knob cannot express this surviving-descendant
case (POSIX `sh` gives a backgrounded job `/dev/null`, not the inherited fd) — a committed
test for it would need a hand-written script or a new Fake knob, and none exists yet.
