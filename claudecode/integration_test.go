//go:build integration

// These tests drive the real Claude Code CLI, which costs money and needs a
// working credential. They are excluded from `go test ./...` by the build tag,
// and CI has no stage that sets it — a suite that silently spends money on
// every push is a suite people turn off.
//
// Run them by hand:
//
//	go test -tags integration ./claudecode/ -v
//
// AGENTIC_CLAUDE_BINARY names the binary; without it the pinned vendored path
// is used, and `Install` is how that gets there.
package claudecode

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	agentic "github.com/pedromvgomes/agentic-driver"
	"strings"
)

func integrationDriver(t *testing.T) *agentic.Driver {
	t.Helper()

	p, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	opts := []agentic.Option{agentic.WithTimeout(2 * time.Minute)}
	if binary := os.Getenv("AGENTIC_CLAUDE_BINARY"); binary != "" {
		opts = append(opts, agentic.WithBinary(binary))
	}

	d, err := agentic.New(p, opts...)
	if err != nil {
		t.Fatalf("agentic.New: %v", err)
	}
	return d
}

// The cheapest thing that still proves the whole path works: one turn, a
// handful of tokens, no tools.
func TestRunAgainstTheRealCLI(t *testing.T) {
	d := integrationDriver(t)

	got, err := d.Run(t.Context(), agentic.Request{
		Prompt:   "Reply with the single word: ok",
		Model:    "haiku",
		MaxTurns: 1,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.IsError {
		t.Fatalf("the CLI reported an error: %s", got.Text)
	}
	if got.Text == "" {
		t.Error("Text is empty")
	}
	if got.SessionID == "" {
		t.Error("SessionID is empty, so the turn cannot be resumed")
	}
	if got.Usage.OutputTokens == 0 {
		t.Error("Usage reports no output tokens for a turn that produced text")
	}
}

// The committed golden envelope is only worth what its resemblance to today's
// output is worth. This is what catches the schema moving.
func TestTheRealEnvelopeStillMatchesTheGoldenOne(t *testing.T) {
	d := integrationDriver(t)

	got, err := d.Run(t.Context(), agentic.Request{Prompt: "Reply with the single word: ok", Model: "haiku", MaxTurns: 1})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	p := testProvider(t)
	want, err := p.Parse(golden(t, "success.json"), nil, 0)
	if err != nil {
		t.Fatalf("Parse the golden envelope: %v", err)
	}

	// The values differ every run; what must not differ is which fields the
	// envelope populates at all.
	if (got.Text == "") != (want.Text == "") ||
		(got.SessionID == "") != (want.SessionID == "") ||
		(got.Turns == 0) != (want.Turns == 0) ||
		(got.Usage.OutputTokens == 0) != (want.Usage.OutputTokens == 0) {
		t.Errorf("the live envelope populates different fields from the golden one:\nlive   = %+v\ngolden = %+v", got, want)
	}
}

func TestStreamAgainstTheRealCLI(t *testing.T) {
	d := integrationDriver(t)

	seq, err := d.Stream(t.Context(), agentic.Request{
		Prompt:   "Reply with the single word: ok",
		Model:    "haiku",
		MaxTurns: 1,
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var sawText, sawResult bool
	for event, err := range seq {
		if err != nil {
			t.Fatalf("stream: %v", err)
		}
		switch event.Kind {
		case agentic.EventKindText:
			sawText = true
		case agentic.EventKindResult:
			sawResult = true
		}
	}
	if !sawText {
		t.Error("the stream produced no text event")
	}
	if !sawResult {
		t.Error("the stream did not end with a result envelope")
	}
}

// A resumed session has to see the first turn, which is the only thing that
// distinguishes it from a fresh one that answers just as fluently.
func TestResumeAgainstTheRealCLI(t *testing.T) {
	d := integrationDriver(t)

	first, err := d.Run(t.Context(), agentic.Request{
		Prompt:   "Remember the word: pomegranate. Reply with just: stored",
		Model:    "haiku",
		MaxTurns: 1,
	})
	if err != nil {
		t.Fatalf("first turn: %v", err)
	}
	if first.SessionID == "" {
		t.Fatal("the first turn returned no session to resume")
	}

	second, err := d.Run(t.Context(), agentic.Request{
		Prompt:    "What word did I ask you to remember? Reply with just that word.",
		SessionID: first.SessionID,
		Model:     "haiku",
		MaxTurns:  1,
	})
	if err != nil {
		t.Fatalf("resumed turn: %v", err)
	}
	if !strings.Contains(second.Text, "pomegranate") {
		t.Errorf("the resumed turn answered %q, so it did not see the first turn", second.Text)
	}
}

// Setting a model is only half of it. The other half is asking what actually
// answered — a flag the CLI silently ignored, or an alias that resolved to
// something else, produces a perfectly good reply from the wrong model, and the
// cost sitting beside it is the wrong model's cost.
func TestSettingTheModelTakesEffectAgainstTheRealCLI(t *testing.T) {
	for alias, want := range map[string]string{
		"haiku":  "claude-haiku-4-5",
		"sonnet": "claude-sonnet-5",
	} {
		t.Run(alias, func(t *testing.T) {
			p, err := New(t.TempDir())
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			opts := []agentic.Option{agentic.WithTimeout(2 * time.Minute), agentic.WithModel(alias)}
			if binary := os.Getenv("AGENTIC_CLAUDE_BINARY"); binary != "" {
				opts = append(opts, agentic.WithBinary(binary))
			}
			d, err := agentic.New(p, opts...)
			if err != nil {
				t.Fatalf("agentic.New: %v", err)
			}

			// The getter answers before anything has run: it is a property of
			// how the driver is configured, not of a reply.
			if got := d.Model(); got != want {
				t.Errorf("Model() = %q, want the alias %q resolved to %q", got, alias, want)
			}

			result, err := d.Run(t.Context(), agentic.Request{Prompt: "Reply with the single word: ok", MaxTurns: 1})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if result.IsError {
				t.Fatalf("the CLI reported an error: %s", result.Text)
			}
			if result.Model != want {
				t.Errorf("the turn was answered by %q, want %q — the model was set but did not take effect", result.Model, want)
			}
		})
	}
}

// Request.Model has to beat the driver's own setting, and the proof is which
// model answered rather than which flag was assembled.
func TestARequestModelOverridesTheDriverAgainstTheRealCLI(t *testing.T) {
	p, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	opts := []agentic.Option{agentic.WithTimeout(2 * time.Minute), agentic.WithModel("sonnet")}
	if binary := os.Getenv("AGENTIC_CLAUDE_BINARY"); binary != "" {
		opts = append(opts, agentic.WithBinary(binary))
	}
	d, err := agentic.New(p, opts...)
	if err != nil {
		t.Fatalf("agentic.New: %v", err)
	}

	result, err := d.Run(t.Context(), agentic.Request{
		Prompt:   "Reply with the single word: ok",
		Model:    "haiku",
		MaxTurns: 1,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Model != "claude-haiku-4-5" {
		t.Errorf("the turn was answered by %q, want the request's model to beat the driver's", result.Model)
	}
	// The driver's own setting is unchanged by a request that overrode it.
	if got := d.Model(); got != "claude-sonnet-5" {
		t.Errorf("Model() = %q after an overriding request, want the driver's own setting intact", got)
	}
}

// The captured claim: the real CLI still constrains its final answer, and the
// answer still arrives in the envelope's structured_output.
func TestTheRealCLIStillConstrainsItsAnswer(t *testing.T) {
	d := integrationDriver(t)

	result, err := d.Run(t.Context(), agentic.Request{
		Prompt: "Reply in plain English prose only. Do not output any JSON. " +
			"What is the capital of France?",
		Model: "haiku",
		Schema: json.RawMessage(`{"type":"object",` +
			`"properties":{"answer":{"type":"string"},"confidence":{"type":"integer"}},` +
			`"required":["answer","confidence"],"additionalProperties":false}`),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.IsError {
		t.Fatalf("the CLI reported an error: %s", result.Text)
	}

	// The prompt argues against the schema on purpose. A CLI that merely
	// SUGGESTS the shape answers this one in prose.
	var answer struct {
		Answer     string `json:"answer"`
		Confidence int    `json:"confidence"`
	}
	if err := json.Unmarshal(result.Structured, &answer); err != nil {
		t.Fatalf("Structured is not the schema's document: %v (%s)", err, result.Structured)
	}
	if !strings.Contains(strings.ToLower(answer.Answer), "paris") {
		t.Errorf("answer = %q, want the question answered inside the shape", answer.Answer)
	}
}

// The single most fragile claim in the design, and the reason it is verified
// live rather than by fixture alone. Given a schema nothing satisfies, the CLI
// validates each attempt, rejects it, lets the model retry, and then answers in
// PROSE on exit 0 with is_error false and subtype "success". Only the absence
// of structured_output distinguishes that from an answer.
//
// The day the CLI starts setting is_error on a give-up, this is what notices.
func TestARunThatGivesUpOnTheShapeIsStillReportedAsSuccessByTheCLI(t *testing.T) {
	d := integrationDriver(t)

	result, err := d.Run(t.Context(), agentic.Request{
		Prompt: "Set n to 7. Answer immediately.",
		Model:  "haiku",
		Schema: json.RawMessage(`{"type":"object",` +
			`"properties":{"n":{"type":"integer","minimum":10,"maximum":5}},` +
			`"required":["n"],"additionalProperties":false}`),
	})
	if err != nil {
		t.Fatalf("Run reported an outage for a turn the CLI finished: %v", err)
	}
	if !result.IsError {
		t.Errorf("IsError = false on a run that produced no payload: %+v", result)
	}
	if result.Structured != nil {
		t.Errorf("Structured = %s, want nil", result.Structured)
	}
	if result.Text == "" {
		t.Error("Text is empty, so the agent's account of why it could not answer is lost")
	}
}
