package codex

import (
	"errors"
	"slices"
	"strings"
	"testing"

	agentic "github.com/pedromvgomes/agentic-driver"
	"github.com/pedromvgomes/agentic-driver/claudecode"
)

// The prompt is positional and last, so a prompt beginning with a dash cannot
// be read as a flag.
func TestThePromptIsTheLastArgument(t *testing.T) {
	inv, err := New().StreamCommand(agentic.Request{Prompt: "--not-a-flag", Model: "gpt-5"})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}

	if got := inv.Args[len(inv.Args)-1]; got != "--not-a-flag" {
		t.Errorf("last argument = %q, want the prompt", got)
	}
	if inv.Args[0] != "exec" {
		t.Errorf("argv starts %q, want the exec subcommand first", inv.Args)
	}
}

func TestCommandRefusesAnEmptyPrompt(t *testing.T) {
	if _, err := New().StreamCommand(agentic.Request{}); !errors.Is(err, agentic.ErrInvalidRequest) {
		t.Errorf("error = %v, want ErrInvalidRequest", err)
	}
}

// The whole reason a second provider exists this early: a vocabulary shared
// between two CLIs would have to be the union of both, and every entry in it
// would be wrong for one of them.
func TestTheTwoProvidersShareNoCredentialVocabulary(t *testing.T) {
	claude, err := claudecode.New(t.TempDir())
	if err != nil {
		t.Fatalf("claudecode.New: %v", err)
	}

	claudeAuth := claude.AuthEnv("token")
	for name := range New().AuthEnv("token") {
		if _, both := claudeAuth[name]; both {
			t.Errorf("both providers carry their credential in %s, so the variable is not dialect after all", name)
		}
	}

	codexDenied := New().DenyEnv()
	var shared int
	for _, name := range claude.DenyEnv() {
		if slices.Contains(codexDenied, name) {
			shared++
		}
	}
	if shared == len(codexDenied) {
		t.Error("codex denies nothing claudecode does not; the lists are not actually per-provider")
	}
}

// Codex vendors no binary, declares no roster and cannot bound a loop. Those
// absences are what the driver reads to answer for the capability without
// spawning a process.
func TestAbsentCapabilitiesAreAbsentFromTheType(t *testing.T) {
	var p any = New()

	if _, ok := p.(agentic.Installer); ok {
		t.Error("codex implements Installer, but it vendors no signed binary to install")
	}
	if _, ok := p.(agentic.Resumer); ok {
		t.Error("codex implements Resumer, but no resume flag has been established")
	}
	if _, ok := p.(agentic.AgentDefiner); ok {
		t.Error("codex implements AgentDefiner, but it has no roster dialect")
	}
	if _, ok := p.(agentic.TurnLimiter); ok {
		t.Error("codex implements TurnLimiter, but it has no configuration field for a turn bound")
	}
}

// The honest outcome: codex constrains a run by sandbox, not by tool. Its
// `tools` table has exactly one field, web_search, and no allowlist of any
// kind — so an allowedTools this accepted could only be discarded, leaving the
// run with more authority than was asked for.
func TestAToolAllowlistIsRefusedRatherThanDropped(t *testing.T) {
	_, err := New().PermissionArgs("", []string{"Bash(git status)"})

	if !errors.Is(err, agentic.ErrInvalidRequest) {
		t.Fatalf("error = %v, want ErrInvalidRequest", err)
	}
	if !strings.Contains(err.Error(), "Bash(git status)") {
		t.Errorf("error = %q, want it to name the grant that cannot be expressed", err)
	}
}

// The refusal has to survive being reached through Command, which is where a
// driver actually meets it. A request carrying tools must never produce an argv.
func TestAToolAllowlistNeverProducesAnInvocation(t *testing.T) {
	inv, err := New().StreamCommand(agentic.Request{Prompt: "hi", AllowedTools: []string{"Read"}})

	if err == nil {
		t.Fatalf("a tool grant produced the invocation %q instead of a refusal", inv.Args)
	}
	if !errors.Is(err, agentic.ErrInvalidRequest) {
		t.Errorf("error = %v, want ErrInvalidRequest", err)
	}
}

func TestASandboxModeBecomesTheSandboxFlag(t *testing.T) {
	for _, mode := range []string{"read-only", "workspace-write", "danger-full-access"} {
		args, err := New().PermissionArgs(mode, nil)
		if err != nil {
			t.Fatalf("PermissionArgs(%q): %v", mode, err)
		}
		if !slices.Equal(args, []string{"-s", mode}) {
			t.Errorf("PermissionArgs(%q) = %q, want the sandbox flag", mode, args)
		}
	}
}

// A mode the CLI does not know exits non-zero with nothing on stdout, which
// reaches a caller as an outage — a typo wearing the costume of a failing
// provider, and one that invites a retry loop. Refusing here names the problem.
func TestAnUnknownSandboxModeIsRefusedBeforeSpawning(t *testing.T) {
	_, err := New().PermissionArgs("acceptEdits", nil)

	if !errors.Is(err, agentic.ErrInvalidRequest) {
		t.Fatalf("error = %v, want ErrInvalidRequest for another CLI's vocabulary", err)
	}
	if !strings.Contains(err.Error(), "read-only") {
		t.Errorf("error = %q, want it to list what codex does accept", err)
	}
}

// An empty mode is the CLI's own default, not a mode to validate.
func TestNoPermissionModeAddsNoFlags(t *testing.T) {
	args, err := New().PermissionArgs("", nil)
	if err != nil {
		t.Fatalf("PermissionArgs: %v", err)
	}
	if len(args) != 0 {
		t.Errorf("PermissionArgs = %q, want nothing added", args)
	}
}

// approval_policy is deliberately absent: `codex exec` has nobody to prompt and
// never blocks for approval, so setting one would be a knob with no meaning in
// this mode.
func TestTheInvocationDoesNotSetAnApprovalPolicy(t *testing.T) {
	inv, err := New().StreamCommand(agentic.Request{Prompt: "hi", PermissionMode: "read-only"})
	if err != nil {
		t.Fatalf("StreamCommand: %v", err)
	}

	for _, arg := range inv.Args {
		if strings.Contains(arg, "approval_policy") {
			t.Errorf("argv = %q, want no approval policy", inv.Args)
		}
	}
}

// The turn bound codex accepts and ignores. Emitting it would leave the loop
// running unbounded while the caller believed it had capped it.
func TestNoInvocationCarriesAFabricatedTurnBound(t *testing.T) {
	inv, err := New().StreamCommand(agentic.Request{Prompt: "hi"})
	if err != nil {
		t.Fatalf("StreamCommand: %v", err)
	}

	for _, arg := range inv.Args {
		if strings.Contains(arg, "max_turns") {
			t.Errorf("argv = %q, want no turn bound codex would silently ignore", inv.Args)
		}
	}
}
