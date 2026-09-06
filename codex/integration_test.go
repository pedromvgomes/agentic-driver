//go:build integration

// These tests drive the real Codex CLI, which spends a subscription's quota and
// needs a working login. They are excluded from `go test ./...` by the build
// tag, and CI has no stage that sets it — a suite that silently spends money on
// every push is a suite people turn off.
//
// Run them by hand, against a `codex login` that has already happened:
//
//	go test -tags integration ./codex/ -v
//
// They exist because every fixture in testdata was captured from this CLI, and
// a fixture is only worth what the claim that it still matches is worth.
package codex

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	agentic "github.com/pedromvgomes/agentic-driver"
)

// workspace is a git repository, because codex refuses to run anywhere else:
// "Not inside a trusted directory and --skip-git-repo-check was not specified".
// The provider does not pass that flag, so the guard stays where the CLI put
// it, and a caller wanting to run outside a repository has to say so itself.
func workspace(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("cannot make a git repository to run in: %v: %s", err, out)
	}
	return dir
}

func integrationDriver(t *testing.T) *agentic.Driver {
	t.Helper()

	d, err := agentic.New(New(), agentic.WithTimeout(3*time.Minute), agentic.WithWorkDir(workspace(t)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := d.Ready(); err != nil {
		t.Skipf("codex is not runnable here: %v", err)
	}
	return d
}

// The claim every fixture rests on: the real CLI still emits what testdata
// says it does, and the decoder still folds it into a populated Result.
func TestTheRealCLIStillFoldsToAResult(t *testing.T) {
	d := integrationDriver(t)

	result, err := d.Run(t.Context(), agentic.Request{
		Prompt:         "Reply with exactly the word: pong",
		PermissionMode: "read-only",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.Contains(strings.ToLower(result.Text), "pong") {
		t.Errorf("Text = %q, want the answer", result.Text)
	}
	if result.SessionID == "" {
		t.Error("SessionID is empty, so the thread id is no longer on the opening line")
	}
	if result.Usage.InputTokens == 0 || result.Usage.OutputTokens == 0 {
		t.Errorf("Usage = %+v, so the terminal line no longer carries counts", result.Usage)
	}
	if result.Turns != 1 {
		t.Errorf("Turns = %d, want the one turn a plain exec starts", result.Turns)
	}
	if result.IsError {
		t.Errorf("IsError is set on a successful run: %q", result.Text)
	}
}

// A UI drives the same run through Stream and must see the work as it happens,
// then the same Result the fold produced.
func TestTheRealCLIStreamsEventsAndThenTheResult(t *testing.T) {
	d := integrationDriver(t)

	seq, err := d.Stream(t.Context(), agentic.Request{
		Prompt:         "Reply with exactly the word: pong",
		PermissionMode: "read-only",
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var kinds []agentic.EventKind
	var last agentic.Event
	for event, err := range seq {
		if err != nil {
			t.Fatalf("stream: %v", err)
		}
		kinds = append(kinds, event.Kind)
		last = event
	}

	if last.Kind != agentic.EventKindResult {
		t.Fatalf("the stream ended with %q, want the terminal result", last.Kind)
	}
	if last.Result.SessionID == "" {
		t.Error("the terminal event carries no session")
	}
	var sawText bool
	for _, kind := range kinds[:len(kinds)-1] {
		if kind == agentic.EventKindText {
			sawText = true
		}
	}
	if !sawText {
		t.Errorf("kinds = %v, want the answer to arrive as an event before the result", kinds)
	}
}

// The sandbox is the axis that actually constrains a codex run. A denied write
// is a VERDICT OF SUCCESS: the CLI ran, the sandbox did its job, and the agent
// reported it could not act.
func TestTheRealCLIHonoursTheReadOnlySandbox(t *testing.T) {
	d := integrationDriver(t)

	result, err := d.Run(t.Context(), agentic.Request{
		Prompt:         "Create a file called out.txt containing the word hi. Then say DONE.",
		PermissionMode: "read-only",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.IsError {
		t.Errorf("a sandbox refusal was reported as a failed turn: %q", result.Text)
	}
	if strings.Contains(result.Text, "DONE") {
		t.Errorf("the agent reported success writing under a read-only sandbox: %q", result.Text)
	}
}

// A model the account cannot use is a verdict, not an outage: the CLI ran, was
// understood, and said its own turn failed.
func TestTheRealCLIReportsARejectedModelAsAVerdict(t *testing.T) {
	d := integrationDriver(t)

	result, err := d.Run(t.Context(), agentic.Request{
		Prompt:         "hi",
		Model:          "not-a-real-model-xyz",
		PermissionMode: "read-only",
	})
	if err != nil {
		t.Fatalf("a turn the CLI itself failed was reported as an outage: %v", err)
	}

	if !result.IsError {
		t.Error("IsError is not set on a turn that failed")
	}
	if result.Text == "" {
		t.Error("Text is empty, so the caller learns nothing about why the turn failed")
	}
}

// A usage error puts nothing on stdout, so there is no verdict to report and
// the run is an outage carrying the CLI's own explanation.
func TestTheRealCLIRejectsAnUnknownSandboxBeforeSpawning(t *testing.T) {
	d := integrationDriver(t)

	_, err := d.Run(t.Context(), agentic.Request{Prompt: "hi", PermissionMode: "acceptEdits"})
	if err == nil {
		t.Fatal("another CLI's permission mode was accepted")
	}
	if !strings.Contains(err.Error(), "read-only") {
		t.Errorf("error = %q, want it to name what codex accepts", err)
	}
}
