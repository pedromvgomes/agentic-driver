---
about: The Claude Code release manifest signature is verified by a hand-written stdlib RSA/OpenPGP verifier, not an OpenPGP library, because any OpenPGP library drags golang.org/x/crypto into every consumer's module graph; trust rests on the embedded key's fingerprint, and the key's own expiry and revocation are deliberately not consulted
saw:
  - claudecode/openpgp.go
  - claudecode/release.go
  - claudecode/anthropic-release-key.asc
  - claudecode/testdata/README.md
---

`releaseClient.Manifest` (release.go) checks the detached signature over `manifest.json` with
`publicKey.verifyDetached` (openpgp.go), built on `crypto/rsa`. The embedded key is a single v4
RSA-4096 key, so no general OpenPGP implementation is needed. `go.mod` requires only
`golang.org/x/mod`: reintroducing `ProtonMail/go-crypto` (or `x/crypto/openpgp`) brings
`golang.org/x/crypto` back, and consumers' module-level `govulncheck` scans then report
GO-2026-5932, which has no fixed version, even though no vulnerable code path is reachable.

What the trust anchor is: `assertFingerprint` compares the parsed embedded key's v4 fingerprint
to `SigningKeyFingerprint` at construction, and `parsePublicKey` accepts exactly one primary key
per block and reads only the first armor block, so a second key cannot be added as a signer.
User IDs, self-certifications, expiry and revocation of the key are not parsed; the fingerprint
pin fixes the exact key material instead, and the embedded key has no expiry. A key rotation or
revocation therefore only takes effect when the key file and constant are replaced.

The verifier refuses SHA-1 and weaker hashes, non-binary signature types, a missing creation
time, a signature dated before the key, an expired signature, an issuer fingerprint that is not
the key, unknown critical subpackets, and signature files holding more than one packet. The
armor CRC line is ignored. Tests start from a real Anthropic-signed manifest and signature
captured in `claudecode/testdata` (provenance in that directory's README.md); the synthetic
signer lives in `claudecode/openpgp_test.go`.
