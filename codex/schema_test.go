package codex

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	agentic "github.com/pedromvgomes/agentic-driver"
)

var schema = json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"]}`)

// schemaPath is where SchemaArgs put the file, taken from the argv it built.
func schemaPath(t *testing.T, args []string) string {
	t.Helper()

	i := slices.Index(args, "--output-schema")
	if i < 0 || i+1 >= len(args) {
		t.Fatalf("argv carries no --output-schema: %q", args)
	}
	return args[i+1]
}

// Codex reads the schema from a file, so the file has to exist before the
// process does — a path pointing at nothing is refused by the CLI before the
// model runs.
func TestSchemaArgsWritesTheSchemaItNames(t *testing.T) {
	args, err := New().SchemaArgs(schema)
	if err != nil {
		t.Fatalf("SchemaArgs: %v", err)
	}

	written, err := os.ReadFile(schemaPath(t, args))
	if err != nil {
		t.Fatalf("reading the schema the argv names: %v", err)
	}
	if string(written) != string(schema) {
		t.Errorf("file holds %s, want %s", written, schema)
	}
}

// The path is the digest of the contents, so one request renders one command
// however many times it is built. A per-run temporary name would make Run and
// Stream issue different argv for the same Request, and make a logged
// invocation incomparable to the next one.
func TestTheSchemaPathIsTheSameForTheSameSchema(t *testing.T) {
	first, err := New().SchemaArgs(schema)
	if err != nil {
		t.Fatalf("SchemaArgs: %v", err)
	}
	second, err := New().SchemaArgs(schema)
	if err != nil {
		t.Fatalf("SchemaArgs again: %v", err)
	}

	if !slices.Equal(first, second) {
		t.Errorf("the same schema rendered two commands:\nfirst  = %q\nsecond = %q", first, second)
	}

	sum := sha256.Sum256(schema)
	if name := filepath.Base(schemaPath(t, first)); name != hex.EncodeToString(sum[:])+".json" {
		t.Errorf("file is named %q, want the full digest of its contents", name)
	}
}

// Two schemas must never share a file: the run that lost would be constrained
// to a shape nobody asked it for, and would answer perfectly well in it.
func TestDifferentSchemasGetDifferentFiles(t *testing.T) {
	other := json.RawMessage(`{"type":"object","properties":{"finding":{"type":"string"}}}`)

	mine, err := New().SchemaArgs(schema)
	if err != nil {
		t.Fatalf("SchemaArgs: %v", err)
	}
	theirs, err := New().SchemaArgs(other)
	if err != nil {
		t.Fatalf("SchemaArgs: %v", err)
	}
	if schemaPath(t, mine) == schemaPath(t, theirs) {
		t.Errorf("both schemas landed on %s", schemaPath(t, mine))
	}
}

// The flag precedes the prompt, which is positional and last, so nothing the
// prompt contains can be read as a flag.
func TestStreamCommandCarriesTheSchemaBeforeThePrompt(t *testing.T) {
	inv, err := New().StreamCommand(agentic.Request{Prompt: "review this", Schema: schema})
	if err != nil {
		t.Fatalf("StreamCommand: %v", err)
	}

	flag := slices.Index(inv.Args, "--output-schema")
	prompt := slices.Index(inv.Args, "review this")
	if flag < 0 || prompt < 0 || flag > prompt {
		t.Errorf("argv = %q, want --output-schema before the positional prompt", inv.Args)
	}
}

// A request that asks for no schema emits no flag, so the CLI's own
// unconstrained behaviour applies.
func TestNoSchemaMeansNoFlag(t *testing.T) {
	inv, err := New().StreamCommand(agentic.Request{Prompt: "hi"})
	if err != nil {
		t.Fatalf("StreamCommand: %v", err)
	}
	if slices.Contains(inv.Args, "--output-schema") {
		t.Errorf("argv = %q, want no schema flag", inv.Args)
	}
}

// The grammar constrains the decoder itself, so the answer is the last agent
// message and it is already the document the caller wants.
func TestAConstrainedRunAnswersInStructured(t *testing.T) {
	req := agentic.Request{Prompt: "hi", Schema: schema}
	result, ok, _ := fold(t, "structured.ndjson", req)
	if !ok {
		t.Fatal("the run reported no result")
	}
	if result.IsError {
		t.Errorf("IsError = true, want a constrained run to be a clean verdict: %+v", result)
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

// A turn opens with a prose preamble and only its FINAL message is constrained,
// so a decoder keeping the first would answer with the run's opening remark.
func TestOnlyTheFinalMessageIsTheStructuredAnswer(t *testing.T) {
	req := agentic.Request{Prompt: "hi", Schema: schema}
	result, _, seen := fold(t, "structured-preamble.ndjson", req)

	var texts []string
	for _, event := range seen {
		if event.Kind == agentic.EventKindText {
			texts = append(texts, event.Text)
		}
	}
	if len(texts) < 2 {
		t.Fatalf("fixture yielded %d text events, want a preamble and an answer", len(texts))
	}
	if json.Valid([]byte(texts[0])) {
		t.Errorf("the preamble %q parses as JSON, so this fixture no longer proves the distinction", texts[0])
	}

	if !json.Valid(result.Structured) {
		t.Errorf("Structured = %s, want the final message", result.Structured)
	}
}

// A schema nothing can satisfy is a grammar the model cannot finish, so it
// generates until its output ceiling and codex reports a failed turn. It ran
// and said so: a verdict, with no payload.
func TestAnUnsatisfiableSchemaIsAFailedTurnWithNoPayload(t *testing.T) {
	req := agentic.Request{Prompt: "hi", Schema: schema}
	result, ok, _ := fold(t, "structured-unsatisfiable.ndjson", req)
	if !ok {
		t.Fatal("the run reported no result")
	}
	if !result.IsError {
		t.Error("IsError = false, want the failed turn reported as a bad verdict")
	}
	if result.Structured != nil {
		t.Errorf("Structured = %s, want nil on a run that produced no payload", result.Structured)
	}
	if !strings.Contains(result.Text, "max_output_tokens") {
		t.Errorf("Text = %q, want codex's own account of why the turn failed", result.Text)
	}
}

// Codex only checks that the schema file parses as JSON; the API judges the
// rest, and reports its verdict through a turn that starts and then fails.
// Unlike a malformed file, this is a statement about the request.
func TestASchemaTheAPIRejectsIsAVerdict(t *testing.T) {
	req := agentic.Request{Prompt: "hi", Schema: schema}
	result, ok, _ := fold(t, "structured-invalid-schema.ndjson", req)
	if !ok {
		t.Fatal("the run reported no result")
	}
	if !result.IsError {
		t.Error("IsError = false, want the rejected schema reported as a bad verdict")
	}
	if !strings.Contains(result.Text, "invalid_json_schema") {
		t.Errorf("Text = %q, want the API's own explanation", result.Text)
	}
}

// A run that asked for nothing in particular is not held to a shape, however
// unlike JSON its answer is.
func TestAnUnconstrainedRunLeavesStructuredNil(t *testing.T) {
	result, _, _ := fold(t, "success.ndjson", agentic.Request{Prompt: "hi"})
	if result.Structured != nil {
		t.Errorf("Structured = %s, want nil when no schema was asked for", result.Structured)
	}
	if result.IsError {
		t.Error("IsError = true, want an unconstrained run to be unaffected by the rule")
	}
}

// The driver refuses a schema no provider can honour before spawning anything,
// which is what makes this sentinel worth matching on.
func TestSchemaArgsAcceptsWhatTheDriverHasAlreadyValidated(t *testing.T) {
	// The driver checks json.Valid first, so a provider never sees a malformed
	// document. What it must not do is invent a second opinion about a
	// well-formed one the CLI would have accepted.
	odd := json.RawMessage(`{"type":"banana"}`)
	if _, err := New().SchemaArgs(odd); err != nil {
		t.Errorf("SchemaArgs refused a well-formed schema: %v", err)
	}
}

// A schema file codex cannot read produces no stream at all: nothing on stdout,
// an explanation on stderr, and a non-zero exit. There is no verdict in that —
// the run never happened — so it reaches a caller as an outage, and stderr is
// the only thing that makes it diagnosable.
//
// This is the case the driver's own json.Valid check exists to prevent reaching
// the CLI, and the fixture is what says the check is worth having.
func TestASchemaFileCodexCannotReadLeavesNoResultToReport(t *testing.T) {
	decoder := New().NewDecoder(agentic.Request{Prompt: "hi", Schema: schema})
	if _, complete := decoder.Result(); complete {
		t.Error("a decoder that has seen nothing reports a result")
	}

	stderr, err := os.ReadFile(filepath.Join("testdata", "structured-unreadable-schema.stderr"))
	if err != nil {
		t.Fatalf("read the fixture: %v", err)
	}
	if !strings.Contains(string(stderr), "not valid JSON") {
		t.Errorf("stderr = %q, want the CLI's explanation of the schema file", stderr)
	}
}
