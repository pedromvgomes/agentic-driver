package agentic

import (
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
)

// minimalPath is the PATH a child gets under isolated credentials.
//
// Not the parent's: that is whatever a shell, a unit file or a container image
// happened to export, and it is a lookup surface the caller did not choose.
// The agent binary itself is resolved before this is built, so this exists only
// for the handful of standard tools the CLI may shell out to.
const minimalPath = "/usr/local/bin:/usr/bin:/bin"

// Credentials is how a child process is authenticated. Construct one with
// Ambient or Isolated.
type Credentials struct {
	isolated bool
	token    string
}

// Ambient inherits the parent environment: the child uses whatever the CLI is
// already authenticated with. This is what a developer's machine wants, and it
// is the default.
func Ambient() Credentials { return Credentials{} }

// Isolated builds the child environment from a fixed list and hands the
// provider a specific token.
//
// The guarantee is that the environment is CONSTRUCTED, not filtered from
// os.Environ(): a variable nobody thought of cannot arrive by accident, because
// nothing arrives that was not written down. The provider's DenyEnv is applied
// on top as a backstop, and catches only what a later edit puts back in.
func Isolated(token string) Credentials { return Credentials{isolated: true, token: token} }

// buildEnv assembles the child's environment.
//
// The ordering is the whole policy. Under isolation the deny list runs before
// the credential is applied, so a provider is free to name its own auth
// variable in DenyEnv — which it usually must, because a variable that carries
// the credential is by definition one that can REDIRECT the credential when it
// arrives from anywhere but here. Scrubbing after would strip the token the
// driver deliberately injected and hand the CLI no credential at all, which
// fails as an authentication error miles from its cause.
func (d *Driver) buildEnv(inv Invocation) ([]string, error) {
	if !d.creds.isolated {
		// Everything the caller has, plus the provider's dialect settings.
		// Applying DenyEnv here would be the opposite of what ambient means: a
		// caller pointing the CLI at a proxy with ANTHROPIC_BASE_URL is using
		// the mode as intended.
		return append(os.Environ(), sortedKV(inv.Env)...), nil
	}

	iso, ok := d.provider.(Isolator)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrIsolationUnsupported, d.descriptor.ID)
	}

	env := map[string]string{"PATH": d.path}
	// An empty HOME is exported as an empty value rather than left out, and a
	// Node program resolves that differently from an absent one — writing its
	// cache relative to the filesystem root. Omitting it lets the CLI apply its
	// own fallback.
	if d.home != "" {
		// A program that writes a cache beside its config should write it
		// inside the directory the caller nominated, not the operator's own.
		env["HOME"] = d.home
	}
	for k, v := range inv.Env {
		env[k] = v
	}

	// Everything that could have come from somewhere else is scrubbed first.
	out := scrub(sortedKV(env), iso.DenyEnv())

	// The credential goes on last, so it survives the scrub and cannot be
	// shadowed by a dialect variable of the same name.
	return append(out, sortedKV(iso.AuthEnv(d.creds.token))...), nil
}

// scrub drops any denied variable from an assembled environment.
//
// Redundant against building the list from scratch, and deliberately so: that
// is a property of today's code, this is a property of the function. It
// protects what passes THROUGH it and nothing else — an assignment to cmd.Env
// after the call goes straight round it.
func scrub(env []string, denied []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, ok := strings.Cut(kv, "=")
		if ok && slices.Contains(denied, name) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// sortedKV renders a map as KEY=VALUE, ordered, so a constructed environment is
// identical from one run to the next and a test can assert on it.
func sortedKV(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}
