---
about: a new optional Request field is not "supported" until it has both a capability interface and a matching gate in Driver.prepare, or its zero-drop is silent — and if a provider already self-guards a sibling field directly inside its own StreamCommand (bypassing Driver), the new field needs that same direct guard too
saw:
  - provider.go
  - driver.go
  - errors.go
  - codex/provider.go
---

Every optional `Request` field follows the same two-part pattern, not just "add an interface":

1. A capability interface in `provider.go` that only a provider able to express the field
   implements (e.g. `Permitter` at provider.go:173-177, `Disallower` at provider.go:179-195,
   `TurnLimiter` at provider.go:224-228).
2. A check in `Driver.prepare` (driver.go:332-...) that runs BEFORE `provider.StreamCommand` is
   ever called: if the field is set (non-zero/non-empty) and the provider does not implement the
   matching interface, `prepare` returns a dedicated sentinel wrapping an `Err*Unsupported` error
   (defined in errors.go, e.g. `ErrPermissionsUnsupported` at errors.go:34-38,
   `ErrDisallowUnsupported` at errors.go:40-44) and no process starts.

Confirmed by `DisallowedTools`: it was built exactly this way — the `Disallower` interface
(provider.go:179-195), `ErrDisallowUnsupported` (errors.go:40-44), and a `Driver.prepare` block
(driver.go:359-365) mirroring the `AllowedTools`/`Permitter` block right above it
(driver.go:351-357).

A THIRD thing showed up that this pattern alone doesn't cover, caught only by a panel code
review, not by the plan: `codex/provider.go`'s `StreamCommand` already self-guards `MaxTurns`
directly (refusing it with `ErrInvalidRequest` before building argv), even though
`Driver.prepare` also gates `MaxTurns` — because `StreamCommand` is callable directly on the
provider, bypassing `Driver` entirely, and a caller that does that never goes through `prepare`
at all. `DisallowedTools` initially only got the `Driver.prepare` gate and NOT a matching direct
check in codex's `StreamCommand`, which the review flagged (AMBER, security:authorization) as
letting a direct caller silently run with more authority than asked. Fixed by adding the same
`len(req.DisallowedTools) > 0` refusal directly in codex's `StreamCommand`, next to the
`MaxTurns` one.

So the real rule: whenever a NEW field is gated in `Driver.prepare`, check whether any provider
already self-guards a SIBLING field (one already refused by `Driver.prepare`) directly inside
its own `StreamCommand` — if so, the new field needs the identical direct guard there too, not
just the `Driver`-level one. `Driver.prepare` alone is not the complete answer for a provider
whose `StreamCommand` is part of its own public contract.
