// Package agentic drives headless coding-agent CLIs.
//
// The library owns the PROCESS and a provider owns the DIALECT. Argv assembly,
// timeouts, exit-code interpretation, stderr redaction and environment
// construction are written once, here, and no provider touches them. Flag
// spelling, the result-envelope schema, which variables carry auth and which
// ones can hijack the CLI are declared per provider.
//
// A provider declares what it can do by WHICH INTERFACES IT IMPLEMENTS, and the
// driver discovers that by type assertion — never a boolean field, never a
// switch on the provider ID, and never a method that exists only to return
// "not supported". A capability that is absent is absent from the type, so the
// driver can answer for it before spawning anything.
package agentic

import (
	"context"
	"time"
)

// Provider is the mandatory interface. Every provider implements it.
type Provider interface {
	// Descriptor identifies the provider and names the binary it drives.
	Descriptor() Descriptor

	// Command translates a Request into the provider's own flags. It returns
	// ErrInvalidRequest for a request this CLI cannot express, so the refusal
	// arrives before a process is started rather than as a usage message on
	// stderr.
	Command(Request) (Invocation, error)

	// Parse turns one finished invocation into a Result.
	//
	// It is a pure function of what the process produced, which is what makes
	// it testable against committed output from the real CLI. It is called
	// even when code is non-zero: a CLI reports a rejected credential in its
	// JSON body and may still exit non-zero, so judging the exit code first
	// would turn a clear verdict into a spurious outage.
	Parse(stdout, stderr []byte, code int) (Result, error)
}

// Isolator is optional: the provider can be handed a specific credential and
// run in an environment built from a fixed list.
//
// Only the provider can know this vocabulary. That ANTHROPIC_CUSTOM_HEADERS can
// carry an Authorization header and reroute billing outright is a fact about
// Claude Code; Codex's list is entirely different.
type Isolator interface {
	// DenyEnv names the variables that can redirect THIS CLI away from the
	// credential it was given.
	//
	// The guarantee comes from constructing the environment rather than
	// filtering os.Environ(), so nothing here can arrive by accident. The list
	// is the backstop for the day someone adds a pass-through option or copies
	// a parent environment in for a debugging session.
	DenyEnv() []string

	// AuthEnv renders a credential as the variables that carry it.
	AuthEnv(token string) map[string]string
}

// ModelResolver is optional: the provider understands family aliases, so a
// caller can ask for "opus" without naming a version.
//
// Resolution belongs to the provider because model naming is dialect: which
// families exist, and which build is currently the newest in one, is a fact
// about that CLI's vendor and nothing the library could know.
type ModelResolver interface {
	// ResolveModel turns a family alias into the concrete model it currently
	// means. A name that is already concrete, or that names no family the
	// provider knows, is returned unchanged — the CLI is the authority on what
	// it accepts, and rejecting a name here would break the day the vendor adds
	// one.
	ResolveModel(name string) string
}

// Resumer is optional: the provider can continue a prior session.
type Resumer interface {
	// ResumeArgs returns the arguments that continue sessionID. The provider's
	// own Command uses it; the driver asserts on the interface so a Request
	// carrying a SessionID is refused outright by a provider that would
	// otherwise start a fresh session and answer as though it had read the
	// history.
	ResumeArgs(sessionID string) []string
}

// Streamer is optional: the provider can emit newline-delimited events while it
// works, instead of one envelope at the end.
type Streamer interface {
	// StreamCommand is Command's counterpart for the streaming dialect.
	StreamCommand(Request) (Invocation, error)

	// ParseEvent decodes one line of the stream. A line the provider does not
	// model yields an Event with an empty Kind, which the driver skips: a CLI
	// adding an event type is not a reason to fail a run that is otherwise
	// working.
	ParseEvent(line []byte) (Event, error)
}

// Installer is optional: only for providers that vendor a binary.
//
// Implementing it also settles where the binary comes from. A vendored CLI is
// executed by absolute path at a pinned version, so there is no PATH lookup for
// something else to win and no launcher symlink to repoint — the binary that
// runs is the one that was verified against the publisher's signature.
type Installer interface {
	// BinaryPath is the absolute path of the pinned version. It is answered
	// whether or not that version is installed yet, because Install is how it
	// gets there.
	BinaryPath() string

	// Install fetches a version and verifies it against the publisher's
	// signature. An empty version means the provider's pin.
	Install(ctx context.Context, version string) (InstallResult, error)
	// Installed lists the versions present, newest first.
	Installed(ctx context.Context) ([]string, error)
	// Prune trims old versions, never removing the pin.
	Prune(ctx context.Context, keep int) error
}

// Descriptor identifies a provider.
type Descriptor struct {
	// ID is the stable identifier, e.g. "claude-code".
	ID string
	// DisplayName is for humans.
	DisplayName string
	// Binary is the executable name looked up on PATH when the caller has not
	// pinned an absolute path.
	Binary string
}

// Request is what a caller asks for, in terms every CLI can be asked to
// honour. A field a provider cannot express is an ErrInvalidRequest from
// Command, not a silently dropped flag.
type Request struct {
	// Prompt is the single-shot instruction.
	Prompt string
	// Model names a model, or is empty for the driver's WithModel default and
	// then the CLI's own. A provider implementing ModelResolver also accepts a
	// family alias such as "opus" here.
	Model string
	// MaxTurns bounds the agent loop, or is zero for the CLI's default.
	MaxTurns int
	// SessionID continues a prior session. It requires a Resumer.
	SessionID string
	// WorkDir is the working directory of the child process, or empty for the
	// parent's.
	WorkDir string
	// Timeout bounds this invocation, overriding the driver's default.
	Timeout time.Duration
}

// Invocation is what a provider says to run: the arguments after the binary,
// and the environment the dialect requires.
type Invocation struct {
	// Args is the argv after the executable.
	Args []string
	// Env is the provider's non-secret environment contribution — the
	// variables that make the CLI behave like a program being scripted rather
	// than a TUI being watched. It applies in both credential modes, because
	// it is dialect, not credentials.
	Env map[string]string
}

// Result is deliberately thin: what did it say, and what did it cost. A
// provider whose envelope has no counterpart for a field leaves it zero.
//
// Modelling each CLI's envelope faithfully would put the union of every
// provider's schema in the one type every caller has to read.
type Result struct {
	// Text is the agent's answer.
	Text string
	// SessionID identifies this session, for a later Resumer.
	SessionID string
	// Model is the model that actually answered, as the provider reported it.
	//
	// It is not the name that was asked for: a family alias resolves to a
	// build, and a CLI may substitute one. Since the model is what determines
	// the cost sitting next to it in Usage, a Result that reported only what
	// was requested would be describing a price nobody was charged.
	Model string
	// Usage is what the turn consumed.
	Usage Usage
	// Turns is how many turns the agent took.
	Turns int
	// IsError reports that the CLI itself declared the turn a failure. It is a
	// verdict from the provider, not an error from the library: the Result is
	// still populated, and Text carries the explanation.
	IsError bool
}

// Usage is the cost of a turn. Zero means the provider did not report it.
type Usage struct {
	InputTokens         int
	OutputTokens        int
	CacheReadTokens     int
	CacheCreationTokens int
	// CostUSD is the provider's own figure, not a computed one.
	CostUSD float64
}

// EventKind classifies a streamed event across providers.
type EventKind string

const (
	// EventKindUnknown is a line the provider does not model. The driver skips
	// it rather than failing the run.
	EventKindUnknown EventKind = ""
	// EventKindText is agent output.
	EventKindText EventKind = "text"
	// EventKindToolUse is the agent invoking a tool.
	EventKindToolUse EventKind = "tool_use"
	// EventKindToolResult is a tool answering.
	EventKindToolResult EventKind = "tool_result"
	// EventKindResult is the terminal envelope. Its Result is the same value
	// Parse would have produced for a non-streaming run.
	EventKindResult EventKind = "result"
)

// Event is one item from a streamed run.
type Event struct {
	Kind EventKind
	// Text is the content of a text event, or a tool's name or output.
	Text string
	// Result is set on EventKindResult.
	Result Result
	// Raw is the undecoded line, for a caller that wants the provider's own
	// detail without the library having to model it.
	Raw []byte
}

// InstallResult reports what an Install did.
type InstallResult struct {
	Version string
	Path    string
	// AlreadyPresent means the version was already installed and verified, so
	// nothing was downloaded.
	AlreadyPresent bool
}
