---
about: a new optional Request field is not "supported" until it has both a capability interface and a matching gate in Driver.prepare, or its zero-drop is silent
saw:
  - provider.go
  - driver.go
  - errors.go
---

Every optional `Request` field that already exists (`SessionID`, `Agents`, `AllowedTools`/
`PermissionMode`, `MaxTurns`, `Schema`) follows the same two-part pattern, not just "add an
interface":

1. A capability interface in `provider.go` that only a provider able to express the field
   implements (e.g. `Permitter` at provider.go:173-177, `TurnLimiter` at provider.go:206-210).
2. A check in `Driver.prepare` (driver.go:321-385) that runs BEFORE `provider.StreamCommand`
   is ever called: if the field is set (non-zero/non-empty) and the provider does not
   implement the matching interface, `prepare` returns a dedicated sentinel wrapping
   `ErrInvalidRequest`-shaped `Err*Unsupported` error (defined in errors.go:8-61, e.g.
   `ErrPermissionsUnsupported` at errors.go:34-38) and no process starts.

The reason `prepare` exists as a second, driver-owned check rather than leaving refusal to
each provider's `StreamCommand` is stated directly in the comments beside each block
(driver.go:322-323, :340-341, :348-350, :357-366): dropping the field is silent and the
run still answers competently, just with the wrong scope/history/shape/authority — usually
in the DANGEROUS direction (more authority, no cap, wrong shape) rather than a visible
failure. `ErrPermissionsUnsupported`'s doc comment states the general rule explicitly:
"Dropping either widens what the run may do, so the silent failure runs with more authority
than was asked for, never less" (errors.go:34-38).

So a new field like `DisallowedTools` needs: (a) a `Disallower` interface in provider.go,
(b) a new `Err*Unsupported` sentinel in errors.go, (c) a block in `Driver.prepare` that
refuses when `len(req.DisallowedTools) > 0` and the provider isn't a `Disallower`, mirroring
the `AllowedTools`/`Permitter` block at driver.go:340-346. Doing only (a) — implementing the
interface and calling it from `StreamCommand` — would let a provider that lacks the
capability silently ignore the field instead of refusing, since nothing upstream of
`StreamCommand` would stop the call.
