package claudecode

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	agentic "github.com/pedromvgomes/agentic-driver"
)

var schema = json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"]}`)

// The schema goes on the command line as the document itself, so nothing
// reaches disk and the argv stays a pure function of the request.
func TestStreamCommandCarriesTheSchemaInline(t *testing.T) {
	p := testProvider(t)

	inv, err := p.StreamCommand(agentic.Request{Prompt: "review this", Schema: schema})
	if err != nil {
		t.Fatalf("StreamCommand: %v", err)
	}

	i := slices.Index(inv.Args, "--json-schema")
	if i < 0 || i+1 >= len(inv.Args) {
		t.Fatalf("argv carries no --json-schema: %q", inv.Args)
	}
	if inv.Args[i+1] != string(schema) {
		t.Errorf("--json-schema = %q, want the schema document verbatim", inv.Args[i+1])
	}
}

func TestNoSchemaMeansNoFlag(t *testing.T) {
	p := testProvider(t)

	inv, err := p.StreamCommand(agentic.Request{Prompt: "hi"})
	if err != nil {
		t.Fatalf("StreamCommand: %v", err)
	}
	if slices.Contains(inv.Args, "--json-schema") {
		t.Errorf("argv = %q, want no schema flag", inv.Args)
	}
}

// StructuredOutput is the tool the CLI offers the model to answer in the
// required shape, and it is NOT subject to --allowedTools. Reconciling the two
// here would refuse a combination the CLI honours.
func TestAnAllowlistDoesNotHaveToGrantStructuredOutput(t *testing.T) {
	p := testProvider(t)

	inv, err := p.StreamCommand(agentic.Request{
		Prompt:       "hi",
		Schema:       schema,
		AllowedTools: []string{"Read"},
	})
	if err != nil {
		t.Fatalf("StreamCommand: %v", err)
	}
	if !slices.Contains(inv.Args, "--json-schema") {
		t.Errorf("argv = %q, want the schema alongside a narrow allowlist", inv.Args)
	}
}

// The answer to a constrained run is the envelope's structured_output, which is
// the field that says the constraint actually held.
func TestAConstrainedRunAnswersInStructured(t *testing.T) {
	p := testProvider(t)
	req := agentic.Request{Prompt: "hi", Schema: schema}

	_, decoder := events(t, p, golden(t, "structured.json"), req)
	result, ok := decoder.Result()
	if !ok {
		t.Fatal("the run reported no result")
	}
	if result.IsError {
		t.Errorf("IsError = true, want a constrained run that answered to be a clean verdict: %+v", result)
	}

	var answer struct {
		Answer     string `json:"answer"`
		Confidence int    `json:"confidence"`
	}
	if err := json.Unmarshal(result.Structured, &answer); err != nil {
		t.Fatalf("Structured is not the schema's document: %v (%s)", err, result.Structured)
	}
	if answer.Answer == "" {
		t.Errorf("Structured = %s, want the schema's fields populated", result.Structured)
	}
}

// The rule the whole capability rests on. Claude Code validates each attempt at
// the shape, feeds the rejection back, and when the model gives up it answers
// in prose on exit 0 — is_error false, subtype "success", and no
// structured_output. A caller that asked for JSON and received prose has not
// been answered, and nothing in the envelope says so.
func TestARunThatGaveUpOnTheShapeIsABadVerdict(t *testing.T) {
	p := testProvider(t)
	raw := golden(t, "structured-unmet.json")

	// What the CLI itself said, before the library judges it.
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("reading the fixture: %v", err)
	}
	if env.IsError || env.Subtype != "success" {
		t.Fatalf("the fixture no longer captures a give-up the CLI calls a success: is_error=%v subtype=%q",
			env.IsError, env.Subtype)
	}

	_, decoder := events(t, p, raw, agentic.Request{Prompt: "hi", Schema: schema})
	result, ok := decoder.Result()
	if !ok {
		t.Fatal("the run reported no result")
	}
	if !result.IsError {
		t.Error("IsError = false, want a run that produced no payload reported as a bad verdict")
	}
	if result.Structured != nil {
		t.Errorf("Structured = %s, want nil", result.Structured)
	}
	if !strings.Contains(result.Text, "StructuredOutput") {
		t.Errorf("Text = %q, want the agent's own account of why it could not answer in the shape", result.Text)
	}
}

// The same envelope, asked for nothing in particular, is exactly what the CLI
// called it. The rule applies to runs that carried a schema and to no others.
func TestTheSameEnvelopeIsCleanWhenNoSchemaWasAskedFor(t *testing.T) {
	p := testProvider(t)

	_, decoder := events(t, p, golden(t, "structured-unmet.json"), agentic.Request{Prompt: "hi"})
	result, _ := decoder.Result()
	if result.IsError {
		t.Error("IsError = true, want the CLI's own verdict when no schema was required")
	}
	if result.Structured != nil {
		t.Errorf("Structured = %s, want nil when no schema was asked for", result.Structured)
	}
}

// A caller watching the work sees the constraint being attempted and rejected,
// which is the difference between a run that is failing and one that is slow.
func TestTheRejectedAttemptsAreVisibleInTheStream(t *testing.T) {
	p := testProvider(t)

	seen, _ := events(t, p, golden(t, "structured-unmet.ndjson"), agentic.Request{Prompt: "hi", Schema: schema})

	var attempts, rejections int
	for _, event := range seen {
		switch {
		case event.Kind == agentic.EventKindToolUse && event.Text == "StructuredOutput":
			attempts++
		case event.Kind == agentic.EventKindToolResult && strings.Contains(event.Text, "does not match required schema"):
			rejections++
		}
	}
	if attempts == 0 {
		t.Error("no StructuredOutput tool use reached the caller")
	}
	if rejections == 0 {
		t.Error("no schema rejection reached the caller")
	}
}

// Parse reports what the envelope said and nothing more. The judgement about a
// missing payload belongs to the decoder, which is the only thing that knows
// what the run was asked for.
func TestParseReportsTheEnvelopeWithoutJudgingTheShape(t *testing.T) {
	p := testProvider(t)

	result, err := p.Parse(golden(t, "structured-unmet.json"), nil, 0)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if result.IsError {
		t.Error("Parse set IsError; only a decoder holding the request may do that")
	}
}
