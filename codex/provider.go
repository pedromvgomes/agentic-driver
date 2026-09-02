// Package codex is the OpenAI Codex CLI dialect.
//
// It exists to keep the interfaces honest. An interface designed against one
// implementation is wrong in ways nobody can see from inside that
// implementation, and `codex exec` is the closest analogue to `claude -p` that
// is genuinely a different program: different flag spelling, a different
// credential variable, a different set of variables that can redirect it, and
// no vendored binary at all.
//
// Command, DenyEnv and AuthEnv are real. Parse is not: the envelope schema has
// not been captured from the CLI the way claudecode's testdata was, and writing
// one from memory would produce a parser that passes its own tests and nothing
// else. It returns ErrNotImplemented until a golden envelope exists to write it
// against.
package codex

import (
	"errors"
	"fmt"
	"strconv"

	agentic "github.com/pedromvgomes/agentic-driver"
)

// ID is the provider's stable identifier.
const ID = "codex"

// BinaryName is the executable, found on PATH. Unlike claudecode this package
// vendors nothing: there is no signed manifest to pin against, so claiming a
// verified build would be a claim it cannot keep.
const BinaryName = "codex"

// ErrNotImplemented means the capability is declared but not yet written.
var ErrNotImplemented = errors.New("codex: not implemented")

// Provider is the Codex dialect.
type Provider struct{}

// New builds the provider.
func New() *Provider { return &Provider{} }

func (p *Provider) Descriptor() agentic.Descriptor {
	return agentic.Descriptor{
		ID:          ID,
		DisplayName: "Codex",
		Binary:      BinaryName,
	}
}

// Command renders a Request as `codex exec`.
//
// The subcommand is the shape that differs most from claudecode: the prompt is
// a positional argument after a subcommand rather than the value of a flag.
// That is the reason Invocation carries an argv the provider assembles in full,
// rather than the library assembling one from named parts.
func (p *Provider) Command(req agentic.Request) (agentic.Invocation, error) {
	if req.Prompt == "" {
		return agentic.Invocation{}, fmt.Errorf("%w: codex exec needs a prompt", agentic.ErrInvalidRequest)
	}

	args := []string{"exec", "--json"}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if req.MaxTurns > 0 {
		// Codex spells the bound as a config override rather than a flag, which
		// is why Request carries the intent and each provider spells it.
		args = append(args, "-c", "max_turns="+strconv.Itoa(req.MaxTurns))
	}
	// The prompt is positional and last, so nothing it contains can be read as
	// a flag.
	args = append(args, req.Prompt)

	return agentic.Invocation{Args: args, Env: map[string]string{"NO_COLOR": "1", "TERM": "dumb"}}, nil
}

// Parse is unwritten. See the package comment.
func (p *Provider) Parse(stdout, stderr []byte, code int) (agentic.Result, error) {
	return agentic.Result{}, fmt.Errorf("%w: Parse has no golden envelope to be written against", ErrNotImplemented)
}

// AuthEnv carries an OpenAI key.
//
// A different variable from claudecode's, which is the point of the vocabulary
// being the provider's: nothing generic could have named it.
func (p *Provider) AuthEnv(token string) map[string]string {
	return map[string]string{"OPENAI_API_KEY": token}
}

// DenyEnv is every variable that can redirect Codex away from the key it was
// handed.
//
// Entirely different from claudecode's list, and shorter — which is the fact
// that makes the list a provider's property rather than the library's. A shared
// list would have to be the union, and every entry in it would be wrong for
// somebody.
func (p *Provider) DenyEnv() []string {
	return []string{
		"OPENAI_API_KEY",
		"OPENAI_BASE_URL",
		"OPENAI_API_BASE",
		"OPENAI_ORGANIZATION",
		"OPENAI_PROJECT",
		"AZURE_OPENAI_API_KEY",
		"AZURE_OPENAI_ENDPOINT",
		"CODEX_API_KEY",
	}
}

// Compile-time proof of which capabilities this provider claims. It implements
// neither Installer nor Streamer nor Resumer: absent capabilities are absent
// from the type, and the driver answers for them without spawning anything.
var (
	_ agentic.Provider = (*Provider)(nil)
	_ agentic.Isolator = (*Provider)(nil)
)
