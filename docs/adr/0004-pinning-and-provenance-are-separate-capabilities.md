# Pinning and provenance are separate capabilities

`Installer` bundled two guarantees that are not the same thing:

1. **I control which version runs.** An exact version, executed by absolute path, whose
   bytes were checked against a digest.
2. **I verified who built it.** A signature from a named publisher, checked against a
   trust anchor this repository embeds.

`claudecode` delivers both, so nothing in it exposed the seam. `codex` can deliver the
first everywhere it vendors and the second nowhere, which is what forced the question.

The two are now separate interfaces. `Pinner` is (1). `Installer` embeds `Pinner` and adds
(2), carrying `SigningIdentity() string` — which is also what keeps them distinguishable:
an `Installer` that added no method would be satisfied by every `Pinner`, so the type
assertion this library discovers capabilities with could not tell a verified build from a
merely pinned one, and that is the entire distinction.

`SigningIdentity` returns a value rather than reporting a capability. A method whose only
job is to answer "yes, verified" states nothing an auditor can check, and the question a
caller actually has — verified by **whom** — would still have nowhere to be answered.
`claudecode.Provider` returns its OpenPGP fingerprint and key UID. Its guarantee is
unchanged: the same embedded key, the same fingerprint assertion, the same refusal to act
on a manifest signed by anything else.

## Why pinning codex matters, and it is not mainly security

`codex/parse.go` decodes an NDJSON event stream, written against golden captures in
`codex/testdata`. An unpinned, self-updating codex can move that schema silently. The
break then surfaces on a user's machine, mid-run, as a parse failure. Pinned, it surfaces
at the bump, as a failing decoder test against a re-captured fixture. Codex ships at
0.153.4 on a fast cadence, so this is a live risk rather than a theoretical one.

What the pin buys is not immobility. It is knowing when the CLI changed.

## What codex actually publishes

At tag `rust-v0.153.4`:

- The GitHub release carries `.sigstore` bundles for **`unknown-linux-musl` targets only**.
  There is no bundle for either `apple-darwin` target.
- `codex-package_SHA256SUMS` covers the twelve `*-package-*.tar.gz` assets, darwin
  included — but that file is itself unsigned and served from the same host as the
  artifacts it describes. Integrity, not authenticity.
- npm publishes the per-platform binaries as prerelease-suffixed versions of
  `@openai/codex` (`0.153.4-darwin-arm64` and so on), pulled in by the wrapper as optional
  dependencies. **Every one of them carries a SLSA provenance attestation**, darwin
  included: a Sigstore bundle whose Fulcio certificate names
  `https://github.com/openai/codex/.github/workflows/rust-release.yml@refs/tags/rust-v0.153.4`,
  issued to an OIDC identity from `token.actions.githubusercontent.com`, over a subject
  digest that is the tarball's SHA-512.

So authenticity for macOS codex does exist. It is on npm, not on the GitHub release. The
belief that codex cannot be verified on darwin was true of one distribution channel and
false of the other.

## Why the library still does not check it

Verifying that bundle in Go means `sigstore-go`, which resolves to roughly seventy
modules. This library currently has five. Hand-rolling the verification with the standard
library is worse than not doing it: the Fulcio chain, the Rekor inclusion proof, and the
ten-minute ephemeral certificate validity window — which makes the signed timestamp
load-bearing — are exactly the parts a from-scratch verifier gets wrong in the direction of
accepting everything. That produces a verification which passes its own tests and rejects
nothing, and a claim the code cannot keep is the thing this decision exists to avoid.

Provenance is therefore checked **once, by a human, when the pin moves**, with tooling that
already exists on the machine doing the bump, and the result is committed as a digest. The
digest table in `codex/release.go` is the trust anchor, for the same reason
`claudecode/anthropic-release-key.asc` is: changing it is a deliberate act visible in a
diff. Checking a download against a digest the same server just served would prove the
bytes arrived intact, not that they are the bytes anyone chose.

npm is the download source rather than the GitHub release, because it is the only channel
carrying the same artifact shape for every platform, and the only one whose darwin
artifacts have an attestation a human can check at bump time.

## Alternatives rejected

**One interface, weakened contract, `InstallResult` reports what was verified.** This
moves a compile-time-checkable claim into a runtime field every caller has to remember to
read — the boolean-capability-field pattern the package documentation opens by rejecting.

**Leave codex unpinned and document the exposure.** Cheapest, and it leaves the schema
risk — the reason this exists — entirely unmanaged.

**Take the `sigstore-go` dependency so codex can implement `Installer` honestly.** A
fourteen-fold increase in the dependency tree of every downstream consumer, to strengthen
the secondary benefit. If a consumer needs install-time provenance, the right shape is a
separate module they opt into, not a cost this one imposes on everybody.

**Implement `Installer` on linux only, via the musl sigstore bundles.** A capability that
is present on one platform and absent on another is worse than an absent one: the caller
asserting on `Installer` to decide whether to trust a build gets a different answer
depending on where it happens to be running, and the platform where it silently gets less
is the one nobody develops on.

## Consequences

- `codex.New(providersRoot)` vendors and pins; `codex.NewOnPath()` keeps the ambient
  behaviour and implements neither interface. This mirrors `claudecode` exactly, so the
  two dialects stay readable as parallel.
- Windows vendors nothing. `Executable` requires an execute bit no Windows file carries, so
  a vendored install there would publish correctly and then be reported missing forever.
  `NewOnPath` is the answer on that platform.
- A version absent from the digest table cannot be installed, and is refused at
  construction rather than at the download.
- Two installers now exist with near-identical staging, single-flight and retention logic.
  That duplication is real and is the price of not refactoring `claudecode` in the same
  change that redefines the interface it implements.

## Moving the pin

The procedure is the guarantee. Skipping any step turns the pin back into a version number.

1. Verify the new version's provenance for every vendored platform:
   `gh attestation verify --repo openai/codex --predicate-type https://slsa.dev/provenance/v1`
   against each downloaded tarball, or `npm audit signatures` in a tree that installed it.
   The certificate identity must name `openai/codex`'s `rust-release.yml` at the matching
   `refs/tags/rust-v<version>` tag.
2. Record the digests: `npm view @openai/codex@<version>-<platform> dist.integrity` for
   each of `darwin-arm64`, `darwin-x64`, `linux-arm64`, `linux-x64`, added to
   `pinnedDigests` as a new entry rather than an edit to the old one, so a rollback target
   survives.
3. Re-capture `codex/testdata` from the newly pinned binary and run
   `go test ./codex/` — the decoder tests read those fixtures, so a moved schema is a red
   test here.
4. Run `go test -tags integration ./codex/`, which refuses to run against any binary but
   the pin.
