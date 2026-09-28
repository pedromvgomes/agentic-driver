package codex

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	agentic "github.com/pedromvgomes/agentic-driver"
	"github.com/pedromvgomes/agentic-driver/agentictest"
	"github.com/pedromvgomes/agentic-driver/claudecode"
)

// The prompt travels on stdin, so argv is the same whatever the prompt says —
// including a prompt spelled like a flag, or like the `-` that tells codex to
// read stdin.
func TestArgvIsTheSameWhateverThePromptSays(t *testing.T) {
	want, err := onPath(t).StreamCommand(agentic.Request{Prompt: "hi", Model: "gpt-5"})
	if err != nil {
		t.Fatalf("StreamCommand: %v", err)
	}
	if want.Args[0] != "exec" {
		t.Errorf("argv starts %q, want the exec subcommand first", want.Args)
	}

	for _, prompt := range []string{"--not-a-flag", skipGitRepoCheck, "-", "--model=o3"} {
		t.Run(prompt, func(t *testing.T) {
			inv, err := onPath(t).StreamCommand(agentic.Request{Prompt: prompt, Model: "gpt-5"})
			if err != nil {
				t.Fatalf("StreamCommand: %v", err)
			}
			if !slices.Equal(inv.Args, want.Args) {
				t.Errorf("argv = %q, want %q: the prompt must not reach argv", inv.Args, want.Args)
			}
			if string(inv.Stdin) != prompt {
				t.Errorf("stdin = %q, want the prompt %q", inv.Stdin, prompt)
			}
		})
	}
}

func TestCommandRefusesAnEmptyPrompt(t *testing.T) {
	if _, err := onPath(t).StreamCommand(agentic.Request{}); !errors.Is(err, agentic.ErrInvalidRequest) {
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
	for name := range onPath(t).AuthEnv("token") {
		if _, both := claudeAuth[name]; both {
			t.Errorf("both providers carry their credential in %s, so the variable is not dialect after all", name)
		}
	}

	codexDenied := onPath(t).DenyEnv()
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
	var p any = onPath(t)

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
	if _, ok := p.(agentic.Disallower); ok {
		t.Error("codex implements Disallower, but its nearest analogue is a single switch over five tools, not a per-tool control")
	}
}

// The honest outcome: codex constrains a run by sandbox, not by tool. Its
// `tools` table has exactly one field, web_search, and no allowlist of any
// kind — so an allowedTools this accepted could only be discarded, leaving the
// run with more authority than was asked for.
func TestAToolAllowlistIsRefusedRatherThanDropped(t *testing.T) {
	_, err := onPath(t).PermissionArgs("", []string{"Bash(git status)"})

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
	inv, err := onPath(t).StreamCommand(agentic.Request{Prompt: "hi", AllowedTools: []string{"Read"}})

	if err == nil {
		t.Fatalf("a tool grant produced the invocation %q instead of a refusal", inv.Args)
	}
	if !errors.Is(err, agentic.ErrInvalidRequest) {
		t.Errorf("error = %v, want ErrInvalidRequest", err)
	}
}

// StreamCommand is reachable directly on the provider, without going through
// Driver.prepare's gate, so the refusal has to hold here too — not only at the
// layer a caller might skip.
func TestADenyListNeverProducesAnInvocation(t *testing.T) {
	inv, err := onPath(t).StreamCommand(agentic.Request{Prompt: "hi", DisallowedTools: []string{"Agent"}})

	if err == nil {
		t.Fatalf("a deny-list produced the invocation %q instead of a refusal", inv.Args)
	}
	if !errors.Is(err, agentic.ErrInvalidRequest) {
		t.Errorf("error = %v, want ErrInvalidRequest", err)
	}
}

func TestASandboxModeBecomesTheSandboxFlag(t *testing.T) {
	for _, mode := range []string{"read-only", "workspace-write", "danger-full-access"} {
		args, err := onPath(t).PermissionArgs(mode, nil)
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
	_, err := onPath(t).PermissionArgs("acceptEdits", nil)

	if !errors.Is(err, agentic.ErrInvalidRequest) {
		t.Fatalf("error = %v, want ErrInvalidRequest for another CLI's vocabulary", err)
	}
	if !strings.Contains(err.Error(), "read-only") {
		t.Errorf("error = %q, want it to list what codex does accept", err)
	}
}

// An empty mode is the CLI's own default, not a mode to validate.
func TestNoPermissionModeAddsNoFlags(t *testing.T) {
	args, err := onPath(t).PermissionArgs("", nil)
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
	inv, err := onPath(t).StreamCommand(agentic.Request{Prompt: "hi", PermissionMode: "read-only"})
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
	inv, err := onPath(t).StreamCommand(agentic.Request{Prompt: "hi"})
	if err != nil {
		t.Fatalf("StreamCommand: %v", err)
	}

	for _, arg := range inv.Args {
		if strings.Contains(arg, "max_turns") {
			t.Errorf("argv = %q, want no turn bound codex would silently ignore", inv.Args)
		}
	}
}

// skipGitRepoCheck is the flag that lets a scripted run happen wherever it is
// pointed. Named once so a test cannot pass by asserting on its own typo.
const skipGitRepoCheck = "--skip-git-repo-check"

// recordedArgs runs one request through a fake binary and reports the argv the
// child was actually given. Asserting on StreamCommand alone would test what
// the dialect asks for; this tests what the driver spawns.
func recordedArgs(t *testing.T, req agentic.Request) []string {
	t.Helper()
	return ranFake(t, req).Recorded(t).Args
}

// ranFake runs one request through a fake binary and returns the fake, so a
// test can read back both the argv and the stdin the child was given.
func ranFake(t *testing.T, req agentic.Request) *agentictest.Fake {
	t.Helper()

	stream, err := os.ReadFile(filepath.Join("testdata", "success-mini.ndjson"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	fake := (&agentictest.Fake{Stdout: string(stream)}).Build(t)

	d, err := agentic.New(onPath(t), agentic.WithBinary(fake.Path()))
	if err != nil {
		t.Fatalf("agentic.New: %v", err)
	}
	if _, err := d.Run(t.Context(), req); err != nil {
		t.Fatalf("Run with a %d-byte prompt: %v", len(req.Prompt), err)
	}
	return fake
}

// Without the flag codex refuses to start outside a git repository, and a
// scripted run has nobody to warn. It is on every invocation rather than
// conditional on anything a caller sets, so the same Request that succeeds on
// claudecode — which has no equivalent check — does not die at spawn here.
func TestEveryExecInvocationSkipsTheGitRepositoryCheck(t *testing.T) {
	requests := map[string]agentic.Request{
		"a bare prompt":  {Prompt: "hi"},
		"a model":        {Prompt: "hi", Model: "gpt-5.6-sol"},
		"a sandbox mode": {Prompt: "hi", PermissionMode: "read-only"},
		"a schema":       {Prompt: "hi", Schema: json.RawMessage(`{"type":"object"}`)},
	}

	for name, req := range requests {
		t.Run(name, func(t *testing.T) {
			inv, err := onPath(t).StreamCommand(req)
			if err != nil {
				t.Fatalf("StreamCommand: %v", err)
			}
			if !slices.Contains(inv.Args, skipGitRepoCheck) {
				t.Errorf("argv = %q, want %s", inv.Args, skipGitRepoCheck)
			}
		})
	}
}

// The child is what actually runs, so the flag has to survive the driver as
// well as the dialect.
func TestTheSpawnedChildIsGivenTheGitRepositoryCheckFlag(t *testing.T) {
	args := recordedArgs(t, agentic.Request{Prompt: "hi"})

	if !slices.Contains(args, skipGitRepoCheck) {
		t.Errorf("the child was run as %q, want %s", args, skipGitRepoCheck)
	}
}

// A prompt spelled exactly like the flag reaches the child on stdin, and the
// child's argv carries the flag once: the one the dialect put there.
func TestAPromptSpelledLikeTheGitRepositoryCheckFlagStaysOutOfArgv(t *testing.T) {
	fake := ranFake(t, agentic.Request{Prompt: skipGitRepoCheck})

	args := fake.Recorded(t).Args
	var n int
	for _, arg := range args {
		if arg == skipGitRepoCheck {
			n++
		}
	}
	if n != 1 {
		t.Errorf("the child was run as %q, want %s exactly once", args, skipGitRepoCheck)
	}
	if got := fake.Stdin(t); got != skipGitRepoCheck {
		t.Errorf("stdin = %q, want the prompt %q", got, skipGitRepoCheck)
	}
}

// Linux caps a single argv element at 128 KiB, and a prompt embedding a whole
// diff routinely exceeds it. The run goes through a real exec, because an
// Invocation inspected without spawning anything cannot fail at execve(2).
func TestAPromptLargerThanAnArgvElementReachesTheCLIOnStdin(t *testing.T) {
	const marker = "diff --git a/prompt b/prompt\n"
	prompt := strings.Repeat(marker, 200*1024/len(marker)+1)

	fake := ranFake(t, agentic.Request{Prompt: prompt})

	if got := fake.Stdin(t); got != prompt {
		t.Errorf("stdin carried %d bytes, want the %d-byte prompt verbatim", len(got), len(prompt))
	}
	for _, arg := range fake.Recorded(t).Args {
		if strings.Contains(arg, "diff --git") {
			t.Errorf("argv carries the prompt, which fails to exec once it outgrows one argument: %.80q", arg)
		}
	}
}

// codex authenticates from auth.json under CODEX_HOME, rewrites it in place as
// it refreshes, and its refresh tokens are effectively single-use. Two runs
// sharing that file race to refresh it and leave the profile holding whichever
// half-rotated state lost, so the cost of ignoring this is a broken login
// rather than a slow queue.
func TestCodexRunsCannotShareACredential(t *testing.T) {
	var p any = onPath(t)

	limiter, ok := p.(agentic.ConcurrencyLimiter)
	if !ok {
		t.Fatal("codex does not implement ConcurrencyLimiter, so a caller fanning out has no way to learn it must not")
	}
	if got := limiter.MaxConcurrentRuns(); got != 1 {
		t.Errorf("MaxConcurrentRuns() = %d, want 1: auth.json cannot be shared by concurrent runs", got)
	}
}

// The credential vocabulary is dialect, and so is the limit on sharing it: the
// two constructors differ on which binary runs, not on the profile it reads.
func TestBothConstructorsReportTheSameLimit(t *testing.T) {
	if got := vendored(t).MaxConcurrentRuns(); got != onPath(t).MaxConcurrentRuns() {
		t.Errorf("the vendored provider reports %d and the PATH one %d", got, onPath(t).MaxConcurrentRuns())
	}
}

// A driver answers for the capability without a caller asserting on anything.
func TestTheDriverReportsTheCodexLimit(t *testing.T) {
	fake := (&agentictest.Fake{}).Build(t)

	d, err := agentic.New(onPath(t), agentic.WithBinary(fake.Path()))
	if err != nil {
		t.Fatalf("agentic.New: %v", err)
	}
	if got := d.MaxConcurrentRuns(); got != 1 {
		t.Errorf("MaxConcurrentRuns() = %d, want codex's own limit", got)
	}
}
