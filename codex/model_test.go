package codex

import (
	"slices"
	"testing"

	agentic "github.com/pedromvgomes/agentic-driver"
	"github.com/pedromvgomes/agentic-driver/claudecode"
)

// A bare family name has to reach the CLI as a concrete build. The CLI would
// resolve one itself, but then the same request means a different model after
// an upgrade, and nothing in the stream records which one answered.
func TestAFamilyAliasResolvesToAConcreteModel(t *testing.T) {
	p := onPath(t)

	for alias, want := range map[string]string{
		"astra": "gpt-6-astra",
		"sol":   "gpt-5.6-sol",
		"terra": "gpt-5.6-terra",
		"luna":  "gpt-5.6-luna",
		"mini":  "gpt-5.4-mini",
	} {
		if got := p.ResolveModel(alias); got != want {
			t.Errorf("ResolveModel(%q) = %q, want %q", alias, got, want)
		}
	}
}

// A name this table has not heard of belongs to the CLI to accept or reject.
// Refusing it here would put every model OpenAI ships next behind a release of
// this package.
func TestAnUnknownModelIsPassedThroughUntouched(t *testing.T) {
	p := onPath(t)

	for _, name := range []string{"gpt-6-astra", "gpt-5.5", "a-family-that-does-not-exist-yet", ""} {
		if got := p.ResolveModel(name); got != name {
			t.Errorf("ResolveModel(%q) = %q, want it unchanged", name, got)
		}
	}
}

func TestModelsIsACopy(t *testing.T) {
	Models()["astra"] = "tampered"

	if got := onPath(t).ResolveModel("astra"); got == "tampered" {
		t.Error("a caller mutating the returned map changed what the provider resolves")
	}
}

// The aliases are OpenAI's own names. A vocabulary shared with the other
// dialect would have this library assert that some OpenAI model is the
// counterpart of some Anthropic one, and a caller swapping providers in a
// manifest would silently get a model nobody chose.
func TestTheAliasVocabularyIsTheVendorsOwn(t *testing.T) {
	for alias := range Models() {
		if _, shared := claudecode.Models()[alias]; shared {
			t.Errorf("both dialects understand %q, so the alias names no vendor's family in particular", alias)
		}
	}
}

// The alias is settled before the provider is asked for an argv, so what the
// child actually runs is the concrete build.
func TestAnAliasReachesTheChildAsAConcreteModel(t *testing.T) {
	args := recordedArgs(t, agentic.Request{Prompt: "hi", Model: "sol"})

	i := slices.Index(args, "--model")
	if i < 0 || i+1 >= len(args) {
		t.Fatalf("the child was run as %q, want a --model flag", args)
	}
	if args[i+1] != "gpt-5.6-sol" {
		t.Errorf("--model = %q, want the alias resolved to a concrete build", args[i+1])
	}
}

// The resolution is dialect, so it is the same object whichever binary runs.
func TestBothConstructorsResolveTheSameAliases(t *testing.T) {
	if got, want := vendored(t).ResolveModel("astra"), onPath(t).ResolveModel("astra"); got != want {
		t.Errorf("the vendored provider resolves astra to %q and the PATH one to %q", got, want)
	}
}
