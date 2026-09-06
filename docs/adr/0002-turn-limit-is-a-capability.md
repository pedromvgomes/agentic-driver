# A turn limit is a capability, not a Request field every provider honours

Claude Code bounds the agent loop with `--max-turns`. Codex has no equivalent: `max_turns`,
`turn_limit`, `max_iterations`, `max_steps`, `tools.max_turns` and `experimental_max_turns`
are all rejected as unknown configuration fields, and codex counts a turn as the whole
agent loop rather than one iteration within it, so there is no concept at that granularity
to bind to. `MaxTurns` therefore moves behind a `TurnLimiter` interface, gated by type
assertion exactly as `Resumer`, `AgentDefiner` and `Permitter` already are. Claude Code
implements it; codex does not, and a request carrying `MaxTurns` is refused before a
process starts.

## Considered options

**Emit `-c max_turns=N` on codex anyway.** This is what the code did, and it is the worst
of the three: without `--strict-config` codex accepts the override, ignores it, and runs
unbounded while the caller believes a cap applied.

**Drop `MaxTurns` from `Request` entirely.** Symmetrical, and it would have made the two
providers freely interchangeable. Rejected because it deletes a real, working, fixture-backed
Claude Code feature purely to match the weaker CLI.

## Consequences

A caller that wants one bound working identically across providers uses `Request.Timeout`,
which the driver enforces itself and which needs nothing from the CLI. A caller that wants
to offer a turn limit only where it means something asserts on `TurnLimiter` and asks
before offering, rather than discovering the answer as a runtime error.
