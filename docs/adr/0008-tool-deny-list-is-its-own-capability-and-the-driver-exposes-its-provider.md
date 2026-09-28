# A tool deny-list is its own capability, refused outright where absent, and the Driver exposes its Provider

`Request.DisallowedTools` removes tools from a child run regardless of what an allowlist or
permission mode would otherwise grant — the gap an allowlist alone can't close, since a tool
needing no permission is invisible to it. It moves behind a new `Disallower` interface rather
than a third parameter on `Permitter.PermissionArgs`, because a provider can grant an allowlist
without being able to remove anything from it (or vice versa), and changing `PermissionArgs`'s
signature would force every existing implementer to change to accommodate a capability some of
them don't have. Claude Code implements it, emitting `--disallowedTools`. Codex does not: its
nearest analogue, `features.multi_agent`, is a single switch over five tools at once, not a
per-tool control, so a `Disallower` on codex could only honor a couple of hardcoded names and
silently refuse the rest — the same silent-scope failure `ErrTurnLimitUnsupported` (ADR 0002)
already exists to prevent, just partial instead of total. A non-empty `DisallowedTools` on codex
is refused before any process starts, exactly like `AllowedTools` today.

Deciding this exposed a second, unrelated gap: nothing let a caller ask *whether* a provider
implements an input-shaping capability like `Disallower` without either holding onto its own
concrete `Provider` value (against the grain of the README's own example, which discards it
after `agentic.New(provider, ...)`) or submitting a `Request` and reading back the error.
`Driver` gains an exported `Provider()` accessor returning the wrapped `agentic.Provider`, so a
caller can type-assert any capability — this one or any future one — without keeping its own
reference alive.

## Considered options

**Codex maps a small set of names (`Agent`, `Task`) to `-c features.multi_agent=false` and
refuses the rest.** Closes the exact gap the motivating bug describes on codex too. Rejected:
it is a capability that behaves differently depending on the value passed to it, which nothing
else in this codebase does, and it buys very little — the motivating caller only needs
`DisallowedTools` on Claude Code, since that's the dialect actually being driven as the locked
down curator.

**A `Disallower`-specific accessor (`Driver.Disallows()`) instead of a general `Provider()`.**
Mirrors `MaxConcurrentRuns()`/`DetectableBlocks()`, which exist for the three *informational*
capabilities answerable without a `Request` in hand. Rejected: `Permitter`, `AgentDefiner`,
`Resumer` and `TurnLimiter` have the identical discovery gap today and a capability-specific
accessor only for `Disallower` would fix it for the one field this task happens to add, leaving
the other four exactly as undiscoverable as before.

## Consequences

`Driver.Provider()` makes the concrete `Provider` reachable from any `Driver`, which the
existing `Model()`, `MaxConcurrentRuns()` and `DetectableBlocks()` accessors already did
implicitly for their three capabilities by wrapping the type assertion themselves; this makes
the underlying value reachable directly instead, for every capability at once, informational or
not. It does not replace those three convenience accessors, which stay for their nil-safe
defaults.
