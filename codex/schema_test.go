package codex

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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
	// The file is named for its contents and outlives the process that wrote
	// it, so a run on a machine that has seen this schema before would find it
	// already there and assert on someone else's work. Removing it first is
	// what makes this a test of the write.
	path := removeSchemaFile(t, schema)

	args, err := New().SchemaArgs(schema)
	if err != nil {
		t.Fatalf("SchemaArgs: %v", err)
	}
	if got := schemaPath(t, args); got != path {
		t.Fatalf("argv names %s, want %s", got, path)
	}

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the schema the argv names: %v", err)
	}
	if string(written) != string(schema) {
		t.Errorf("file holds %s, want %s", written, schema)
	}
}

// removeSchemaFile clears the file a schema would be published to and returns
// its path, so a test starts from a machine that has never seen this schema.
func removeSchemaFile(t *testing.T, schema json.RawMessage) string {
	t.Helper()

	// The published path is a function of TMPDIR, and every test here would
	// otherwise share one absolute path with any concurrent run of this package.
	t.Setenv("TMPDIR", t.TempDir())

	dir, err := (&Provider{}).schemaDir()
	if err != nil {
		t.Fatalf("schemaDir: %v", err)
	}
	sum := sha256.Sum256(schema)
	path := filepath.Join(dir, hex.EncodeToString(sum[:])+".json")
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("clearing %s: %v", path, err)
	}
	return path
}

// The digest names the file; it does not vouch for it. Anything able to write
// the directory could have put a different document at the name, and a run
// constrained to a schema nobody asked for still answers in valid JSON with
// nothing to mark it wrong — so the contents are read and compared, never
// inferred from the path.
func TestAPlantedSchemaFileIsReplaced(t *testing.T) {
	path := removeSchemaFile(t, schema)

	planted := []byte(`{"type":"object","properties":{"attacker":{"type":"string"}}}`)
	if err := os.WriteFile(path, planted, 0o600); err != nil {
		t.Fatalf("planting a file: %v", err)
	}

	if _, err := New().SchemaArgs(schema); err != nil {
		t.Fatalf("SchemaArgs: %v", err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the schema: %v", err)
	}
	if string(written) != string(schema) {
		t.Errorf("file holds %s, want the requested schema %s", written, schema)
	}
}

// A symlink at the schema's name would send codex to read a file the driver
// never meant it to, and an existence check that follows links cannot see one.
func TestASymlinkAtTheSchemaNameIsReplaced(t *testing.T) {
	path := removeSchemaFile(t, schema)

	elsewhere := filepath.Join(t.TempDir(), "elsewhere.json")
	if err := os.WriteFile(elsewhere, []byte(`{"type":"object"}`), 0o600); err != nil {
		t.Fatalf("preparing the link target: %v", err)
	}
	if err := os.Symlink(elsewhere, path); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}

	if _, err := New().SchemaArgs(schema); err != nil {
		t.Fatalf("SchemaArgs: %v", err)
	}

	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("inspecting the published path: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error("the schema path is still a symlink, so codex would read whatever it points at")
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the schema: %v", err)
	}
	if string(written) != string(schema) {
		t.Errorf("file holds %s, want the requested schema", written)
	}
	// The link target is left alone: renaming over a symlink replaces the link,
	// not the file at the end of it.
	target, err := os.ReadFile(elsewhere)
	if err != nil || string(target) != `{"type":"object"}` {
		t.Errorf("the link target was written through: %s (%v)", target, err)
	}
}

// A directory shared with another account cannot be trusted to hold only what
// this process put there, and MkdirAll succeeds on one whoever owns it.
func TestASchemaDirectoryOpenToOtherUsersIsRefused(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())

	p := &Provider{}
	dir, err := p.schemaDir()
	if err != nil {
		t.Fatalf("schemaDir: %v", err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatalf("widening the directory: %v", err)
	}

	if _, err := p.schemaDir(); err == nil {
		t.Error("a world-writable schema directory was accepted")
	}
}

// A directory that cannot be made is an outage: nothing was said about the
// request, and no process ran.
func TestASchemaThatCannotBeWrittenIsAnOutage(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("in the way"), 0o600); err != nil {
		t.Fatalf("preparing the obstruction: %v", err)
	}
	t.Setenv("TMPDIR", blocked)

	_, err := (&Provider{}).SchemaArgs(schema)
	if !errors.Is(err, agentic.ErrProviderUnavailable) {
		t.Errorf("error = %v, want ErrProviderUnavailable", err)
	}
	if errors.Is(err, agentic.ErrInvalidRequest) {
		t.Error("a filesystem failure was reported as a bad request")
	}
}

// Codex constrains the decoder, so a completed turn normally cannot answer with
// anything but conforming JSON. The backstop is for the turn that ends some
// other way — one truncated mid-document leaves text that is not a document,
// and the CLI reports that turn a success.
func TestATruncatedAnswerIsAnUnmetConstraintOnASuccessfulTurn(t *testing.T) {
	decoder := New().NewDecoder(agentic.Request{Prompt: "hi", Schema: schema})
	for _, line := range []string{
		`{"type":"thread.started","thread_id":"77777777-7777-4777-8777-777777777777"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"{\"answer\":\"trunc"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`,
	} {
		if _, err := decoder.Decode([]byte(line)); err != nil {
			t.Fatalf("decode %q: %v", line, err)
		}
	}

	result, ok := decoder.Result()
	if !ok {
		t.Fatal("the run reported no result")
	}
	if !result.IsError {
		t.Error("IsError = false on a turn whose answer is not a document")
	}
	if result.Structured != nil {
		t.Errorf("Structured = %s, want nil", result.Structured)
	}
	// The half-document is what codex said, and it is what a caller needs to
	// see; reporting the failure without it explains nothing.
	if !strings.Contains(result.Text, "trunc") {
		t.Errorf("Text = %q, want the answer codex actually produced", result.Text)
	}
}

// A JSON null satisfies every syntactic test and answers nothing: unmarshalling
// it leaves the caller's value zeroed with no sign anything went wrong.
func TestANullAnswerIsNotAPayload(t *testing.T) {
	decoder := New().NewDecoder(agentic.Request{Prompt: "hi", Schema: schema})
	for _, line := range []string{
		`{"type":"thread.started","thread_id":"77777777-7777-4777-8777-777777777777"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"null"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`,
	} {
		if _, err := decoder.Decode([]byte(line)); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}

	result, _ := decoder.Result()
	if !result.IsError || result.Structured != nil {
		t.Errorf("IsError = %v, Structured = %s; want a null answer treated as no answer",
			result.IsError, result.Structured)
	}
}

// A Result whose IsError is set beside an empty Text says a run failed without
// saying anything about it. A turn that completes carrying no agent message at
// all has no explanation to borrow, so the decoder supplies one.
func TestAnUnmetConstraintAlwaysSaysSomething(t *testing.T) {
	decoder := New().NewDecoder(agentic.Request{Prompt: "hi", Schema: schema})
	for _, line := range []string{
		`{"type":"thread.started","thread_id":"77777777-7777-4777-8777-777777777777"}`,
		`{"type":"turn.started"}`,
		`{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`,
	} {
		if _, err := decoder.Decode([]byte(line)); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}

	result, _ := decoder.Result()
	if !result.IsError {
		t.Fatal("IsError = false on a run that produced no answer at all")
	}
	if result.Text == "" {
		t.Error("Text is empty, so the verdict reports a failure it cannot explain")
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
	// The message names the file, which is what makes it diagnosable: the
	// driver's own error says only that a run produced nothing.
	if !strings.Contains(string(stderr), schemaDirName) || !strings.Contains(string(stderr), ".json") {
		t.Errorf("stderr = %q, want it to name the schema file the driver passed", stderr)
	}
}

// A directory standing at the schema's name cannot be renamed over, so leaving
// it would make that one schema permanently unusable for as long as it sat
// there.
func TestADirectoryAtTheSchemaNameIsReplaced(t *testing.T) {
	path := removeSchemaFile(t, schema)

	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("planting a directory: %v", err)
	}
	if _, err := New().SchemaArgs(schema); err != nil {
		t.Fatalf("SchemaArgs: %v", err)
	}

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the schema: %v", err)
	}
	if string(written) != string(schema) {
		t.Errorf("file holds %s, want the requested schema", written)
	}
}

// A temporary directory is swept by the system, so a provider that remembered
// its own earlier work would keep naming a file that had since been reaped.
func TestASchemaReapedBetweenRunsIsRepublished(t *testing.T) {
	path := removeSchemaFile(t, schema)

	p := New()
	if _, err := p.SchemaArgs(schema); err != nil {
		t.Fatalf("SchemaArgs: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("reaping the schema: %v", err)
	}

	args, err := p.SchemaArgs(schema)
	if err != nil {
		t.Fatalf("SchemaArgs after the file was reaped: %v", err)
	}
	if _, err := os.ReadFile(schemaPath(t, args)); err != nil {
		t.Errorf("the argv names a file that does not exist: %v", err)
	}
}

// Runs sharing a schema arrive together, and the file has to be published once
// and readable by all of them — never half-written, and never a path one run
// hands to codex while another is still creating it.
func TestConcurrentRunsSharingASchemaPublishItOnce(t *testing.T) {
	removeSchemaFile(t, schema)

	p := New()
	paths := make([]string, 8)
	var wg sync.WaitGroup
	for i := range paths {
		wg.Add(1)
		go func() {
			defer wg.Done()
			args, err := p.SchemaArgs(schema)
			if err != nil {
				t.Errorf("SchemaArgs: %v", err)
				return
			}
			paths[i] = schemaPath(t, args)
		}()
	}
	wg.Wait()

	for i, path := range paths {
		written, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("run %d names an unreadable schema: %v", i, err)
		}
		if string(written) != string(schema) {
			t.Errorf("run %d sees %s, want the requested schema", i, written)
		}
		if path != paths[0] {
			t.Errorf("run %d published to %s, run 0 to %s", i, path, paths[0])
		}
	}
}
