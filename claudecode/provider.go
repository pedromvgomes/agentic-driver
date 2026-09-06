package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"

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

// StreamCommand renders a Request as `claude -p`.
//
// --verbose is not optional: without it the CLI collapses stream-json down to
// the single terminal envelope, and every intermediate event a caller asked to
// watch is lost.
func (p *dialect) StreamCommand(req agentic.Request) (agentic.Invocation, error) {
	if req.Prompt == "" {
		return agentic.Invocation{}, fmt.Errorf("%w: claude -p needs a prompt", agentic.ErrInvalidRequest)
	}

	common, err := p.commonArgs(req)
	if err != nil {
		return agentic.Invocation{}, err
	}
	args := append(p.baseArgs(), "-p", req.Prompt, "--output-format", "stream-json", "--verbose")
	return agentic.Invocation{Args: append(args, common...), Env: p.dialectEnv()}, nil
}

// commonArgs renders the optional parts of a Request. A field left zero is a
// flag left off, so the CLI's own default applies.
func (p *dialect) commonArgs(req agentic.Request) ([]string, error) {
	var args []string
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if req.MaxTurns > 0 {
		turnArgs, err := p.TurnLimitArgs(req.MaxTurns)
		if err != nil {
			return nil, err
		}
		args = append(args, turnArgs...)
	}
	if req.SessionID != "" {
		args = append(args, p.ResumeArgs(req.SessionID)...)
	}
	permArgs, err := p.PermissionArgs(req.PermissionMode, req.AllowedTools)
	if err != nil {
		return nil, err
	}
	args = append(args, permArgs...)
	agentArgs, err := p.AgentArgs(req.Agents)
	if err != nil {
		return nil, err
	}
	args = append(args, agentArgs...)
	if len(req.Schema) > 0 {
		schemaArgs, err := p.SchemaArgs(req.Schema)
		if err != nil {
			return nil, err
		}
		args = append(args, schemaArgs...)
	}
	return args, nil
}

// SchemaArgs binds the final answer to a JSON Schema.
//
// The schema goes on the command line as the document itself, so nothing is
// written to disk and the argv is a pure function of the request.
//
// The constraint is served by a tool the CLI offers the model, StructuredOutput,
// and that tool is NOT subject to --allowedTools: a run restricted to Read still
// answers in the required shape. Reconciling the two here would refuse requests
// the CLI honours perfectly well.
func (p *dialect) SchemaArgs(schema json.RawMessage) ([]string, error) {
	return []string{"--json-schema", string(schema)}, nil
}

// TurnLimitArgs bounds the agent loop.
//
// The unit is one iteration of that loop, not one exchange with the user: a
// single prompt that calls three tools spends several. A bound of one answers
// without ever calling a tool.
func (p *dialect) TurnLimitArgs(maxTurns int) ([]string, error) {
	if maxTurns < 1 {
		return nil, fmt.Errorf("%w: a turn limit of %d bounds the loop to nothing", agentic.ErrInvalidRequest, maxTurns)
	}
	return []string{"--max-turns", strconv.Itoa(maxTurns)}, nil
}

// ResumeArgs continues a prior session.
func (p *dialect) ResumeArgs(sessionID string) []string {
	return []string{"--resume", sessionID}
}

// AgentArgs declares a roster as --agents, whose value is a JSON object keyed
// by the name the run delegates by.
//
// encoding/json sorts map keys, so the same roster renders the same argv every
// time — which is what lets a test assert on it, and what keeps a cached or
// logged invocation comparable between runs.
func (p *dialect) AgentArgs(agents map[string]agentic.Agent) ([]string, error) {
	if len(agents) == 0 {
		return nil, nil
	}

	// The CLI reads this as one JSON object, so a blank field does not fail —
	// it produces an agent the model can see and cannot use, which is a harder
	// thing to notice than a refused request.
	spec := make(map[string]agentSpec, len(agents))
	for name, a := range agents {
		switch {
		case name == "":
			return nil, fmt.Errorf("%w: an agent has no name", agentic.ErrInvalidRequest)
		case a.Description == "":
			return nil, fmt.Errorf("%w: agent %q has no description, so nothing would delegate to it", agentic.ErrInvalidRequest, name)
		case a.Prompt == "":
			return nil, fmt.Errorf("%w: agent %q has no prompt", agentic.ErrInvalidRequest, name)
		}
		spec[name] = agentSpec{Description: a.Description, Prompt: a.Prompt}
	}

	encoded, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("%w: encoding agents: %w", agentic.ErrInvalidRequest, err)
	}
	return []string{"--agents", string(encoded)}, nil
}

// agentSpec is the wire shape of one --agents entry.
type agentSpec struct {
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
}

// permissionModes is the closed set the pinned CLI accepts.
//
// Unlike a model name, this is a vendor enum the CLI validates itself: an
// unrecognised mode exits 1 with an empty stdout, which reaches a caller as
// ErrProviderUnavailable — a typo wearing the costume of an outage, and one
// that invites a retry loop. Refusing here names the actual problem.
//
// It is pinned knowledge like the flag spelling around it, so it moves when
// PinnedVersion moves.
var permissionModes = []string{
	"acceptEdits",
	"auto",
	"bypassPermissions",
	"manual",
	"dontAsk",
	"plan",
}

// PermissionArgs constrains what the run may do.
//
// Both flags are argv, not settings-file keys, so they apply to a run started
// with --setting-sources ” — the whole reason a scripted run can be given a
// narrow grant at all.
//
// The two are not cross-checked, and one combination is worth knowing about:
// bypassPermissions with an allowlist renders both, and the mode wins. The
// allowlist is then documentation rather than a restriction.
func (p *dialect) PermissionArgs(mode string, allowedTools []string) ([]string, error) {
	var args []string
	if mode != "" {
		if !slices.Contains(permissionModes, mode) {
			return nil, fmt.Errorf("%w: permission mode %q is not one of %s",
				agentic.ErrInvalidRequest, mode, strings.Join(permissionModes, ", "))
		}
		args = append(args, "--permission-mode", mode)
	}
	if len(allowedTools) == 0 {
		return args, nil
	}

	for _, tool := range allowedTools {
		if err := checkToolPattern(tool); err != nil {
			return nil, err
		}
	}
	// One argument, comma-separated: a tool pattern such as `Bash(agtk memory
	// anchor*)` contains spaces, and the space-separated spelling would split
	// it across argv entries.
	return append(args, "--allowedTools", strings.Join(allowedTools, ",")), nil
}

// checkToolPattern refuses an entry the CLI would read as something wider than
// what it says.
//
// The CLI's tool list splits on whitespace that sits OUTSIDE parentheses, so a
// space in the wrong place does not fail — it grants more. `Bash (agtk memory
// anchor*)` becomes the bare grant `Bash`, which is every command; and any
// entry yielding a bare `*` grants every tool outright. Both read, to a human
// skimming a config, exactly like the narrow grant that was intended.
//
// A blank entry is refused for the mirror-image reason: the CLI reads it as a
// tool named "", which matches nothing and quietly narrows the grant instead.
func checkToolPattern(tool string) error {
	if strings.TrimSpace(tool) == "" {
		return fmt.Errorf("%w: an allowed tool is blank", agentic.ErrInvalidRequest)
	}
	if strings.Contains(tool, ",") {
		// The entries are joined with commas, so one containing a comma
		// silently becomes two grants.
		return fmt.Errorf("%w: allowed tool %q contains a comma, which separates entries", agentic.ErrInvalidRequest, tool)
	}

	depth := 0
	for _, r := range tool {
		switch {
		case r == '(':
			depth++
		case r == ')':
			depth--
		case depth == 0 && unicode.IsSpace(r):
			return fmt.Errorf("%w: allowed tool %q has whitespace outside its parentheses, which the CLI reads as a wider grant",
				agentic.ErrInvalidRequest, tool)
		}
	}
	if depth != 0 {
		return fmt.Errorf("%w: allowed tool %q has unbalanced parentheses", agentic.ErrInvalidRequest, tool)
	}
	if strings.TrimSpace(strings.Trim(tool, "*")) == "" {
		return fmt.Errorf("%w: allowed tool %q grants every tool; leave AllowedTools empty to ask for no restriction",
			agentic.ErrInvalidRequest, tool)
	}
	return nil
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
	_ agentic.AgentDefiner  = (*Provider)(nil)
	_ agentic.Permitter     = (*Provider)(nil)
	_ agentic.TurnLimiter   = (*Provider)(nil)
	_ agentic.Installer     = (*Provider)(nil)

	_ agentic.SchemaConstrainer = (*Provider)(nil)

	// The same dialect, minus the one capability that depends on owning the
	// binary. A PathProvider that gained an Installer would be claiming to
	// have verified a build it merely found.
	_ agentic.Provider      = (*PathProvider)(nil)
	_ agentic.Isolator      = (*PathProvider)(nil)
	_ agentic.ModelResolver = (*PathProvider)(nil)
	_ agentic.Resumer       = (*PathProvider)(nil)
	_ agentic.AgentDefiner  = (*PathProvider)(nil)
	_ agentic.Permitter     = (*PathProvider)(nil)
	_ agentic.TurnLimiter   = (*PathProvider)(nil)

	_ agentic.SchemaConstrainer = (*PathProvider)(nil)
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
