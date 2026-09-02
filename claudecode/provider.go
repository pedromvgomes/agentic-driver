package claudecode

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	agentic "github.com/pedromvgomes/agentic-driver"
)

// ID is the provider's stable identifier.
const ID = "claude-code"

// PinnedVersion is the build this package is written against.
//
// The envelope schema and the flag spelling below are properties of a specific
// release, so a silent self-update is a silent break. It lives here, once.
const PinnedVersion = "2.1.258"

// dialect is everything about talking to Claude Code that does not depend on
// where the binary came from: the flags, the envelope, and the environment.
//
// Both provider types embed it, so the two share one implementation of the
// dialect and differ only in the capability that actually distinguishes them.
type dialect struct {
	configDir string
}

// Provider runs a vendored Claude Code, installed and verified against
// Anthropic's signed manifest at a pinned version.
//
// It implements Installer, which is also what makes the binary an absolute
// path: there is no PATH lookup for something else to win and no launcher
// symlink to repoint, so the build that runs is the build that was verified.
type Provider struct {
	dialect
	installer *Installer
	version   string
}

// PathProvider runs whichever Claude Code is on PATH.
//
// It deliberately does NOT implement Installer, and the absence is the point: a
// provider that runs someone else's binary cannot claim the guarantee that
// comes with installing one, and the driver reads that absence rather than
// taking anyone's word for it. Use it on a developer machine, where the answer
// to "which claude" is "the one I already have".
type PathProvider struct {
	dialect
}

// Option configures either provider.
type Option func(*config)

type config struct {
	version    string
	versionSet bool
	configDir  string
}

// WithVersion overrides the pinned version New installs and runs. It is
// meaningless to a PathProvider, which runs whatever is on PATH, and NewOnPath
// refuses it rather than ignoring it.
func WithVersion(v string) Option {
	return func(c *config) { c.version, c.versionSet = v, true }
}

// WithConfigDir sets CLAUDE_CONFIG_DIR, the directory a spawned claude reads and
// writes its own configuration in.
//
// Without it the CLI uses the caller's own ~/.claude, so a run mutates the
// settings of the human sitting at the machine. Nominate a directory whenever
// the run is not that human's own.
func WithConfigDir(dir string) Option {
	return func(c *config) { c.configDir = dir }
}

func settings(opts []Option) config {
	cfg := config{version: PinnedVersion}
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}

// New builds a provider that installs and runs its own copy of the CLI.
//
// providersRoot is where vendored versions live, one directory per version. It
// must be absolute, because the binary under it is executed by the path this
// composes.
//
// The pinned version does not have to be installed yet: Install is how it gets
// there, and refusing to construct the provider would put that method out of
// reach of the object offering it. Driver.Ready reports whether a run could
// start.
func New(providersRoot string, opts ...Option) (*Provider, error) {
	cfg := settings(opts)

	inst, err := NewInstaller(providersRoot)
	if err != nil {
		return nil, err
	}
	if err := validateVersion(cfg.version); err != nil {
		return nil, fmt.Errorf("claudecode: pinned version: %w", err)
	}

	return &Provider{
		dialect:   dialect{configDir: cfg.configDir},
		installer: inst,
		version:   cfg.version,
	}, nil
}

// NewOnPath builds a provider that runs whichever claude is on PATH.
//
// This is the developer-machine case, and it pairs with ambient credentials:
// use the CLI that is already installed, already authenticated, already the one
// being used by hand. What it gives up is the vendored guarantee — the binary
// is whatever PATH resolves to, verified by nobody — which is why it is a
// separate constructor rather than a flag on the other one.
func NewOnPath(opts ...Option) (*PathProvider, error) {
	cfg := settings(opts)

	// Refused rather than ignored: a caller pinning a version is asking for a
	// specific build, and answering that request with "whatever is on PATH"
	// would be the silent substitution the pin exists to prevent.
	if cfg.versionSet {
		return nil, errors.New("claudecode: WithVersion needs a vendored install; NewOnPath runs whatever is on PATH")
	}

	return &PathProvider{dialect: dialect{configDir: cfg.configDir}}, nil
}

// Version reports the version this provider runs.
func (p *Provider) Version() string { return p.version }

func (p *dialect) Descriptor() agentic.Descriptor {
	return agentic.Descriptor{
		ID:          ID,
		DisplayName: "Claude Code",
		Binary:      BinaryName,
	}
}

// BinaryPath is the absolute path of the pinned build.
func (p *Provider) BinaryPath() string { return p.installer.Path(p.version) }

// baseArgs are the flags every invocation carries, in the order they must
// appear.
//
// --setting-sources ” comes first so no caller can forget it and no later
// positional can shadow it. It is the measure that is easy to omit and
// impossible to notice: apiKeyHelper is a SETTINGS-FILE key, not an environment
// variable. It outranks an injected token, it is invisible to `env`, and a
// perfectly constructed environment does not touch it. Refusing to load
// settings files at all is the only thing that closes it.
func (p *dialect) baseArgs() []string {
	return []string{"--setting-sources", ""}
}

// Command renders a Request as `claude -p`.
func (p *dialect) Command(req agentic.Request) (agentic.Invocation, error) {
	if req.Prompt == "" {
		return agentic.Invocation{}, fmt.Errorf("%w: claude -p needs a prompt", agentic.ErrInvalidRequest)
	}

	args := append(p.baseArgs(), "-p", req.Prompt, "--output-format", "json")
	args = append(args, p.commonArgs(req)...)
	return agentic.Invocation{Args: args, Env: p.dialectEnv()}, nil
}

// StreamCommand is Command with the newline-delimited output format.
//
// --verbose is not optional here: without it the CLI collapses stream-json down
// to the single terminal envelope, which is the non-streaming case wearing a
// different flag.
func (p *dialect) StreamCommand(req agentic.Request) (agentic.Invocation, error) {
	if req.Prompt == "" {
		return agentic.Invocation{}, fmt.Errorf("%w: claude -p needs a prompt", agentic.ErrInvalidRequest)
	}

	args := append(p.baseArgs(), "-p", req.Prompt, "--output-format", "stream-json", "--verbose")
	args = append(args, p.commonArgs(req)...)
	return agentic.Invocation{Args: args, Env: p.dialectEnv()}, nil
}

// commonArgs renders the optional parts of a Request. A field left zero is a
// flag left off, so the CLI's own default applies.
func (p *dialect) commonArgs(req agentic.Request) []string {
	var args []string
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if req.MaxTurns > 0 {
		args = append(args, "--max-turns", strconv.Itoa(req.MaxTurns))
	}
	if req.SessionID != "" {
		args = append(args, p.ResumeArgs(req.SessionID)...)
	}
	return args
}

// ResumeArgs continues a prior session.
func (p *dialect) ResumeArgs(sessionID string) []string {
	return []string{"--resume", sessionID}
}

// dialectEnv makes the CLI behave like a program being scripted.
//
// These are not credentials, so they apply under ambient and isolated
// credentials alike.
func (p *dialect) dialectEnv() map[string]string {
	env := map[string]string{
		// Claude Code updates itself by default, and the pin exists precisely
		// so the envelope schema and flag spelling stay the ones this package
		// was written against.
		"DISABLE_AUTOUPDATER": "1",
		// Ink draws a TUI when it thinks it has one. Every byte here is
		// parsed, so the redraws are noise in the middle of it.
		"NO_COLOR": "1",
		"TERM":     "dumb",
	}
	if p.configDir != "" {
		env["CLAUDE_CONFIG_DIR"] = p.configDir
		// A Node program that writes a cache beside its config should write it
		// inside the directory the caller nominated.
		env["HOME"] = p.configDir
	}
	return env
}

// AuthEnv carries a subscription token.
//
// Never --bare on the command line: the CLI does not read a token from there
// and falls through the precedence chain to whatever is next, which is the
// silent misroute this whole arrangement exists to prevent.
func (p *dialect) AuthEnv(token string) map[string]string {
	return map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": token}
}

// DenyEnv is every variable that can redirect Claude Code away from the token
// it was handed.
//
// Precedence runs cloud vars → ANTHROPIC_AUTH_TOKEN → ANTHROPIC_API_KEY →
// apiKeyHelper → CLAUDE_CODE_OAUTH_TOKEN. The injected token is LAST, and every
// one of these outranks it — which is why the list is long rather than the two
// obvious entries.
//
// It is NOT a proof of completeness: Claude Code can add a variable tomorrow
// and this file would not know. The guarantee comes from the driver building
// the environment from a fixed list; this is the backstop for the day someone
// adds a pass-through and that stops being true.
func (p *dialect) DenyEnv() []string {
	return []string{
		"ANTHROPIC_API_KEY",
		"ANTHROPIC_AUTH_TOKEN",
		"ANTHROPIC_BASE_URL",
		"ANTHROPIC_PROFILE",
		"ANTHROPIC_MODEL",
		// Carries an Authorization header, which reroutes billing outright
		// without touching any of the variables above.
		"ANTHROPIC_CUSTOM_HEADERS",
		"ANTHROPIC_BEDROCK_BASE_URL",
		"ANTHROPIC_VERTEX_BASE_URL",
		"CLAUDE_CODE_USE_BEDROCK",
		"CLAUDE_CODE_USE_VERTEX",
		"CLAUDE_CODE_USE_FOUNDRY",
		"AWS_ACCESS_KEY_ID",
		"AWS_SECRET_ACCESS_KEY",
		"AWS_SESSION_TOKEN",
		"AWS_PROFILE",
		"AWS_REGION",
		"AWS_BEARER_TOKEN_BEDROCK",
		"GOOGLE_APPLICATION_CREDENTIALS",
		"GOOGLE_CLOUD_PROJECT",
		"CLOUD_ML_REGION",
		"AZURE_OPENAI_API_KEY",
	}
}

// Compile-time proof of exactly which capabilities this provider claims. The
// driver discovers the same set by type assertion; this fails at build time
// instead of at the call that needed one.
var (
	_ agentic.Provider      = (*Provider)(nil)
	_ agentic.Isolator      = (*Provider)(nil)
	_ agentic.ModelResolver = (*Provider)(nil)
	_ agentic.Resumer       = (*Provider)(nil)
	_ agentic.Streamer      = (*Provider)(nil)
	_ agentic.Installer     = (*Provider)(nil)

	// The same dialect, minus the one capability that depends on owning the
	// binary. A PathProvider that gained an Installer would be claiming to
	// have verified a build it merely found.
	_ agentic.Provider      = (*PathProvider)(nil)
	_ agentic.Isolator      = (*PathProvider)(nil)
	_ agentic.ModelResolver = (*PathProvider)(nil)
	_ agentic.Resumer       = (*PathProvider)(nil)
	_ agentic.Streamer      = (*PathProvider)(nil)
)

// Install downloads and verifies a version.
//
// An empty version means the pin, so "install what you need" is a request a
// caller can make without knowing the number.
func (p *Provider) Install(ctx context.Context, version string) (agentic.InstallResult, error) {
	if version == "" {
		version = p.version
	}
	return p.installer.Install(ctx, version)
}

// Installed lists the versions present, newest first.
func (p *Provider) Installed(ctx context.Context) ([]string, error) {
	return p.installer.Installed(ctx)
}

// Prune trims old versions, and never the pinned one.
//
// The provider supplies that protection rather than the caller, because the
// provider is what knows which version it is about to execute. A retention
// policy that could delete it would trade a full disk for a broken install.
func (p *Provider) Prune(ctx context.Context, keep int) error {
	return p.installer.Prune(ctx, keep, p.version)
}
