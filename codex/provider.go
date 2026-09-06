// Package codex is the OpenAI Codex CLI dialect.
//
// It exists to keep the interfaces honest. An interface designed against one
// implementation is wrong in ways nobody can see from inside that
// implementation, and `codex exec` is the closest analogue to `claude -p` that
// is genuinely a different program: different flag spelling, a different
// credential variable, a different set of variables that can redirect it, and
// no vendored binary at all.
//
// `codex exec --json` emits JSONL from its first line to its last, and has no
// envelope mode to fall back to: -o/--output-last-message writes bare text to a
// file rather than a document to stdout. A Result is therefore a fold over a
// whole run, which is why this package decodes a stream rather than parsing a
// document.
package codex

import (
	"fmt"
	"slices"
	"strings"

	agentic "github.com/pedromvgomes/agentic-driver"
)

// ID is the provider's stable identifier.
const ID = "codex"

// BinaryName is the executable, found on PATH. Unlike claudecode this package
// vendors nothing: there is no signed manifest to pin against, so claiming a
// verified build would be a claim it cannot keep.
const BinaryName = "codex"

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

// StreamCommand renders a Request as `codex exec`.
//
// The subcommand is the shape that differs most from claudecode: the prompt is
// a positional argument after a subcommand rather than the value of a flag.
// That is the reason Invocation carries an argv the provider assembles in full,
// rather than the library assembling one from named parts.
//
// --json is the only output mode. It is a stream, so there is no second
// invocation for a batched run to drift away from this one.
func (p *Provider) StreamCommand(req agentic.Request) (agentic.Invocation, error) {
	if req.Prompt == "" {
		return agentic.Invocation{}, fmt.Errorf("%w: codex exec needs a prompt", agentic.ErrInvalidRequest)
	}
	if req.MaxTurns > 0 {
		// Refused rather than approximated. Codex has no configuration field
		// for a turn bound, and the nearest-looking spelling is accepted and
		// then ignored, so emitting one would leave the loop running
		// unbounded while the caller believed it had capped it.
		return agentic.Invocation{}, fmt.Errorf(
			"%w: codex has no turn limit; bound the run with Request.Timeout instead", agentic.ErrInvalidRequest)
	}

	args := []string{"exec", "--json"}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	permArgs, err := p.PermissionArgs(req.PermissionMode, req.AllowedTools)
	if err != nil {
		return agentic.Invocation{}, err
	}
	args = append(args, permArgs...)
	// The prompt is positional and last, so nothing it contains can be read as
	// a flag.
	args = append(args, req.Prompt)

	return agentic.Invocation{Args: args, Env: map[string]string{"NO_COLOR": "1", "TERM": "dumb"}}, nil
}

// sandboxModes is what -s accepts. Ordered as codex documents them, widening
// from left to right.
//
// Like a permission mode elsewhere, this is a vendor enum the CLI validates
// itself: an unrecognised value exits non-zero with nothing on stdout, which
// reaches a caller as ErrProviderUnavailable — a typo wearing the costume of an
// outage, and one that invites a retry loop. Refusing here names the real
// problem.
var sandboxModes = []string{"read-only", "workspace-write", "danger-full-access"}

// PermissionArgs applies mode, and refuses allowedTools.
//
// Codex constrains a run by sandbox, not by tool. Its `tools` configuration
// table has exactly one field — web_search — and there is no allowlist of any
// kind, so an allowedTools this accepted could only be discarded. A caller that
// asked for a restriction has to learn it was not applied: a dropped grant
// leaves the run with MORE authority than was asked for, and it fails silently,
// because a run that was never narrowed still answers perfectly well.
//
// approval_policy is deliberately left alone. `codex exec` has nobody to prompt
// and never blocks for approval — a sandbox denial comes back as the agent
// reporting it could not act, on an otherwise successful turn — so setting a
// policy here would be a knob with no meaning in this mode.
func (p *Provider) PermissionArgs(mode string, allowedTools []string) ([]string, error) {
	if len(allowedTools) > 0 {
		return nil, fmt.Errorf("%w: codex has no per-tool allowlist, so %s cannot be granted; restrict the run with a sandbox mode (%s)",
			agentic.ErrInvalidRequest, strings.Join(allowedTools, ", "), strings.Join(sandboxModes, ", "))
	}
	if mode == "" {
		return nil, nil
	}
	if !slices.Contains(sandboxModes, mode) {
		return nil, fmt.Errorf("%w: sandbox mode %q is not one of %s",
			agentic.ErrInvalidRequest, mode, strings.Join(sandboxModes, ", "))
	}
	return []string{"-s", mode}, nil
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
// neither Installer nor Resumer nor AgentDefiner nor TurnLimiter: absent
// capabilities are absent from the type, and the driver answers for them
// without spawning anything.
//
// TurnLimiter is absent because codex has no turn bound to express, and
// AgentDefiner because it has no vocabulary for declaring a roster on the
// command line. Installer is absent because this package vendors nothing: there
// is no signed manifest to pin against, so claiming a verified build would be a
// claim it cannot keep.
var (
	_ agentic.Provider  = (*Provider)(nil)
	_ agentic.Isolator  = (*Provider)(nil)
	_ agentic.Permitter = (*Provider)(nil)
)
