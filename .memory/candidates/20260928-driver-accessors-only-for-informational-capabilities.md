---
about: Driver exposes a public accessor only for capabilities a caller needs to query before building a Request (informational); capabilities that just change what StreamCommand accepts are asserted internally in prepare() with no Driver-level method
saw:
  - driver.go
  - provider.go
---

Driver has exactly three public capability-accessor methods that wrap a type assertion:
`ResolveModel`/`Model` for `ModelResolver` (driver.go:226-237), `MaxConcurrentRuns` for
`ConcurrencyLimiter` (driver.go:239-258), and `DetectableBlocks` for `BlockReporter`
(driver.go:260-278). README.md documents exactly this pair as "the pattern" (`driver.
DetectableBlocks()`, `driver.MaxConcurrentRuns()`, see CLAUDE.md's own citation of them).

`Resumer`, `AgentDefiner`, `Permitter`, `TurnLimiter` and `SchemaConstrainer` get NO
Driver-level accessor at all, even though Driver asserts on all of them internally inside
`prepare` (driver.go:321-385). A caller who wants to know ahead of time whether a driver's
provider supports e.g. permissions has to type-assert on `driver` — but `Driver` doesn't
embed or expose the underlying `Provider`, so in practice nothing in the public API lets a
caller pre-check `Permitter`/`TurnLimiter`/etc. the way it can pre-check `BlockReporter` or
`ConcurrencyLimiter`. Those five are only discoverable by trying a Request and reading the
`Err*Unsupported` error back.

The pattern that distinguishes the two groups (not stated as a rule anywhere, inferred from
which methods exist): the three with accessors answer a question a caller needs BEFORE
assembling a Request or a routing decision (which reasons can this provider even detect,
how many runs can share this credential, what does this alias resolve to) — i.e. they're
read-only/informational and meaningful with no other request state. The other five only ever
gate a specific Request field's presence and are consumed exclusively inside `prepare`;
nothing else in the library or README treats them as something a caller queries standalone.

Relevant if adding `Disallower`: precedent is split, so whether it gets a `Driver.
CanDisallowTools()`-style accessor or stays gate-only (like `Permitter`) is a design choice,
not something an existing rule settles either way.
