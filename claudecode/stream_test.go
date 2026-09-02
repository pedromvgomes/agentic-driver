package claudecode

import (
	"bufio"
	"bytes"
	"testing"

	agentic "github.com/pedromvgomes/agentic-driver"
	"strings"
)

// events decodes every line of a committed stream, mirroring what the driver
// does with the live one.
func events(t *testing.T, p *Provider, raw []byte) []agentic.Event {
	t.Helper()

	var out []agentic.Event
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		event, err := p.ParseEvent(line)
		if err != nil {
			t.Fatalf("ParseEvent(%s): %v", line, err)
		}
		if event.Kind == agentic.EventKindUnknown {
			continue
		}
		out = append(out, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return out
}

func TestAStreamYieldsTextAndEndsWithAResult(t *testing.T) {
	p := testProvider(t)

	got := events(t, p, golden(t, "stream.ndjson"))
	if len(got) == 0 {
		t.Fatal("the stream yielded no events at all")
	}

	last := got[len(got)-1]
	if last.Kind != agentic.EventKindResult {
		t.Errorf("the stream ends with %q, want the terminal result envelope", last.Kind)
	}
	if last.Result.SessionID == "" {
		t.Error("the terminal event carries no session, so the stream cannot be resumed")
	}

	var sawText bool
	for _, event := range got[:len(got)-1] {
		if event.Kind == agentic.EventKindText && event.Text != "" {
			sawText = true
		}
	}
	if !sawText {
		t.Errorf("no text event in %d events, so a caller streaming output would see nothing", len(got))
	}
}

// The stream carries bookkeeping lines a caller consuming output has no use
// for. Yielding them as errors would fail a run that is working, and a release
// adding another type would break every caller at once.
func TestAnUnmodelledLineIsSkippedNotFailed(t *testing.T) {
	p := testProvider(t)

	for _, line := range []string{
		`{"type":"rate_limit_event","status":"allowed"}`,
		`{"type":"system","subtype":"init"}`,
		`{"type":"a_type_that_does_not_exist_yet"}`,
	} {
		event, err := p.ParseEvent([]byte(line))
		if err != nil {
			t.Errorf("ParseEvent(%s) failed instead of skipping: %v", line, err)
		}
		if event.Kind != agentic.EventKindUnknown {
			t.Errorf("ParseEvent(%s) = %q, want it skipped", line, event.Kind)
		}
	}
}

// The terminal line of a stream is the same document a non-streaming run
// prints. Parsing it twice, in two places, is how the two dialects drift.
func TestTheTerminalEventIsParsedLikeAnyOtherEnvelope(t *testing.T) {
	p := testProvider(t)

	got := events(t, p, golden(t, "stream.ndjson"))
	last := got[len(got)-1]

	direct, err := p.Parse(last.Raw, nil, 0)
	if err != nil {
		t.Fatalf("Parse of the terminal line: %v", err)
	}
	if direct != last.Result {
		t.Errorf("the terminal event and Parse disagree:\nevent = %+v\nparse = %+v", last.Result, direct)
	}
}

// The driver scans into a buffer it reuses, so an Event holding the scanner's
// slice would change under whoever reads it next.
func TestAnEventOwnsItsRawLine(t *testing.T) {
	p := testProvider(t)

	line := []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}`)
	event, err := p.ParseEvent(line)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}

	before := string(event.Raw)
	for i := range line {
		line[i] = 'x'
	}
	if string(event.Raw) != before {
		t.Error("the event's Raw changed when the caller's buffer was overwritten")
	}
}

// A tool's output comes back on a USER line, not an assistant one — the
// transcript models a tool as something that answers the agent. Reading tool
// results off assistant lines finds none, and a caller watching the agent work
// never learns what its tools returned.
func TestAStreamCarriesToolUseAndItsResult(t *testing.T) {
	p := testProvider(t)

	got := events(t, p, golden(t, "stream-tools.ndjson"))

	var use, result *agentic.Event
	for i := range got {
		switch got[i].Kind {
		case agentic.EventKindToolUse:
			use = &got[i]
		case agentic.EventKindToolResult:
			result = &got[i]
		}
	}

	if use == nil {
		t.Fatal("no tool-use event in a stream that used a tool")
	}
	if use.Text != "Read" {
		t.Errorf("tool-use event names %q, want the tool's name", use.Text)
	}
	if result == nil {
		t.Fatal("no tool-result event in a stream whose tool answered")
	}
	if !strings.Contains(result.Text, "hello") {
		t.Errorf("tool-result text = %q, want the tool's own output", result.Text)
	}
}

// The agent's reasoning is not its output, and yielding it as text would put it
// in front of anyone streaming the answer.
func TestThinkingBlocksAreNotYieldedAsText(t *testing.T) {
	p := testProvider(t)

	event, err := p.ParseEvent([]byte(`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"deliberating"}]}}`))
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	if event.Kind != agentic.EventKindUnknown {
		t.Errorf("a thinking block yielded a %q event", event.Kind)
	}
}

// Most tools return a bare string; those with structured output return a list
// of blocks. A caller displaying progress should not have to know which.
func TestAToolResultIsTextWhicheverShapeItArrivesIn(t *testing.T) {
	p := testProvider(t)

	for name, line := range map[string]string{
		"bare string": `{"type":"user","message":{"content":[{"type":"tool_result","content":"plain output"}]}}`,
		"block list":  `{"type":"user","message":{"content":[{"type":"tool_result","content":[{"type":"text","text":"plain output"}]}]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			event, err := p.ParseEvent([]byte(line))
			if err != nil {
				t.Fatalf("ParseEvent: %v", err)
			}
			if event.Kind != agentic.EventKindToolResult {
				t.Fatalf("Kind = %q, want a tool result", event.Kind)
			}
			if event.Text != "plain output" {
				t.Errorf("Text = %q, want %q", event.Text, "plain output")
			}
		})
	}
}
