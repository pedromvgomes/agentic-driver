---
about: Driver exposes a public accessor only for capabilities a caller needs to query before building a Request (informational); capabilities that just change what StreamCommand accepts are asserted internally in prepare() with no per-capability Driver method — except for a single general-purpose escape hatch, Provider(), added to reach all of them at once
saw:
  - driver.go
  - provider.go
---

Driver has three public capability-accessor methods that wrap a type assertion on a specific
interface: `ResolveModel`/`Model` for `ModelResolver` (driver.go:235-248), `MaxConcurrentRuns`
for `ConcurrencyLimiter` (driver.go:250-269), and `DetectableBlocks` for `BlockReporter`
(driver.go:271-289). README.md documents exactly this trio as "the pattern" (`driver.
DetectableBlocks()`, `driver.MaxConcurrentRuns()`).

`Resumer`, `AgentDefiner`, `Permitter`, `Disallower`, `TurnLimiter` and `SchemaConstrainer` get
NO per-capability accessor, even though Driver asserts on all of them internally inside
`prepare` (driver.go:332-...). The pattern that distinguishes the two groups (not stated as a
rule anywhere, inferred from which methods exist): the three with accessors answer a question a
caller needs BEFORE assembling a Request or a routing decision (which reasons can this provider
even detect, how many runs can share this credential, what does this alias resolve to) — i.e.
they're read-only/informational and meaningful with no other request state. The other capabilities
only ever gate a specific Request field's presence and are consumed exclusively inside `prepare`.

Resolved: when `Disallower` was added, it did NOT get a per-capability accessor (no
`Driver.CanDisallowTools()`) — the informational/gating split above held. Instead `Driver`
gained `Provider()` (driver.go:216-225), a single general-purpose accessor that returns the raw
`agentic.Provider` for a caller to type-assert ANY capability without a dedicated method —
`Permitter`, `AgentDefiner`, `Resumer`, `TurnLimiter`, `Disallower`, or one not yet invented. Its
own doc comment is explicit that it must not grow into a list of per-capability wrappers: "a
capability worth calling often earns its own accessor, the way Model and MaxConcurrentRuns
already have. Adding one here per capability would just move the type assertion from the caller
into this file without removing it." So the real rule going forward is: a gate-only capability
gets no accessor of its own, full stop — `Provider()` is the one escape hatch for all of them,
not a stepping stone to more per-capability ones.
