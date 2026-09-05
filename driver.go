package agentic

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// defaultTimeout bounds an invocation the caller has not bounded itself.
const defaultTimeout = 5 * time.Minute

// killGrace bounds the wait for a child that ignores the kill signal, or holds
// stdout open after exiting. Without it, waiting on the process can block past
// the deadline that was supposed to end it.
const killGrace = 5 * time.Second

// Driver runs one provider's CLI.
//
// It owns everything about the process and nothing about the dialect: a change
// to how timeouts, cancellation, environments or stderr are handled is a change
// to this file, and reaches every provider at once.
type Driver struct {
	provider   Provider
	descriptor Descriptor

	binary  string
	creds   Credentials
	timeout time.Duration
	model   string
	path    string
	home    string
	workDir string
}

// Option configures a Driver.
type Option func(*Driver)

// WithBinary pins the executable to an absolute path, so no PATH lookup can be
// won by something else and no launcher symlink can be repointed. Without it
// the driver resolves the descriptor's binary name on PATH.
func WithBinary(path string) Option {
	return func(d *Driver) { d.binary = path }
}

// WithCredentials selects the credential mode. The default is Ambient.
func WithCredentials(c Credentials) Option {
	return func(d *Driver) { d.creds = c }
}

// WithModel sets the model every invocation uses unless Request.Model names
// another. It accepts whatever the provider's own CLI accepts, including a
// family alias such as "opus" where the provider implements ModelResolver.
func WithModel(name string) Option {
	return func(d *Driver) { d.model = name }
}

// WithTimeout bounds every invocation that does not set Request.Timeout.
func WithTimeout(t time.Duration) Option {
	return func(d *Driver) { d.timeout = t }
}

// WithChildPath sets the PATH given to an isolated child.
func WithChildPath(p string) Option {
	return func(d *Driver) { d.path = p }
}

// WithHome sets the HOME given to an isolated child. It defaults to the
// parent's, which means an isolated run still reads the caller's own agent
// configuration — nominate a directory to prevent that.
func WithHome(dir string) Option {
	return func(d *Driver) { d.home = dir }
}

// WithWorkDir sets the working directory for invocations that do not set
// Request.WorkDir.
func WithWorkDir(dir string) Option {
	return func(d *Driver) { d.workDir = dir }
}

// New builds a driver for a provider.
//
// It resolves the binary now rather than at the first Run, so a missing CLI is
// reported where the caller is configuring things instead of in the middle of
// a request.
func New(p Provider, opts ...Option) (*Driver, error) {
	if p == nil {
		return nil, errors.New("agentic: no provider")
	}

	d := &Driver{
		provider:   p,
		descriptor: p.Descriptor(),
		creds:      Ambient(),
		timeout:    defaultTimeout,
		path:       minimalPath,
		home:       os.Getenv("HOME"),
	}
	for _, opt := range opts {
		opt(d)
	}

	if d.descriptor.ID == "" {
		return nil, errors.New("agentic: provider has no ID")
	}
	if d.creds.isolated {
		if _, ok := p.(Isolator); !ok {
			return nil, fmt.Errorf("%w: %s", ErrIsolationUnsupported, d.descriptor.ID)
		}
	}

	if err := d.resolveBinary(); err != nil {
		return nil, err
	}

	return d, nil
}

// resolveBinary settles which executable the driver runs.
//
// A vendored provider names its own absolute path, and its existence is NOT
// checked here: on a fresh machine nothing is installed yet, and refusing to
// construct the driver would make Install unreachable through the very object
// that offers it. A missing binary surfaces at Run, as ErrProviderUnavailable.
//
// Everything else is looked up on PATH once, at construction, so a missing CLI
// is reported where the caller is configuring things rather than in the middle
// of a request.
func (d *Driver) resolveBinary() error {
	if d.binary != "" {
		return nil
	}
	if inst, ok := d.provider.(Installer); ok {
		d.binary = inst.BinaryPath()
		if d.binary == "" {
			return fmt.Errorf("agentic: provider %s vendors a binary but names no path", d.descriptor.ID)
		}
		return nil
	}
	if d.descriptor.Binary == "" {
		return fmt.Errorf("agentic: provider %s names no binary", d.descriptor.ID)
	}
	found, err := exec.LookPath(d.descriptor.Binary)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrProviderUnavailable, d.descriptor.ID, err)
	}
	d.binary = found
	return nil
}

// Ready reports whether a run could actually start: the binary the driver
// would execute exists and can be executed.
//
// A driver constructs successfully without one, because a provider that vendors
// its binary needs Install to be reachable before that binary exists — so
// "configured" and "runnable" are genuinely different states and a caller needs
// to be able to tell them apart. Without this the difference only surfaces as a
// fork/exec failure in the middle of a request.
//
// Mere existence is not enough: a directory with the right name, a zero-byte
// file, or one whose execute bit is stripped all satisfy a stat and none can be
// run.
func (d *Driver) Ready() error {
	info, err := os.Stat(d.binary)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("%w: %s is not installed at %s%s",
			ErrProviderUnavailable, d.descriptor.ID, d.binary, d.installHint())
	case err != nil:
		return fmt.Errorf("%w: %s: %w", ErrProviderUnavailable, d.descriptor.ID, err)
	case info.IsDir():
		return fmt.Errorf("%w: %s is a directory, not a binary", ErrProviderUnavailable, d.binary)
	case info.Size() == 0:
		return fmt.Errorf("%w: %s is empty%s", ErrProviderUnavailable, d.binary, d.installHint())
	case !Executable(info):
		return fmt.Errorf("%w: %s is not executable", ErrProviderUnavailable, d.binary)
	}
	return nil
}

// Executable reports whether a stat result describes something that could be
// run: a regular file, not empty, with an execute bit.
//
// Exported so a provider deciding whether its own binary is installed uses the
// same predicate the driver uses to decide whether it is runnable. Two
// definitions differing by a permission bit would let one call a version
// runnable while the other treats it as absent — reinstalling it and failing to
// publish over the directory already there, on every retry.
//
// Any execute bit counts. Which one applies depends on the process's uid and
// groups, which a stat does not answer, so checking only the owner bit would
// reject a binary installed by a package manager and executed by everyone.
func Executable(info os.FileInfo) bool {
	return info.Mode().IsRegular() && info.Size() > 0 && info.Mode().Perm()&0o111 != 0
}

// installHint names the way out, when there is one. A provider that vendors its
// binary can fetch it; one that found it on PATH cannot, and saying "install it"
// there would send the caller to a method that does not exist.
func (d *Driver) installHint() string {
	if _, ok := d.provider.(Installer); ok {
		return "; call Install"
	}
	return ""
}

// Descriptor reports which provider this driver runs.
func (d *Driver) Descriptor() Descriptor { return d.descriptor }

// Binary reports the executable the driver will run.
func (d *Driver) Binary() string { return d.binary }

// Model reports the model this driver currently runs: the concrete name a
// request that names none would be answered by.
//
// It is the resolved form, so a driver configured with "opus" answers with the
// build that alias currently means. An empty answer means no model has been
// chosen and the CLI's own default applies — which is a real state, and one a
// caller displaying "active model" needs to be able to tell apart from a
// choice.
func (d *Driver) Model() string { return d.ResolveModel(d.model) }

// ResolveModel reports which model a name currently means to this provider.
//
// A provider that does not resolve aliases returns the name unchanged, so a
// caller can display the answer without first asking whether resolution is
// supported — there is nothing here for an absent capability to fail at.
func (d *Driver) ResolveModel(name string) string {
	resolver, ok := d.provider.(ModelResolver)
	if !ok {
		return name
	}
	return resolver.ResolveModel(name)
}

// Run executes one request and returns what the provider made of it.
//
// A non-nil error means the invocation could not be carried out or could not be
// understood. A CLI that ran and reported a failure of its own comes back as a
// Result with IsError set and a nil error: that is a verdict, and discarding it
// as an outage is exactly the confusion this split exists to prevent.
func (d *Driver) Run(ctx context.Context, req Request) (Result, error) {
	req, inv, err := d.invocation(req)
	if err != nil {
		return Result{}, err
	}

	stdout, stderr, code, err := d.exec(ctx, req, inv)

	// A caller that went away is never told the run succeeded. Everything else
	// about the run is a question of what came back; this is a question of
	// whether anyone is still listening, and answering "fine" to a context the
	// caller cancelled during shutdown invites acting on it.
	if ctx.Err() != nil {
		if err != nil {
			return Result{}, err
		}
		return Result{}, fmt.Errorf("%w: %s was cancelled: %w",
			ErrProviderUnavailable, d.descriptor.ID, ctx.Err())
	}

	// Parsed BEFORE the exit status is judged. A CLI reports a rejected
	// credential or a refused request in its JSON body and may still exit
	// non-zero, so discarding the output on a failed exit turns a clear verdict
	// into a spurious "provider down".
	//
	// A complete envelope outranks a TIMEOUT for the same reason: the answer
	// arrived and was paid for, and a CLI that flushes its result and then
	// hangs holding stdout open has stalled in its teardown, not in the work.
	result, parseErr := d.provider.Parse(stdout, stderr, code)
	if parseErr == nil {
		return result, nil
	}
	if err != nil {
		return Result{}, err
	}
	return Result{}, fmt.Errorf("%w: %s: %w%s",
		ErrProviderUnavailable, d.descriptor.ID, parseErr, d.suffix(stderr))
}

// prepare settles the request against the driver's own configuration and
// refuses what the provider cannot honour, before any process is started.
//
// Both Run and Stream go through it, so the two dialects cannot drift on which
// requests are acceptable or on what a model name means.
func (d *Driver) prepare(req Request) (Request, error) {
	// Checked here rather than left to the provider, because the failure of
	// dropping the field is silent: a fresh session answers perfectly well, and
	// nothing in the reply says the history was never read.
	if req.SessionID != "" {
		if _, ok := d.provider.(Resumer); !ok {
			return req, fmt.Errorf("%w: %s", ErrResumeUnsupported, d.descriptor.ID)
		}
	}

	// Same reasoning, one step worse: a run whose roster was dropped answers
	// the prompt itself, competently, with none of the context the delegation
	// existed to supply.
	if len(req.Agents) > 0 {
		if _, ok := d.provider.(AgentDefiner); !ok {
			return req, fmt.Errorf("%w: %s", ErrAgentsUnsupported, d.descriptor.ID)
		}
	}

	// A dropped restriction fails in the dangerous direction: the run proceeds
	// with the CLI's own defaults, which are wider than what was asked for.
	if len(req.AllowedTools) > 0 || req.PermissionMode != "" {
		if _, ok := d.provider.(Permitter); !ok {
			return req, fmt.Errorf("%w: %s", ErrPermissionsUnsupported, d.descriptor.ID)
		}
	}

	// The model is settled before the provider sees the request, so Command is
	// handed a name the CLI accepts rather than each provider having to
	// remember to resolve one.
	if req.Model == "" {
		req.Model = d.model
	}
	req.Model = d.ResolveModel(req.Model)

	return req, nil
}

// invocation asks the provider for the command.
func (d *Driver) invocation(req Request) (Request, Invocation, error) {
	req, err := d.prepare(req)
	if err != nil {
		return req, Invocation{}, err
	}

	inv, err := d.provider.Command(req)
	if err != nil {
		return req, Invocation{}, err
	}
	return req, inv, nil
}

// exec runs the child and returns its output and exit code.
//
// The error it returns is about the RUN — the process could not be started,
// the caller went away, the deadline passed. An ordinary non-zero exit is not
// one of those: it comes back as code, with stdout intact, because the provider
// is the only thing that knows what that code means.
func (d *Driver) exec(ctx context.Context, req Request, inv Invocation) (stdout, stderr []byte, code int, err error) {
	timeout := d.timeout
	if req.Timeout > 0 {
		timeout = req.Timeout
	}

	// The parent is kept, not shadowed. Both contexts are done once the
	// deadline passes, so the derived one alone cannot tell "the CLI hung" from
	// "the caller went away" — and reporting a client disconnect as a
	// five-minute timeout sends people hunting a stall that never happened.
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	env, err := d.buildEnv(inv)
	if err != nil {
		return nil, nil, 0, err
	}

	cmd := d.command(ctx, inv.Args, env, req.WorkDir)

	// Bounded, because until the deadline passes the child decides how much
	// memory the parent spends. A CLI looping on an error message would
	// otherwise grow the heap for the whole timeout.
	outBuf := boundedBuffer{limit: maxStdoutCapture}
	errBuf := boundedBuffer{limit: maxStderrCapture}
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	runErr := cmd.Run()
	stdout, stderr = outBuf.Bytes(), errBuf.Bytes()

	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
		return stdout, stderr, 0, nil

	case parent.Err() != nil:
		return stdout, stderr, -1, fmt.Errorf("%w: %s was cancelled: %w",
			ErrProviderUnavailable, d.descriptor.ID, parent.Err())

	case ctx.Err() != nil:
		// The stderr detail is carried here too: a CLI that explains itself
		// before hanging is explaining the very thing being diagnosed.
		return stdout, stderr, -1, fmt.Errorf("%w: %s did not finish within %s%s",
			ErrProviderUnavailable, d.descriptor.ID, timeout, d.suffix(stderr))

	case errors.As(runErr, &exitErr):
		// Not an error from this function's point of view. The provider reads
		// the code alongside the output it accompanies.
		return stdout, stderr, exitErr.ExitCode(), nil

	default:
		// A missing or unrunnable binary is the common case here, and
		// "fork/exec …: no such file or directory" does not say what to do
		// about it. Checked only on the failure path, so a healthy run pays
		// nothing for it.
		if ready := d.Ready(); ready != nil {
			return stdout, stderr, -1, ready
		}
		return stdout, stderr, -1, fmt.Errorf("%w: %s could not be run: %w%s",
			ErrProviderUnavailable, d.descriptor.ID, runErr, d.suffix(stderr))
	}
}

// command builds the child process. This is the only place in the library an
// *exec.Cmd is constructed.
func (d *Driver) command(ctx context.Context, args, env []string, workDir string) *exec.Cmd {
	// The binary is an absolute path, resolved once at New. No PATH lookup is
	// left to win.
	//
	// nosemgrep: go.lang.security.audit.dangerous-exec-command.dangerous-exec-command
	cmd := exec.CommandContext(ctx, d.binary, args...) // #nosec G204 -- d.binary is resolved at construction and args come from the provider's own Command
	cmd.Env = env

	cmd.Dir = workDir
	if cmd.Dir == "" {
		cmd.Dir = d.workDir
	}

	// These CLIs are Node programs that spawn children, and exec.CommandContext
	// signals only the process it started — so a timeout would leave the
	// children running, and in a long-lived caller they accumulate for as long
	// as it is up.
	killWholeGroup(cmd)
	cmd.WaitDelay = killGrace

	return cmd
}

// suffix renders captured stderr for an error message, redacted and bounded.
func (d *Driver) suffix(stderr []byte) string {
	// The driver's own token is passed as known material: a token format no
	// pattern in redact.go anticipates is still removed by exact match.
	s := detail(stderr, d.creds.token)
	if s == "" {
		return ""
	}
	return ": " + s
}
