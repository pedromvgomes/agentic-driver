# A concurrency limit is a capability, and it belongs to the provider

`codex exec` authenticates from `auth.json` under `CODEX_HOME`. The CLI rewrites that file
in place as it refreshes, and the refresh tokens in it are effectively single-use: two runs
reading it at once race to refresh, the loser presents a token the winner has already spent,
and the profile is left holding whichever half-rotated state landed last. Claude Code
authenticates from a static bearer token injected as an environment variable, which nothing
rewrites and any number of runs can read at once.

So "run these four agents in parallel" is correct on one provider and corrupts a credential
on the other, and nothing in this library's surface said which was which. A caller wanting
to fan out had one route left — `if id == "codex"` — which is the switch on a provider ID
that every capability here exists to avoid.

`ConcurrencyLimiter` is that capability, gated by type assertion exactly as `Permitter`,
`SchemaConstrainer` and `ModelResolver` already are. codex implements it and answers 1;
claudecode does not implement it, and absent means unconstrained.

## Considered options

**Put the answer on `Driver` instead, so it can account for the credential mode.** More
honest-looking, and wrong here. `Isolated` hands codex a token in `OPENAI_API_KEY`, but a
`CODEX_HOME` profile holding a session OUTRANKS that token — codex uses the session and
never attempts the key. A driver reporting "isolated, therefore unbounded" would be
answering for a profile it cannot see, and it would be most confidently wrong in exactly the
configuration that is most dangerous. `Driver.MaxConcurrentRuns` therefore forwards the
provider's answer and adds nothing to it, the way `Driver.ResolveModel` does.

**Give each concurrent run its own `WithConfigDir` holding a copy of the session.** The
obvious fix, and it does not work: copies of one session are not independent sessions. The
same single-use refresh token is in every copy, so the first refresh invalidates the rest
and can invalidate the profile they were copied from — trading one broken login for several.
Only genuinely separate logins are genuinely concurrent, and nothing in this library can
tell a separate login from a copy.

**Express it as a boolean — `MustSerialise() bool`.** Honest about the only two defensible
states today, but it states exactly what implementing the interface already states, so a
caller sizing a pool would still have no number to size it from. The precedent is
`Installer.SigningIdentity`: a method carries a value, because one whose only job is to
answer "yes" leaves the question a caller actually has with nowhere to be answered.

## Consequences

A caller that fans out asserts on `ConcurrencyLimiter`, or asks its `Driver`, and sizes its
pool from the answer. Zero means nothing is claimed — the honest reading of an absent
capability, and a different statement from a limit of 1. The limit describes the credential
the CLI resolves rather than any mode the caller selected, so a caller that has genuinely
provisioned separate logins is free to run a driver per login; what it must not do is share
one.
