package claudecode

import (
	"strings"
	"testing"
)

// A bare family name has to reach the CLI as a concrete build. The CLI would
// accept "opus" and resolve it itself, but then the same request means a
// different model after an upgrade, and the envelope carries no record of which
// one answered.
func TestAFamilyAliasResolvesToAConcreteModel(t *testing.T) {
	p := testProvider(t)

	for alias, want := range map[string]string{
		"opus":   "claude-opus-5",
		"sonnet": "claude-sonnet-5",
		"haiku":  "claude-haiku-4-5",
		"fable":  "claude-fable-5-1",
	} {
		if got := p.ResolveModel(alias); got != want {
			t.Errorf("ResolveModel(%q) = %q, want %q", alias, got, want)
		}
	}
}

// A name this table has not heard of belongs to the CLI to accept or reject.
// Refusing it here would put every new family behind a release of this package.
func TestAnUnknownModelIsPassedThroughUntouched(t *testing.T) {
	p := testProvider(t)

	for _, name := range []string{"claude-opus-5", "a-family-that-does-not-exist-yet", ""} {
		if got := p.ResolveModel(name); got != name {
			t.Errorf("ResolveModel(%q) = %q, want it unchanged", name, got)
		}
	}
}

func TestModelsIsACopy(t *testing.T) {
	Models()["opus"] = "tampered"

	p := testProvider(t)
	if got := p.ResolveModel("opus"); got == "tampered" {
		t.Error("a caller mutating the returned map changed what the provider resolves")
	}
}

// The envelope has no field naming the model that answered: modelUsage is
// keyed by every model the run touched, and a turn on Opus still bills a little
// Haiku for the CLI's own housekeeping. Picking the wrong entry would report a
// cost against a model that never saw the conversation.
func TestTheAnsweringModelIsTheOneThatReadTheConversation(t *testing.T) {
	p := testProvider(t)

	for name, want := range map[string]string{
		"success.json":       "claude-opus-5",
		"success-haiku.json": "claude-haiku-4-5",
	} {
		got, err := p.Parse(golden(t, name), nil, 0)
		if err != nil {
			t.Fatalf("Parse(%s): %v", name, err)
		}
		if got.Model != want {
			t.Errorf("Parse(%s).Model = %q, want %q", name, got.Model, want)
		}
	}
}

// The key carries a context-window suffix that is a billing detail rather than
// a model, so the canonical name is what a caller sees.
func TestTheReportedModelDropsTheContextWindowSuffix(t *testing.T) {
	p := testProvider(t)

	got, err := p.Parse(golden(t, "success.json"), nil, 0)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if strings.Contains(got.Model, "[") {
		t.Errorf("Model = %q, want the canonical name without a context-window suffix", got.Model)
	}
}

func TestAnEnvelopeWithNoModelUsageReportsNoModel(t *testing.T) {
	p := testProvider(t)

	got, err := p.Parse(golden(t, "rejected-auth.json"), nil, 1)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Model != "" {
		t.Errorf("Model = %q, want empty for a turn no model ever answered", got.Model)
	}
}

// Go randomises map iteration, so a tie has to be broken by something in the
// data rather than by the order it happens to be visited in.
func TestTheAnsweringModelIsStableAcrossRuns(t *testing.T) {
	p := testProvider(t)

	// Two entries with identical totals whose key ordering is the opposite of
	// their canonical-name ordering: whichever the comparison uses, the two
	// disagree, so an unstable answer shows up.
	const tied = `{"type":"result","session_id":"s","result":"x","modelUsage":{
		"aaa-model":{"canonicalModel":"zzz","inputTokens":10},
		"bbb-model":{"canonicalModel":"yyy","inputTokens":10}}}`

	first, err := p.Parse([]byte(tied), nil, 0)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for i := 0; i < 200; i++ {
		got, err := p.Parse([]byte(tied), nil, 0)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if got.Model != first.Model {
			t.Fatalf("Model varies between runs: %q then %q", first.Model, got.Model)
		}
	}
}

// An envelope that reports usage without input tokens still names the model
// that produced it.
func TestASingleModelIsNamedEvenWithNoInputTokens(t *testing.T) {
	p := testProvider(t)

	const outputOnly = `{"type":"result","session_id":"s","result":"x","modelUsage":{
		"solo-model":{"canonicalModel":"solo","outputTokens":4}}}`

	got, err := p.Parse([]byte(outputOnly), nil, 0)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Model != "solo" {
		t.Errorf("Model = %q, want the only model in the envelope", got.Model)
	}
}
