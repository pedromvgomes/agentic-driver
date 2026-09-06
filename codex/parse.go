package codex

import (
	"bytes"
	"encoding/json"

	agentic "github.com/pedromvgomes/agentic-driver"
)

// event is the part of one `codex exec --json` line this package reads. Every
// other field is deliberately left undecoded: modelling them would make each
// release's additions a breaking change here.
type event struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Item     struct {
		Type             string `json:"type"`
		Text             string `json:"text"`
		Command          string `json:"command"`
		AggregatedOutput string `json:"aggregated_output"`
	} `json:"item"`
	Usage struct {
		InputTokens       int `json:"input_tokens"`
		CachedInputTokens int `json:"cached_input_tokens"`
		CacheWriteTokens  int `json:"cache_write_input_tokens"`
		OutputTokens      int `json:"output_tokens"`
	} `json:"usage"`
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

// NewDecoder returns a decoder for one run.
func (p *Provider) NewDecoder(req agentic.Request) agentic.Decoder {
	return &decoder{schema: req.Schema != nil}
}

// decoder folds one run of `codex exec --json`.
//
// Codex has no result envelope. Its terminal line carries token usage and
// nothing else — no session id, no answer — so a Result exists only as the fold
// of a whole run: the thread id arrives on the first line, the answer on the
// last agent message, the usage on the last line of all.
type decoder struct {
	sessionID string
	text      string
	failure   string
	usage     agentic.Usage
	turns     int
	isError   bool
	complete  bool
	// schema records that the run was required to answer in a shape, which is
	// the only way the fold can tell an answer from an explanation of why there
	// is none.
	schema bool
}

// Decode consumes one line of the stream.
//
// A line whose type this package does not model returns the zero Event, which
// the driver skips. Codex emits transient diagnostics — websocket reconnection
// notices, a fallback from one transport to another — that are bookkeeping
// about the connection rather than statements about the turn. A run that
// reconnects four times and then answers is a successful run.
func (d *decoder) Decode(line []byte) (agentic.Event, error) {
	var ev event
	if err := json.Unmarshal(line, &ev); err != nil {
		return agentic.Event{}, err
	}

	switch ev.Type {
	case "thread.started":
		d.sessionID = ev.ThreadID
		return agentic.Event{}, nil

	case "turn.started":
		// Counted rather than assumed to be one. A plain exec starts a single
		// turn, but a resumed session carries the turns that came before it.
		d.turns++
		return agentic.Event{}, nil

	case "item.started", "item.completed":
		return d.item(ev, line), nil

	case "turn.completed":
		d.usage = agentic.Usage{
			InputTokens:         ev.Usage.InputTokens,
			OutputTokens:        ev.Usage.OutputTokens,
			CacheReadTokens:     ev.Usage.CachedInputTokens,
			CacheCreationTokens: ev.Usage.CacheWriteTokens,
			// CostUSD is left zero: the turn.completed usage object carries
			// token counts and nothing else, on every plan.
			//
			// Codex does know a cost, in two places this cannot reach. One is
			// an OpenTelemetry metric, codex.turn.cost_microusd, which goes to
			// an OTLP exporter rather than to stdout. The other is a TUI
			// status-line item documented as "Estimated current-thread cost in
			// USD (Enterprise workspaces only; omitted when unavailable)" —
			// gated on the workspace, not on the plan, and an estimate even
			// there.
			//
			// Deriving one from the token counts instead would put a number
			// the provider never quoted beside figures it did, and a caller
			// summing costs across providers would be adding a real price to
			// an invented one.
		}
		d.complete = true
		return agentic.Event{}, nil

	case "turn.failed":
		// A verdict, not an outage: the CLI ran, was understood, and reported
		// that its own turn failed. The Result is populated and the error is
		// nil, so a caller can tell a rejected credential from a missing
		// binary.
		d.failure = ev.Error.Message
		d.isError = true
		d.complete = true
		return agentic.Event{}, nil

	default:
		return agentic.Event{}, nil
	}
}

// item renders one item of the stream.
//
// A turn emits several agent messages — a preamble announcing what it is about
// to do, then the answer — so the LAST one is the result and each earlier one
// is progress. Keeping the first would report a run's opening remark as its
// conclusion.
func (d *decoder) item(ev event, line []byte) agentic.Event {
	switch ev.Item.Type {
	case "agent_message":
		if ev.Item.Text == "" {
			return agentic.Event{}
		}
		if ev.Type == "item.completed" {
			d.text = ev.Item.Text
		}
		return agentic.Event{Kind: agentic.EventKindText, Text: ev.Item.Text, Raw: clone(line)}

	case "command_execution":
		// The same item arrives twice: once on item.started with no output,
		// and again on item.completed carrying what the command printed. They
		// are the agent invoking a tool and the tool answering it.
		if ev.Type == "item.started" {
			return agentic.Event{Kind: agentic.EventKindToolUse, Text: ev.Item.Command, Raw: clone(line)}
		}
		return agentic.Event{Kind: agentic.EventKindToolResult, Text: ev.Item.AggregatedOutput, Raw: clone(line)}

	default:
		return agentic.Event{}
	}
}

// Result is the fold over the run.
//
// It reports incomplete until a terminal event arrives. Positive evidence is
// required: a stream that stopped early decodes without complaint into a zero
// value, and reading that as success reports a run which produced no answer
// whatsoever as a clean one.
func (d *decoder) Result() (agentic.Result, bool) {
	if !d.complete {
		return agentic.Result{}, false
	}

	structured, unmet := d.constrained()
	return agentic.Result{
		Text:       d.answer(unmet),
		Structured: structured,
		SessionID:  d.sessionID,
		// Model is left zero. The stream never names the model that answered,
		// and echoing back the one that was REQUESTED would describe a choice
		// the CLI never confirmed making.
		Model:   "",
		Usage:   d.usage,
		Turns:   d.turns,
		IsError: d.isError || unmet,
	}, true
}

// constrained reports the schema-conforming answer, and whether a run that
// required one did not produce it.
//
// Codex constrains the decoder itself, so the final agent message of a
// successful turn cannot be anything but conforming JSON — which makes this a
// backstop rather than a routine path. It earns its place on the turns that end
// some other way: a turn that failed carries no agent message at all, and one
// truncated mid-document leaves text that is not a document, and neither is an
// answer to a caller waiting to unmarshal one.
func (d *decoder) constrained() (json.RawMessage, bool) {
	if !d.schema {
		return nil, false
	}
	if d.isError || !isPayload([]byte(d.text)) {
		return nil, true
	}
	return json.RawMessage(d.text), false
}

// isPayload reports whether a final message is an answer in the required shape.
//
// A JSON null passes every syntactic test and is not an answer: a caller
// unmarshalling it gets the zero value of whatever it decoded into and no sign
// that anything went wrong, which is precisely the outcome the schema was there
// to rule out.
func isPayload(text []byte) bool {
	trimmed := bytes.TrimSpace(text)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return false
	}
	return json.Valid(trimmed)
}

// answer is the agent's reply, or the explanation for why there is none.
//
// A failed turn carries no agent message at all, so returning only the text
// would report a rejected credential as an empty success. An unmet constraint
// on a turn codex considered fine has no explanation to borrow either — the
// stream simply never carried an answer — and a Result whose IsError is set
// beside an empty Text says a run failed without saying anything about it.
func (d *decoder) answer(unmet bool) string {
	if d.text != "" {
		return d.text
	}
	if d.isError {
		return "codex ended the turn: " + d.failure
	}
	if unmet {
		return "codex ended the turn without an answer in the required shape"
	}
	return ""
}

// clone copies the line, because the driver scans into a buffer it reuses for
// the next one — an Event holding the original would change under its reader.
func clone(line []byte) []byte {
	out := make([]byte, len(line))
	copy(out, line)
	return out
}
