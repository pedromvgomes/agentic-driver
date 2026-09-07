// Package codex is the OpenAI Codex CLI dialect.
//
// It exists to keep the interfaces honest. An interface designed against one
// implementation is wrong in ways nobody can see from inside that
// implementation, and `codex exec` is the closest analogue to `claude -p` that
// is genuinely a different program: different flag spelling, a different
// credential variable, a different set of variables that can redirect it, and a
// vendor whose distribution supports a different guarantee.
//
// It vendors its own copy of the CLI at an exact version, checked against a
// digest committed in this repository, and executes it by absolute path. What
// it does NOT claim is provenance: OpenAI publishes sigstore bundles for its
// linux-musl release assets alone, and the npm channel that does carry an
// attestation for every platform can only be verified with a dependency tree an
// order of magnitude larger than this library. That attestation is checked by
// hand when the pin moves; see docs/adr/0004.
//
// `codex exec --json` emits JSONL from its first line to its last, and has no
// envelope mode to fall back to: -o/--output-last-message writes bare text to a
// file rather than a document to stdout. A Result is therefore a fold over a
// whole run, which is why this package decodes a stream rather than parsing a
// document.
package codex

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	agentic "github.com/pedromvgomes/agentic-driver"
)

// ID is the provider's stable identifier.
const ID = "codex"

// BinaryName is what the platform package calls the executable, and what a
// PathProvider looks up on PATH.
const BinaryName = "codex"

// dialect is everything both providers share: the flags codex takes, the
// envelope it prints, and the variables that carry or redirect its credential.
// Which binary runs is the only thing they differ on.
type dialect struct {
	// configDir is the directory nominated by WithConfigDir, exported to the
	// child as CODEX_HOME. Empty means the CLI uses its own default.
	configDir string

	// publishing holds one lock per schema file, keyed by its name.
	//
	// It saves work and nothing else. Correctness across concurrent runs comes
	// from publishing through a rename, which is atomic and which converges
	// whichever writer lands last, and that holds between processes too where a
	// lock could not reach. What the lock removes is the redundancy: runs
	// sharing a schema arrive together, and without it each one writes what its
	// siblings are writing at the same moment.
	publishing sync.Map
}

// Provider runs a vendored Codex, installed at a pinned version whose tarball
// digest is committed in this package.
//
// It implements Pinner, which is also what makes the binary an absolute path:
// there is no PATH lookup for something else to win and no launcher symlink to
// repoint, so the build that runs is the build the pin names. It does NOT
// implement Installer — see the assertions at the foot of this file.
type Provider struct {
	dialect
	installer *Installer
	version   string
}

// PathProvider runs whichever Codex is on PATH.
//
// It deliberately implements neither Pinner nor Installer, and the absence is
// the point: a provider that runs someone else's binary has not chosen a
// version and cannot vouch for one, and the driver reads that absence rather
// than taking anyone's word for it. Use it on a developer machine, where the
// answer to "which codex" is "the one I already have", and on Windows, where
// this package vendors nothing.
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

// WithVersion overrides the pinned version New installs and runs. It must be a
// version this package commits digests for, since there would otherwise be
// nothing to check a download against. It is meaningless to a PathProvider,
// which runs whatever is on PATH, and NewOnPath refuses it rather than ignoring
// it.
func WithVersion(v string) Option {
	return func(c *config) { c.version, c.versionSet = v, true }
}

// WithConfigDir sets CODEX_HOME, the directory a spawned codex reads its
// configuration and its credential from.
//
// Without it the CLI uses the caller's own ~/.codex, so a run authenticates as
// the human sitting at the machine and writes to their profile. Nominate a
// directory whenever the run is not that human's own.
//
// The directory carries the credential, not just settings: `codex exec` reads
// its session from $CODEX_HOME/auth.json, which is why --ignore-user-config
// still honours this variable. A nominated directory holding a session
// therefore OUTRANKS the token Isolated injects as OPENAI_API_KEY — codex uses
// the session and never attempts the key. Pair a config dir holding a session
// with Ambient credentials, and Isolated with a directory that has none.
//
// Unlike claudecode's equivalent this does not also override HOME. There the
// override exists because a Node program writes its cache beside its config;
// codex resolves its whole profile from this one variable, so overriding HOME
// would buy nothing and would silently defeat Driver.WithHome.
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
		return nil, fmt.Errorf("codex: pinned version: %w", err)
	}
	// Refused at construction rather than at Install, and refused for THIS
	// machine rather than for the version in the abstract.
	//
	// A version with no committed digest can never be installed, so a provider
	// configured with one is a driver that will fail every time it is asked to
	// fetch anything — at the call that needed the binary rather than the call
	// that chose it. A version pinned for some platforms and not this one fails
	// identically, so asking whether the version appears at all would move the
	// wrong half of the question forward.
	if _, err := PinnedDigest(cfg.version, inst.release.platform); err != nil {
		return nil, fmt.Errorf("codex: %w", err)
	}

	return &Provider{
		dialect:   dialect{configDir: cfg.configDir},
		installer: inst,
		version:   cfg.version,
	}, nil
}

// NewOnPath builds a provider that runs whichever codex is on PATH.
//
// This is the developer-machine case, and it pairs with ambient credentials:
// use the CLI that is already installed, already authenticated, already the one
// being used by hand. What it gives up is the pin — the binary is whatever PATH
// resolves to, at whatever version it has updated itself to — which is why it is
// a separate constructor rather than a flag on the other one.
func NewOnPath(opts ...Option) (*PathProvider, error) {
	cfg := settings(opts)

	// Refused rather than ignored: a caller pinning a version is asking for a
	// specific build, and answering that request with "whatever is on PATH"
	// would be the silent substitution the pin exists to prevent.
	if cfg.versionSet {
		return nil, errors.New("codex: WithVersion needs a vendored install; NewOnPath runs whatever is on PATH")
	}

	return &PathProvider{dialect: dialect{configDir: cfg.configDir}}, nil
}

// Version reports the version this provider runs.
func (p *Provider) Version() string { return p.version }

// BinaryPath is the absolute path of the pinned build.
func (p *Provider) BinaryPath() string { return p.installer.Path(p.version) }

// Install downloads a version and refuses any bytes but the ones its committed
// digest names.
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

func (p *dialect) Descriptor() agentic.Descriptor {
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
func (p *dialect) StreamCommand(req agentic.Request) (agentic.Invocation, error) {
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
	if req.Schema != nil {
		schemaArgs, err := p.SchemaArgs(req.Schema)
		if err != nil {
			return agentic.Invocation{}, err
		}
		args = append(args, schemaArgs...)
	}
	// The prompt is positional and last, so nothing it contains can be read as
	// a flag.
	args = append(args, req.Prompt)

	return agentic.Invocation{Args: args, Env: p.dialectEnv()}, nil
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
func (p *dialect) PermissionArgs(mode string, allowedTools []string) ([]string, error) {
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

// schemaDirName is the directory schema files are written under, inside the
// system temporary directory. The caller's user id is appended, because the
// system temporary directory is shared between accounts on a Unix host and a
// directory two users contend for belongs safely to neither.
const schemaDirName = "agentic-codex-schema"

// SchemaArgs binds the final answer to a JSON Schema.
//
// Codex takes a PATH rather than the document, so the schema has to exist as a
// file before the process starts. The file is named for the SHA-256 of its own
// contents, which is what keeps that from being a per-run side effect: the same
// schema always renders the same argv, so a logged or cached invocation stays
// comparable and Run and Stream cannot issue different commands for one
// request. Two runs sharing a schema share the file, and there is nothing
// per-run left behind to reclaim.
//
// The digest names the file; it does not vouch for it. A file's contents are
// established by reading them, so a path that already exists is compared
// against the schema and republished when it differs — the name is a label
// anything able to write the directory could have chosen, and a run constrained
// to a schema nobody asked for still answers, in valid JSON, with nothing to
// mark it wrong.
func (p *dialect) SchemaArgs(schema json.RawMessage) ([]string, error) {
	sum := sha256.Sum256(schema)
	// The name carries the whole digest: a truncated one makes two different
	// schemas collide onto one file, and the run that lost would be constrained
	// to a shape nobody asked it for.
	name := hex.EncodeToString(sum[:]) + ".json"

	// One publisher at a time per schema, so a fan-out of runs sharing one does
	// the work once instead of every goroutine repeating it. Loaded before
	// LoadOrStore so the common path does not allocate a lock it discards.
	gate, ok := p.publishing.Load(name)
	if !ok {
		gate, _ = p.publishing.LoadOrStore(name, new(sync.Mutex))
	}
	lock := gate.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	// The file is checked on every run rather than remembered as published. A
	// temporary directory is swept by the system, and a process that trusted
	// its own earlier work would name a file that had since been reaped and go
	// on naming it for as long as it lived. Confirming costs two syscalls and a
	// read of a small file, against a run that is about to spawn a subprocess.
	//
	// The confirmation is of what is on disk NOW, not of what codex will open.
	// Codex takes a path, so nothing here can hand it a descriptor, and a writer
	// sharing this user's authority — the agent this driver is about to spawn
	// among them — can still replace the file in between. The directory keeps
	// out everyone else; within one account this is a shared file by design.
	dir, err := p.schemaDir()
	if err != nil {
		return nil, fmt.Errorf("%w: codex: %w", agentic.ErrProviderUnavailable, err)
	}
	path := filepath.Join(dir, name)
	if err := publish(path, schema); err != nil {
		return nil, fmt.Errorf("%w: codex: writing the schema: %w", agentic.ErrProviderUnavailable, err)
	}
	return []string{"--output-schema", path}, nil
}

// schemaDir returns a directory this process can trust to hold only what it put
// there.
//
// MkdirAll succeeds on a directory that already exists, whoever owns it and
// whatever its mode, so creating one proves nothing about it. The checks after
// it are what make the trust real: a directory owned by another account, opened
// to another account, or standing in for something that is not a directory is
// refused rather than used, because everything downstream treats a file found
// there as this process's own.
func (p *dialect) schemaDir() (string, error) {
	root, err := schemaRoot()
	if err != nil {
		return "", err
	}

	dir := filepath.Join(root, fmt.Sprintf("%s-%d", schemaDirName, callerID()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("making a directory for the schema: %w", err)
	}

	// Lstat, not Stat: a symlink standing where the directory should be is the
	// thing being refused, and Stat would report whatever it points at.
	info, err := os.Lstat(dir)
	if err != nil {
		return "", fmt.Errorf("inspecting the schema directory: %w", err)
	}
	switch {
	case !info.IsDir():
		return "", fmt.Errorf("%s is not a directory", dir)
	case !ownedByCaller(info):
		return "", fmt.Errorf("%s belongs to another user", dir)
	case !privateToCaller(info):
		return "", fmt.Errorf("%s is open to other users (mode %#o)", dir, info.Mode().Perm())
	}
	return dir, nil
}

// publish makes path hold content, and is a no-op when it already does.
//
// The existing file is READ rather than merely found: a name that is a digest
// says what the contents must be, not what they are, and the difference is
// everything on a machine where something else could have created the file
// first. A stale or planted document is replaced, and so is anything at the
// name that is not a regular file — a symlink, which would otherwise send codex
// to read whatever it points at, or a directory, which a rename cannot write
// over at all.
//
// The replacement is written under a temporary name and renamed into place, so
// a concurrent run reads either the previous complete file or this one and
// never a half-written document, which codex would reject as malformed JSON
// before the model ever ran.
func publish(path string, content []byte) error {
	switch info, err := os.Lstat(path); {
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		// Anything other than absence is reported as itself. Falling through
		// would report whatever the write failed with instead, naming a
		// temporary file that has nothing to do with the obstruction.
		return fmt.Errorf("inspecting %s: %w", path, err)

	case err == nil && info.Mode().IsRegular():
		// The path carries no caller-supplied component: every segment is
		// either the verified schema directory or the hex of a digest this
		// package computed, and Lstat has just established this one is a
		// regular file rather than a link out of it.
		//
		// A file that cannot be read is republished rather than reported: not
		// being able to establish the contents is the same answer as their
		// being wrong.
		current, readErr := os.ReadFile(path) // #nosec G304 -- a digest-named file under a directory this package verified
		if readErr == nil && bytes.Equal(current, content) {
			return nil
		}
	case err == nil:
		// Removing a symlink removes the link and not its target, so nothing
		// outside this directory is touched.
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".schema-*")
	if err != nil {
		return err
	}
	// Removing the temporary name is safe after a successful rename too: it no
	// longer refers to the published file. Without it, a failure between here
	// and the rename leaves a partial file nothing will ever clean up.
	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// AuthEnv carries an OpenAI key.
//
// A different variable from claudecode's, which is the point of the vocabulary
// being the provider's: nothing generic could have named it.
func (p *dialect) AuthEnv(token string) map[string]string {
	return map[string]string{"OPENAI_API_KEY": token}
}

// dialectEnv is the non-secret environment every codex run needs.
//
// HOME is deliberately absent. codex resolves its configuration AND its
// credential from CODEX_HOME alone, so nominating a directory needs one
// variable; setting HOME as well would override the one Driver.WithHome
// nominated, without redirecting anything CODEX_HOME had not already.
func (p *dialect) dialectEnv() map[string]string {
	env := map[string]string{
		// codex draws a TUI when it thinks it has one. Every byte here is
		// parsed, so the redraws are noise in the middle of it.
		"NO_COLOR": "1",
		"TERM":     "dumb",
	}
	if p.configDir != "" {
		env["CODEX_HOME"] = p.configDir
	}
	return env
}

// DenyEnv is every variable that can redirect Codex away from the key it was
// handed.
//
// Entirely different from claudecode's list, and shorter — which is the fact
// that makes the list a provider's property rather than the library's. A shared
// list would have to be the union, and every entry in it would be wrong for
// somebody.
func (p *dialect) DenyEnv() []string {
	denied := []string{
		"OPENAI_API_KEY",
		"OPENAI_BASE_URL",
		"OPENAI_API_BASE",
		"OPENAI_ORGANIZATION",
		"OPENAI_PROJECT",
		"AZURE_OPENAI_API_KEY",
		"AZURE_OPENAI_ENDPOINT",
		"CODEX_API_KEY",
	}
	// CODEX_HOME redirects the whole profile, and the profile is where the
	// credential lives: a session under it outranks the token AuthEnv injects,
	// so an inherited value reroutes the run's identity outright.
	//
	// It is denied only when this dialect does not set it. buildEnv scrubs the
	// assembled environment, dialect variables included, so denying a variable
	// this provider exports would strip the caller's own nomination — which is
	// why the claudecode dialect likewise omits CLAUDE_CONFIG_DIR. With no
	// config dir there is nothing to protect and the backstop applies.
	if p.configDir == "" {
		denied = append(denied, "CODEX_HOME")
	}
	return denied
}

// Compile-time proof of which capabilities each provider claims. Neither
// implements Resumer nor AgentDefiner nor TurnLimiter: absent capabilities are
// absent from the type, and the driver answers for them without spawning
// anything.
//
// TurnLimiter is absent because codex has no turn bound to express, and
// AgentDefiner because it has no vocabulary for declaring a roster on the
// command line.
//
// Installer is absent from BOTH, and that is the one worth reading twice. The
// vendored Provider is a Pinner: it settles which bytes run, by refusing any
// tarball but the one whose digest this package commits, which is what keeps
// parse.go and the fixtures in testdata describing the same CLI. Installer
// would additionally assert that a named publisher signed those bytes, and that
// is a claim this package cannot make on every platform it vendors on with the
// dependencies it is willing to carry. Claiming it anyway — true on linux,
// hollow on darwin — is worse than not claiming it, because the caller who
// asserts on Installer to decide whether to trust a build would get the same
// answer in both cases.
var (
	_ agentic.Provider          = (*Provider)(nil)
	_ agentic.Isolator          = (*Provider)(nil)
	_ agentic.Permitter         = (*Provider)(nil)
	_ agentic.SchemaConstrainer = (*Provider)(nil)
	_ agentic.Pinner            = (*Provider)(nil)

	// The same dialect, minus the capability that depends on owning the binary.
	// A PathProvider that gained a Pinner would be claiming to have chosen a
	// build it merely found.
	_ agentic.Provider          = (*PathProvider)(nil)
	_ agentic.Isolator          = (*PathProvider)(nil)
	_ agentic.Permitter         = (*PathProvider)(nil)
	_ agentic.SchemaConstrainer = (*PathProvider)(nil)
)
