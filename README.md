# agentic-driver

A Go library for driving headless coding-agent CLIs.

The library owns the **process**; a provider owns the **dialect**.

- **Process** — argv assembly, context and timeout handling, exit-code
  interpretation, stderr redaction, environment construction. Written once, in
  this package, and no provider touches it.
- **Dialect** — flag spelling, result-envelope schema, the environment
  variables that can hijack a given CLI, resume semantics. Declared per
  provider.

Providers live in their own subpackages (`claudecode`, `codex`, ...) and
declare their capabilities by which interfaces they implement, discovered by
type assertion rather than by a boolean field or a switch on provider ID.

## Status

Early. The API is not stable and the first provider is still being extracted.

## Credential modes

Two, chosen by the caller:

- **Ambient** — inherit the environment the process already has. This is what a
  developer's machine wants: use whatever the CLI is already authenticated
  with.
- **Isolated** — build the child environment from a fixed allowlist and hand
  the provider a specific token. The environment is *constructed*, never
  filtered from `os.Environ()`, so a variable nobody thought of cannot arrive
  by accident. The provider supplies the vocabulary — which variables carry
  auth, and which ones can redirect the CLI somewhere else.

## License

MIT
