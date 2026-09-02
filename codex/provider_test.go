package codex

import (
	"errors"
	"slices"
	"testing"

	agentic "github.com/pedromvgomes/agentic-driver"
	"github.com/pedromvgomes/agentic-driver/claudecode"
)

// The prompt is positional and last, so a prompt beginning with a dash cannot
// be read as a flag.
func TestThePromptIsTheLastArgument(t *testing.T) {
	inv, err := New().Command(agentic.Request{Prompt: "--not-a-flag", Model: "gpt-5"})
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
	if _, err := New().Command(agentic.Request{}); !errors.Is(err, agentic.ErrInvalidRequest) {
		t.Errorf("error = %v, want ErrInvalidRequest", err)
	}
}

func TestParseIsHonestAboutBeingUnwritten(t *testing.T) {
	if _, err := New().Parse([]byte(`{}`), nil, 0); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("error = %v, want ErrNotImplemented", err)
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

// Codex vendors no binary and streams nothing. Those absences are what the
// driver reads to answer for the capability without spawning a process.
func TestAbsentCapabilitiesAreAbsentFromTheType(t *testing.T) {
	var p any = New()

	if _, ok := p.(agentic.Installer); ok {
		t.Error("codex implements Installer, but it vendors no signed binary to install")
	}
	if _, ok := p.(agentic.Streamer); ok {
		t.Error("codex implements Streamer, but no event schema has been captured")
	}
	if _, ok := p.(agentic.Resumer); ok {
		t.Error("codex implements Resumer, but no resume flag has been established")
	}
}
