package claudecode

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	agentic "github.com/pedromvgomes/agentic-driver"
)

// flagValue returns the argument following name, and whether name is present.
func flagValue(args []string, name string) (string, bool) {
	i := slices.Index(args, name)
	if i < 0 || i+1 >= len(args) {
		return "", false
	}
	return args[i+1], true
}

func build(t *testing.T, req agentic.Request) agentic.Invocation {
	t.Helper()

	inv, err := testProvider(t).Command(req)
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	return inv
}

// A roster is how a scripted run reaches a subagent at all: the measure that
// makes the run safe to script refuses to load the configuration where agent
// definitions live.
func TestARosterIsDeclaredOnTheCommandLine(t *testing.T) {
	inv := build(t, agentic.Request{
		Prompt: "curate",
		Agents: map[string]agentic.Agent{
			"memory-curator": {Description: "promotes candidates", Prompt: "you are the curator"},
		},
	})

	raw, ok := flagValue(inv.Args, "--agents")
	if !ok {
		t.Fatalf("argv carries no --agents: %q", inv.Args)
	}

	var got map[string]struct {
		Description string `json:"description"`
		Prompt      string `json:"prompt"`
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("--agents is not JSON: %v", err)
	}
	if got["memory-curator"].Description != "promotes candidates" {
		t.Errorf("description = %q", got["memory-curator"].Description)
	}
	if got["memory-curator"].Prompt != "you are the curator" {
		t.Errorf("prompt = %q", got["memory-curator"].Prompt)
	}
}

// A logged or cached invocation is only comparable between runs if the same
// roster renders the same argv, which map iteration order alone does not give.
func TestTheSameRosterRendersTheSameArgv(t *testing.T) {
	req := agentic.Request{
		Prompt: "curate",
		Agents: map[string]agentic.Agent{
			"a": {Description: "d", Prompt: "p"},
			"b": {Description: "d", Prompt: "p"},
			"c": {Description: "d", Prompt: "p"},
		},
	}

	first, _ := flagValue(build(t, req).Args, "--agents")
	for range 8 {
		again, _ := flagValue(build(t, req).Args, "--agents")
		if again != first {
			t.Fatalf("--agents = %q, want the stable %q", again, first)
		}
	}
}

// A blank field does not fail the CLI: it produces an agent the model can see
// and cannot use, which is harder to notice than a refused request.
func TestAnUnusableRosterEntryIsRefusedBeforeAnyProcessStarts(t *testing.T) {
	for name, agents := range map[string]map[string]agentic.Agent{
		"no name":        {"": {Description: "d", Prompt: "p"}},
		"no description": {"curator": {Prompt: "p"}},
		"no prompt":      {"curator": {Description: "d"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := testProvider(t).Command(agentic.Request{Prompt: "hi", Agents: agents})
			if !errors.Is(err, agentic.ErrInvalidRequest) {
				t.Fatalf("error = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

// Both flags are argv rather than settings-file keys, which is the whole reason
// a run started with --setting-sources ” can still be given a narrow grant.
func TestAScriptedGrantSurvivesRefusingToLoadSettings(t *testing.T) {
	inv := build(t, agentic.Request{
		Prompt:         "curate",
		PermissionMode: "acceptEdits",
		AllowedTools:   []string{"Read", "Write"},
	})

	if sources, ok := flagValue(inv.Args, "--setting-sources"); !ok || sources != "" {
		t.Fatalf("argv = %q, want it to still refuse settings sources", inv.Args)
	}
	if mode, _ := flagValue(inv.Args, "--permission-mode"); mode != "acceptEdits" {
		t.Errorf("--permission-mode = %q", mode)
	}
	if tools, _ := flagValue(inv.Args, "--allowedTools"); tools != "Read,Write" {
		t.Errorf("--allowedTools = %q", tools)
	}
}

// A tool pattern contains spaces, so a space-separated spelling would split one
// pattern across argv entries and grant something nobody asked for.
func TestAToolPatternContainingSpacesStaysOneArgument(t *testing.T) {
	inv := build(t, agentic.Request{
		Prompt:       "curate",
		AllowedTools: []string{"Bash(agtk memory anchor*)", "Write"},
	})

	tools, ok := flagValue(inv.Args, "--allowedTools")
	if !ok {
		t.Fatalf("argv carries no --allowedTools: %q", inv.Args)
	}
	if !strings.Contains(tools, "Bash(agtk memory anchor*)") {
		t.Errorf("--allowedTools = %q, want the pattern intact", tools)
	}
	if slices.Contains(inv.Args, "memory") || slices.Contains(inv.Args, "anchor*)") {
		t.Errorf("argv = %q, want the pattern unsplit", inv.Args)
	}
}

// An empty entry reaches the CLI as a tool named "", which matches nothing and
// silently narrows the grant the caller believed they were giving.
func TestABlankAllowedToolIsRefused(t *testing.T) {
	_, err := testProvider(t).Command(agentic.Request{
		Prompt:       "hi",
		AllowedTools: []string{"Read", "  "},
	})
	if !errors.Is(err, agentic.ErrInvalidRequest) {
		t.Fatalf("error = %v, want ErrInvalidRequest", err)
	}
}

// Which modes exist is the CLI's to say, so a name this package does not know
// is passed through rather than rejected — the same reason ResolveModel does
// not police model names.
func TestAnUnknownPermissionModeIsPassedThrough(t *testing.T) {
	inv := build(t, agentic.Request{Prompt: "hi", PermissionMode: "somethingNew"})

	if mode, _ := flagValue(inv.Args, "--permission-mode"); mode != "somethingNew" {
		t.Errorf("--permission-mode = %q, want it passed through", mode)
	}
}

// A field left zero is a flag left off, so the CLI's own default applies.
func TestNeitherFlagAppearsWhenNothingIsAsked(t *testing.T) {
	inv := build(t, agentic.Request{Prompt: "hi"})

	for _, flag := range []string{"--agents", "--allowedTools", "--permission-mode"} {
		if slices.Contains(inv.Args, flag) {
			t.Errorf("argv = %q, want no %s", inv.Args, flag)
		}
	}
}

// The two dialects must not drift on what a request means; a grant that applied
// only to the non-streaming spelling would be the kind of difference nobody
// looks for.
func TestTheStreamingSpellingCarriesTheSameGrant(t *testing.T) {
	req := agentic.Request{
		Prompt:         "curate",
		PermissionMode: "acceptEdits",
		AllowedTools:   []string{"Read"},
		Agents:         map[string]agentic.Agent{"curator": {Description: "d", Prompt: "p"}},
	}

	inv, err := testProvider(t).StreamCommand(req)
	if err != nil {
		t.Fatalf("StreamCommand: %v", err)
	}
	for _, flag := range []string{"--agents", "--allowedTools", "--permission-mode"} {
		if !slices.Contains(inv.Args, flag) {
			t.Errorf("streaming argv = %q, want %s", inv.Args, flag)
		}
	}
}

// The measure is first in argv so no caller can forget it and no later
// positional can shadow it; a new flag inserted ahead of it would undo that
// without any test noticing.
func TestTheSettingsRefusalStaysFirstInArgv(t *testing.T) {
	inv := build(t, agentic.Request{
		Prompt:       "curate",
		AllowedTools: []string{"Read"},
		Agents:       map[string]agentic.Agent{"curator": {Description: "d", Prompt: "p"}},
	})

	if len(inv.Args) < 2 || inv.Args[0] != "--setting-sources" || inv.Args[1] != "" {
		t.Fatalf("argv = %q, want it to open with --setting-sources ''", inv.Args)
	}
}
