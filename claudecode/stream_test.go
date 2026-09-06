package claudecode

import (
	"bufio"
	"bytes"
	"reflect"
	"testing"

	agentic "github.com/pedromvgomes/agentic-driver"
	"strings"
)

// events decodes every line of a committed stream, mirroring what the driver
// does with the live one, and returns the fold alongside what a caller watching
// would have seen.
func events(t *testing.T, p *Provider, raw []byte, req agentic.Request) ([]agentic.Event, agentic.Decoder) {
	t.Helper()

	decoder := p.NewDecoder(req)
	var out []agentic.Event
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		event, err := decoder.Decode(line)
		if err != nil {
			t.Fatalf("Decode(%s): %v", line, err)
		}
		if event.Kind == agentic.EventKindUnknown {
			continue
		}
		out = append(out, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return out, decoder
}

// decode runs one line through a fresh decoder, for the cases where a single
// line is the whole point.
func decodeOne(t *testing.T, p *Provider, line string) agentic.Event {
	t.Helper()

	event, err := p.NewDecoder(agentic.Request{}).Decode([]byte(line))
	if err != nil {
		t.Fatalf("Decode(%s): %v", line, err)
	}
	return event
}

func TestAStreamYieldsTextAndFoldsToAResult(t *testing.T) {
	p := testProvider(t)

	got, decoder := events(t, p, golden(t, "stream.ndjson"), agentic.Request{})
	if len(got) == 0 {
		t.Fatal("the stream yielded no events at all")
	}

	result, complete := decoder.Result()
	if !complete {
		t.Fatal("a stream carrying a terminal envelope folded to no result")
	}
	if result.SessionID == "" {
		t.Error("the fold carries no session, so the run cannot be resumed")
	}

	var sawText bool
	for _, event := range got {
		if event.Kind == agentic.EventKindText && event.Text != "" {
			sawText = true
		}
		// The terminal event is the driver's to build, so a decoder must never
		// announce one itself.
		if event.Kind == agentic.EventKindResult {
			t.Error("the decoder emitted a result event of its own")
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
		event := decodeOne(t, p, line)
		if event.Kind != agentic.EventKindUnknown {
			t.Errorf("Decode(%s) = %q, want it skipped", line, event.Kind)
		}
	}
}

// The terminal line of a stream is the same document a non-streaming run
// prints. Reading it twice, in two places, is how the two dialects drift.
func TestTheFoldAgreesWithParsingTheTerminalLine(t *testing.T) {
	p := testProvider(t)

	raw := golden(t, "stream.ndjson")
	_, decoder := events(t, p, raw, agentic.Request{})
	folded, complete := decoder.Result()
	if !complete {
		t.Fatal("the stream folded to no result")
	}

	var terminal []byte
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if bytes.Contains(line, []byte(`"type": "result"`)) {
			terminal = line
		}
	}
	if terminal == nil {
		t.Fatal("the fixture carries no terminal line")
	}

	direct, err := p.Parse(terminal, nil, 0)
	if err != nil {
		t.Fatalf("Parse of the terminal line: %v", err)
	}
	if !reflect.DeepEqual(direct, folded) {
		t.Errorf("the fold and Parse disagree:\nfold  = %+v\nparse = %+v", folded, direct)
	}
}

// The driver scans into a buffer it reuses, so an Event holding the scanner's
// slice would change under whoever reads it next.
func TestAnEventOwnsItsRawLine(t *testing.T) {
	p := testProvider(t)

	line := []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}`)
	event, err := p.NewDecoder(agentic.Request{}).Decode(line)
	if err != nil {
		t.Fatalf("Decode: %v", err)
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

	got, _ := events(t, p, golden(t, "stream-tools.ndjson"), agentic.Request{})

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

	event := decodeOne(t, p, `{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"deliberating"}]}}`)
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
			event := decodeOne(t, p, line)
			if event.Kind != agentic.EventKindToolResult {
				t.Fatalf("Kind = %q, want a tool result", event.Kind)
			}
			if event.Text != "plain output" {
				t.Errorf("Text = %q, want %q", event.Text, "plain output")
			}
		})
	}
}
