package codex

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentic "github.com/pedromvgomes/agentic-driver"
)

// fold runs a captured stream through a decoder the way the driver does: one
// line at a time, in order, skipping blanks.
func fold(t *testing.T, name string) (agentic.Result, bool, []agentic.Event) {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read the fixture: %v", err)
	}

	decoder := New().NewDecoder()
	var events []agentic.Event
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		event, err := decoder.Decode([]byte(line))
		if err != nil {
			t.Fatalf("decode %q: %v", line, err)
		}
		if event.Kind != agentic.EventKindUnknown {
			events = append(events, event)
		}
	}
	result, complete := decoder.Result()
	return result, complete, events
}

func TestASuccessfulTurnIsFoldedFromTheWholeStream(t *testing.T) {
	result, complete, _ := fold(t, "success.ndjson")

	if !complete {
		t.Fatal("a stream ending in turn.completed did not produce a result")
	}
	if result.Text != "pong" {
		t.Errorf("Text = %q, want the agent's answer", result.Text)
	}
	// The session id arrives on the FIRST line and the usage on the last, which
	// is what makes the result a fold rather than a reading of either one.
	if result.SessionID != "11111111-1111-4111-8111-111111111111" {
		t.Errorf("SessionID = %q, want the thread id from the opening line", result.SessionID)
	}
	if result.Usage.InputTokens != 14059 || result.Usage.OutputTokens != 5 {
		t.Errorf("Usage = %+v, want the counts from the terminal line", result.Usage)
	}
	if result.Usage.CacheReadTokens != 9984 {
		t.Errorf("CacheReadTokens = %d, want the cached input the CLI reported", result.Usage.CacheReadTokens)
	}
	if result.IsError {
		t.Error("IsError is set on a turn that completed")
	}
	if result.Turns != 1 {
		t.Errorf("Turns = %d, want the one turn a plain exec starts", result.Turns)
	}
}

// Codex reports neither figure. Deriving a cost from token counts would quote a
// price nobody was charged, and naming the requested model would describe a
// choice the CLI never confirmed making.
func TestTheFieldsCodexNeverReportsStayZero(t *testing.T) {
	for _, name := range []string{"success.ndjson", "success-mini.ndjson", "tool-use.ndjson"} {
		result, _, _ := fold(t, name)

		if result.Usage.CostUSD != 0 {
			t.Errorf("%s: CostUSD = %v, but codex quotes no cost", name, result.Usage.CostUSD)
		}
		if result.Model != "" {
			t.Errorf("%s: Model = %q, but the stream never names the model that answered", name, result.Model)
		}
	}
}

func TestASecondModelFoldsTheSameWay(t *testing.T) {
	result, complete, _ := fold(t, "success-mini.ndjson")

	if !complete || result.Text != "pong" {
		t.Fatalf("result = %+v, want a completed turn answering pong", result)
	}
	if result.Usage.InputTokens != 10762 || result.Usage.OutputTokens != 16 {
		t.Errorf("Usage = %+v, want this model's own counts", result.Usage)
	}
}

// The answer is the LAST agent message. A turn opens with a preamble saying
// what it is about to do, so keeping the first reports a run's opening remark
// as its conclusion.
func TestTheAnswerIsTheLastAgentMessageNotTheFirst(t *testing.T) {
	result, complete, events := fold(t, "tool-use.ndjson")

	if !complete {
		t.Fatal("the tool-using stream produced no result")
	}
	if strings.HasPrefix(result.Text, "I’ll read the file") {
		t.Fatalf("Text = %q, which is the preamble rather than the answer", result.Text)
	}
	if !strings.Contains(result.Text, "hello world") {
		t.Errorf("Text = %q, want the final answer", result.Text)
	}

	// The same stream is what a caller watching the work sees, so the tool the
	// agent ran and the output it got back both have to arrive as events.
	var kinds []agentic.EventKind
	for _, event := range events {
		kinds = append(kinds, event.Kind)
	}
	if !containsKind(kinds, agentic.EventKindToolUse) {
		t.Errorf("events = %v, want a tool_use for the command the agent ran", kinds)
	}
	if !containsKind(kinds, agentic.EventKindToolResult) {
		t.Errorf("events = %v, want a tool_result for what the command printed", kinds)
	}
}

// A sandbox denial is a VERDICT OF SUCCESS: the CLI ran, the sandbox did its
// job, and the agent reported it could not act. Reading it as a failure would
// turn a correctly restricted run into an error.
func TestASandboxRefusalIsNotAFailure(t *testing.T) {
	result, complete, _ := fold(t, "sandbox-refusal.ndjson")

	if !complete {
		t.Fatal("the refusal stream produced no result")
	}
	if result.IsError {
		t.Error("IsError is set on a run the sandbox correctly restricted")
	}
	if !strings.Contains(result.Text, "read-only") {
		t.Errorf("Text = %q, want the agent's explanation of what it could not do", result.Text)
	}
}

// Both of these exit non-zero with a well-formed stream. They are verdicts, and
// the decoder reports them as complete results with IsError set — never as the
// absence of a result, which is what the driver turns into an outage.
func TestAFailedTurnIsACompleteResultNotAnOutage(t *testing.T) {
	for _, tc := range []struct{ name, fixture, wants string }{
		{"a model the account cannot use", "turn-failed-model.ndjson", "not supported"},
		{"a rejected credential", "rejected-auth.ndjson", "401"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, complete, _ := fold(t, tc.fixture)

			if !complete {
				t.Fatal("a stream ending in turn.failed produced no result, so the driver would report an outage")
			}
			if !result.IsError {
				t.Error("IsError is not set on a turn the CLI declared failed")
			}
			if !strings.Contains(result.Text, tc.wants) {
				t.Errorf("Text = %q, want the CLI's own explanation containing %q", result.Text, tc.wants)
			}
			if result.SessionID == "" {
				t.Error("SessionID is empty, but the failed run still opened a thread")
			}
		})
	}
}

// The transient noise around a rejected credential — websocket reconnection
// notices, a fallback to another transport — is bookkeeping about the
// connection, not a statement about the turn.
func TestReconnectionNoticesAreNotEvents(t *testing.T) {
	_, _, events := fold(t, "rejected-auth.ndjson")

	for _, event := range events {
		if strings.Contains(event.Text, "Reconnecting") {
			t.Errorf("a reconnection notice surfaced as a %s event", event.Kind)
		}
	}
}

// A usage error puts nothing on stdout at all. With no terminal event the
// decoder reports incomplete, which is what makes the driver call it an outage
// rather than an empty success.
func TestAUsageErrorLeavesNoResultToReport(t *testing.T) {
	decoder := New().NewDecoder()
	if _, complete := decoder.Result(); complete {
		t.Error("a decoder that has seen nothing reports a result")
	}

	// The CLI's explanation is on stderr, which is what makes it diagnosable.
	stderr, err := os.ReadFile(filepath.Join("testdata", "usage-error.stderr"))
	if err != nil {
		t.Fatalf("read the fixture: %v", err)
	}
	if !strings.Contains(string(stderr), "unexpected argument") {
		t.Errorf("stderr = %q, want the CLI's usage message", stderr)
	}
}

// A stream cut off before its terminal event decodes cleanly into a zero value.
// Reporting that as a result would answer a run that produced nothing as a
// successful empty one.
func TestATruncatedStreamIsNotAResult(t *testing.T) {
	decoder := New().NewDecoder()
	for _, line := range []string{
		`{"type":"thread.started","thread_id":"11111111-1111-4111-8111-111111111111"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"half an answer"}}`,
	} {
		if _, err := decoder.Decode([]byte(line)); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}

	if _, complete := decoder.Result(); complete {
		t.Error("a stream with no terminal event reported a result")
	}
}

func TestALineThatIsNotJSONEndsTheRun(t *testing.T) {
	if _, err := New().NewDecoder().Decode([]byte("not json at all")); err == nil {
		t.Error("a line that is not JSON decoded without complaint")
	}
}

// An event type this package does not model is skipped, not failed: a release
// adding one is not a reason to break a run that is otherwise working.
func TestAnUnmodelledEventIsSkipped(t *testing.T) {
	event, err := New().NewDecoder().Decode([]byte(`{"type":"some.future.event","detail":{}}`))
	if err != nil {
		t.Fatalf("an unmodelled event failed the run: %v", err)
	}
	if event.Kind != agentic.EventKindUnknown {
		t.Errorf("Kind = %q, want the zero Event the driver skips", event.Kind)
	}
}

// The decoder never emits a terminal result event. The driver builds that from
// Result, so a stream cannot announce one outcome while its fold reports
// another.
func TestTheDecoderDoesNotEmitTheTerminalEvent(t *testing.T) {
	for _, name := range []string{"success.ndjson", "turn-failed-model.ndjson", "tool-use.ndjson"} {
		_, _, events := fold(t, name)
		for _, event := range events {
			if event.Kind == agentic.EventKindResult {
				t.Errorf("%s: the decoder emitted a result event itself", name)
			}
		}
	}
}

// Each decoder folds exactly one run. Sharing one would carry the first run's
// answer and token counts into the second.
func TestEachRunGetsItsOwnDecoder(t *testing.T) {
	first, _, _ := fold(t, "success.ndjson")
	second, _, _ := fold(t, "success-mini.ndjson")

	if first.SessionID == second.SessionID {
		t.Error("two runs folded to the same session id")
	}
	if first.Usage.InputTokens == second.Usage.InputTokens {
		t.Error("two runs folded to the same token counts, so state leaked between them")
	}
}

func TestMaxTurnsIsRefusedRatherThanDropped(t *testing.T) {
	_, err := New().StreamCommand(agentic.Request{Prompt: "hi", MaxTurns: 3})

	if !errors.Is(err, agentic.ErrInvalidRequest) {
		t.Fatalf("error = %v, want ErrInvalidRequest for a bound codex cannot express", err)
	}
	if !strings.Contains(err.Error(), "Timeout") {
		t.Errorf("error = %q, want it to name the bound that does work everywhere", err)
	}
}

func containsKind(kinds []agentic.EventKind, want agentic.EventKind) bool {
	for _, kind := range kinds {
		if kind == want {
			return true
		}
	}
	return false
}
