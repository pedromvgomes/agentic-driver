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
	"encoding/json"
	"time"
)

// Provider is the mandatory interface. Every provider implements it.
type Provider interface {
	// Descriptor identifies the provider and names the binary it drives.
	Descriptor() Descriptor

	// StreamCommand translates a Request into the provider's own flags. It
	// returns ErrInvalidRequest for a request this CLI cannot express, so the
	// refusal arrives before a process is started rather than as a usage
	// message on stderr.
	//
	// It is the only argv builder. A provider that spelled one invocation for
	// a streamed run and another for a batched one would have two dialects to
	// keep in step, and the day they drift is the day Run and Stream answer
	// the same Request differently.
	StreamCommand(Request) (Invocation, error)

	// NewDecoder returns a decoder for exactly one run, and is handed the
	// request that run is answering.
	//
	// The request is there because some outcomes are only legible against what
	// was asked for. A run given a Schema that produces no structured payload
	// has not answered, however cheerfully the CLI reports it — and the decoder
	// is the only thing positioned to read the provider's own signal for that,
	// rather than guessing from whether the text happens to parse.
	NewDecoder(Request) Decoder
}

// Decoder consumes the lines of one run, in order, and folds them into a
// Result.
//
// It is stateful, and per-run, because a Result is a fold rather than a
// projection of any single line. Codex spreads one across a whole stream — the
// session id arrives on the first line, the answer on the last agent message,
// the token usage on the terminal line — so a stateless function of the final
// line could not build one at all.
//
// Being an ordinary object fed lines from a slice is what keeps it testable
// against committed output from the real CLI, with no process involved.
type Decoder interface {
	// Decode consumes one line and reports what it means.
	//
	// A line the provider does not model yields the zero Event, which the
	// driver skips: a CLI adding an event type is not a reason to fail a run
	// that is otherwise working. An error means the line could not be
	// understood at all, which ends the run.
	//
	// It never returns EventKindResult. The terminal event is built by the
	// driver from Result, so there is exactly one thing in the library that
	// decides what a run finally said.
	Decode(line []byte) (Event, error)

	// Result is the fold over every line decoded so far.
	//
	// ok reports whether the run reached a terminal outcome. False means the
	// stream stopped before the CLI said how it ended, which is an outage: no
	// statement was made about the request, so there is no verdict to report.
	// A CLI that ended a turn badly returns true with IsError set — that is a
	// verdict, and the difference between the two is the whole point of the
	// second return value.
	Result() (result Result, ok bool)
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

// AgentDefiner is optional: the provider accepts a roster of subagents on the
// command line, so a scripted run can delegate to one without the CLI reading
// any configuration off disk.
//
// It is a capability and not a Request field a provider may ignore, for the
// same reason resuming is: the failure of dropping it is silent. A run whose
// roster went missing does not fail — it answers the prompt itself, competently
// and with the wrong context, and nothing in the reply says a delegation never
// happened.
type AgentDefiner interface {
	// AgentArgs returns the arguments that declare the roster. It returns
	// ErrInvalidRequest for a roster this CLI cannot express, so the refusal
	// arrives before a process is started.
	AgentArgs(agents map[string]Agent) ([]string, error)
}

// Permitter is optional: the provider can be told what a scripted run may do
// without reading a settings file.
//
// Separate from AgentDefiner because the two are separately absent: a CLI may
// take a roster and have no vocabulary for tool permissions, or the reverse.
type Permitter interface {
	// PermissionArgs returns the arguments that apply mode and allowedTools.
	// Either may be zero, meaning the CLI's own default.
	PermissionArgs(mode string, allowedTools []string) ([]string, error)
}

// Agent is one entry in a Request's roster.
//
// Deliberately two fields. Both are documented by the CLIs this drives; a
// richer definition — a tool grant, a model, a colour — would be written from
// memory, and a dialect written from memory produces a provider that passes its
// own tests and nothing else. A run-wide tool restriction is Request's
// AllowedTools, which is real.
type Agent struct {
	// Description tells the delegating agent when to use this one.
	Description string
	// Prompt is the subagent's instructions.
	Prompt string
}

// TurnLimiter is optional: the provider can bound the agent loop.
//
// A capability rather than a Request field every provider honours, because the
// two CLIs do not merely spell it differently — one has no concept at that
// granularity. Claude Code counts a turn as one iteration of the agent loop and
// bounds it with a flag. Codex counts a turn as the WHOLE loop, and has no
// configuration field for a limit at all; the nearest-looking spelling is
// accepted and silently ignored, which is the failure this interface exists to
// make impossible.
//
// A caller wanting one bound that behaves identically everywhere uses
// Request.Timeout, which the driver enforces itself and which asks nothing of
// the CLI.
type TurnLimiter interface {
	// TurnLimitArgs returns the arguments that bound the loop to maxTurns. It
	// returns ErrInvalidRequest for a bound this CLI cannot express.
	TurnLimitArgs(maxTurns int) ([]string, error)
}

// SchemaConstrainer is optional: the provider can bind the run's final answer
// to a JSON Schema.
//
// A capability rather than a Request field every provider honours, for the
// reason every gate here exists: a dropped schema fails silently and in the
// direction that looks like success. The run answers the prompt in prose,
// competently, and nothing in the reply says the shape was never applied — so
// the caller discovers it by feeding that prose to a JSON decoder somewhere
// else entirely.
type SchemaConstrainer interface {
	// SchemaArgs returns the arguments that bind the answer to schema.
	//
	// The schema is already valid JSON: the driver checks that before any
	// provider sees it. What remains is dialect, and it differs — one CLI takes
	// the document inline, another takes a path to it — so a provider that
	// needs the schema on disk puts it there itself.
	//
	// It returns ErrInvalidRequest for a schema this CLI cannot express, and
	// ErrProviderUnavailable when the arguments could not be built at all: a
	// schema file that cannot be written is not a statement about the request.
	SchemaArgs(schema json.RawMessage) ([]string, error)
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
	// MaxTurns bounds the agent loop, or is zero for the CLI's default. It
	// requires a TurnLimiter, and it is counted in that provider's own unit —
	// see Result.Turns. Request.Timeout is the bound that means the same thing
	// everywhere.
	MaxTurns int
	// SessionID continues a prior session. It requires a Resumer.
	SessionID string
	// Agents is the roster of subagents the run may delegate to, keyed by the
	// name it delegates by. It requires an AgentDefiner.
	//
	// A scripted run cannot rely on the agent definitions a repo has on disk:
	// the same measure that makes a run safe to script — refusing to load
	// settings files, where a key like apiKeyHelper outranks an injected
	// credential — is what puts those definitions out of reach. Naming the
	// roster in the request is how a caller supplies one anyway.
	Agents map[string]Agent
	// AllowedTools restricts the run to these tools, spelled in the provider's
	// own tool vocabulary. Empty leaves the CLI's own default in force, which
	// is broader, not narrower. It requires a Permitter.
	AllowedTools []string
	// PermissionMode decides how the run answers permission prompts, spelled in
	// the provider's own vocabulary. Empty leaves the CLI's default. It
	// requires a Permitter.
	//
	// A mode that waives prompting outranks AllowedTools rather than combining
	// with it, so the two together leave the allowlist as documentation. The
	// provider does not reconcile them: which modes exist, and what each one
	// overrides, is dialect.
	PermissionMode string
	// WorkDir is the working directory of the child process, or empty for the
	// parent's.
	WorkDir string
	// Timeout bounds this invocation, overriding the driver's default.
	Timeout time.Duration
	// Schema binds the run's final answer to a JSON Schema, or is empty for an
	// unconstrained answer. It requires a SchemaConstrainer.
	//
	// A run carrying one answers in Result.Structured. A run that was given a
	// schema and produced no payload comes back with IsError set, because a
	// caller that asked for JSON and received prose has not been answered —
	// even where the CLI itself called the run a success.
	Schema json.RawMessage
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
	// Turns is how many turns the agent took, counted in the provider's own
	// unit.
	//
	// The unit is NOT comparable across providers, and nothing in the library
	// derives behaviour from it. Claude Code counts one turn per iteration of
	// the agent loop, so a run that called three tools reports several. Codex
	// counts one turn for the entire loop, so a single exec reports one
	// however much work it did. Reading the two as the same quantity makes an
	// exhaustive codex run look cheaper than a trivial Claude Code one.
	Turns int
	// Structured is the schema-conforming answer to a run that carried a
	// Request.Schema, exactly as the provider reported it. It is nil for a run
	// that asked for no schema, and nil alongside IsError for one that asked
	// and did not get it.
	//
	// Separate from Text because the two are not the same statement. Text is
	// what the run said; Structured is what it said in the shape it was
	// required to say it in, and the run where those diverge — an agent
	// explaining in prose that it could not satisfy the schema — is the one
	// worth being able to tell apart.
	Structured json.RawMessage
	// IsError reports that the turn failed. Usually that is the CLI's own
	// declaration, and the Result is still populated with Text carrying the
	// explanation. It is also set for a run that was given a Schema and
	// produced no Structured payload, which is the one verdict the library
	// reaches on its own.
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
	// EventKindResult is the terminal event, built by the driver from the
	// decoder's fold. Its Result is the same value Run returns, because Run is
	// this event and nothing else.
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
