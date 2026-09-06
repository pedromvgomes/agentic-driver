package claudecode

import (
	"encoding/json"
	"fmt"
	"strings"

	agentic "github.com/pedromvgomes/agentic-driver"
)

// envelope is the part of `claude -p … --output-format json` this package
// reads. Every other field of a real envelope is deliberately left undecoded:
// modelling them would make each release's additions a breaking change here.
//
// subtype is captured only to be reported. It must never be the thing keyed on:
// a rejected token returns subtype "success" ALONGSIDE is_error true and
// api_error_status 401.
type envelope struct {
	Type           string `json:"type"`
	IsError        bool   `json:"is_error"`
	APIErrorStatus int    `json:"api_error_status"`
	Subtype        string `json:"subtype"`
	Result         string `json:"result"`
	SessionID      string `json:"session_id"`
	// StructuredOutput is the answer to a run that carried --json-schema. It is
	// ABSENT, not null and not empty, on a run that gave up on the shape — and
	// that run reports is_error false with subtype "success", so its presence
	// is the only thing that says the constraint held.
	StructuredOutput json.RawMessage `json:"structured_output"`
	NumTurns         int             `json:"num_turns"`
	TotalCostUSD     float64         `json:"total_cost_usd"`
	Usage            struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
	ModelUsage map[string]struct {
		CanonicalModel           string `json:"canonicalModel"`
		InputTokens              int    `json:"inputTokens"`
		CacheReadInputTokens     int    `json:"cacheReadInputTokens"`
		CacheCreationInputTokens int    `json:"cacheCreationInputTokens"`
	} `json:"modelUsage"`
}

// completed reports whether this document is a finished turn at all.
//
// Positive evidence is required, not merely the absence of is_error. Every
// field above is optional to a JSON decoder, so `null`, `{}` and a document
// like {"type":"error", …} all decode successfully into a zero value — and a
// zero value has IsError false. Reading that as success reports a run that
// produced no answer whatsoever as a clean one.
func (e envelope) completed() bool {
	return e.Type == "result" || e.Subtype != "" || e.SessionID != ""
}

// model is the model that answered the turn.
//
// The envelope has no field for it: modelUsage is keyed by every model the run
// touched, and a turn on one model still bills a little Haiku for the CLI's own
// housekeeping. The main model is the one that read the conversation — its
// input plus cache is the whole context, while an auxiliary call carries a few
// hundred tokens and no cache at all.
//
// The canonical name is preferred over the key, which carries a context-window
// suffix ("claude-opus-5[1m]") that is a billing detail rather than a model.
func (e envelope) model() string {
	// The winning KEY is tracked, not the name it projects to. Comparing a
	// candidate key against an already-projected canonical name would make the
	// tie-break depend on Go's randomised map iteration — the very thing it is
	// here to remove.
	var winner string
	most := -1

	for key, usage := range e.ModelUsage {
		total := usage.InputTokens + usage.CacheReadInputTokens + usage.CacheCreationInputTokens
		if total > most || (total == most && key < winner) {
			most, winner = total, key
		}
	}
	if winner == "" {
		return ""
	}

	// Starting from -1 rather than 0 means an envelope whose entries all report
	// zero input still names the model that produced it, instead of answering
	// as though no model had been involved.
	if canonical := e.ModelUsage[winner].CanonicalModel; canonical != "" {
		return canonical
	}
	return winner
}

// Parse turns one finished invocation into a Result.
//
// The exit code is not consulted. Claude Code exits non-zero for a rejected
// credential and for a turn limit alike, and in both cases the envelope on
// stdout is the more precise answer — the code adds nothing the body does not
// already say. An exit code without a readable envelope is a failure of the run
// rather than a verdict about it, and that is the one case this returns an
// error for.
func (p *dialect) Parse(stdout, stderr []byte, code int) (agentic.Result, error) {
	var env envelope
	if err := json.Unmarshal(stdout, &env); err != nil || !env.completed() {
		return agentic.Result{}, unreadable(stdout, stderr, code)
	}

	return agentic.Result{
		Text:       p.text(env),
		Structured: env.StructuredOutput,
		SessionID:  env.SessionID,
		Model:      env.model(),
		Turns:      env.NumTurns,
		IsError:    env.IsError,
		Usage: agentic.Usage{
			InputTokens:         env.Usage.InputTokens,
			OutputTokens:        env.Usage.OutputTokens,
			CacheReadTokens:     env.Usage.CacheReadInputTokens,
			CacheCreationTokens: env.Usage.CacheCreationInputTokens,
			CostUSD:             env.TotalCostUSD,
		},
	}, nil
}

// text is the agent's answer, or the explanation for why there is none.
//
// A turn stopped by its limit carries a null result, so keying only on the
// result field would report a failed run as an empty success. The subtype is
// the only thing that says what happened, which is why it is rendered here even
// though it is never used to decide whether the run failed.
func (p *dialect) text(env envelope) string {
	if env.Result != "" {
		return env.Result
	}
	if env.IsError && env.Subtype != "" {
		return "claude ended the turn: " + env.Subtype
	}
	return ""
}

// unreadable explains a run whose stdout is not an envelope.
//
// Everything it can observe means the same thing: the invocation could not be
// understood, so nothing here is a statement about the request. stderr carries
// the CLI's own explanation and is what makes a usage error diagnosable —
// `claude` prints "error: unknown option '--x'" there and nothing at all on
// stdout.
func unreadable(stdout, stderr []byte, code int) error {
	if msg := strings.TrimSpace(string(stderr)); msg != "" {
		return fmt.Errorf("exited %d without an envelope: %s", code, msg)
	}
	if len(strings.TrimSpace(string(stdout))) == 0 {
		return fmt.Errorf("exited %d and printed nothing", code)
	}
	return fmt.Errorf("exited %d and printed output that is not a result envelope", code)
}

// streamEvent is the part of one stream-json line this package reads.
//
// A tool result's content is a bare string on a Read but a list of blocks on
// tools that return structured output, so it is decoded lazily rather than
// given a type that only one shape fits.
type streamEvent struct {
	Type    string `json:"type"`
	Message struct {
		Content []struct {
			Type    string          `json:"type"`
			Text    string          `json:"text"`
			Name    string          `json:"name"`
			Content json.RawMessage `json:"content"`
		} `json:"content"`
	} `json:"message"`
}

// NewDecoder returns a decoder for one run.
func (p *dialect) NewDecoder(req agentic.Request) agentic.Decoder {
	return &decoder{dialect: p, schema: len(req.Schema) > 0}
}

// decoder folds one run of `--output-format stream-json --verbose`.
//
// Claude Code puts everything a Result needs on the single terminal line, so
// the only state this carries is that line's verdict. It is a decoder rather
// than a function of the last line anyway, because the interface has to serve
// a CLI that spreads a result across a whole stream.
type decoder struct {
	*dialect
	result   agentic.Result
	complete bool
	// schema records that the run was required to answer in a shape. Parse
	// reports what the envelope said; only the run knows what it was asked for.
	schema bool
}

// Decode decodes one line of the stream.
//
// A line whose type this package does not model returns the zero Event, which
// the driver skips. Claude Code emits bookkeeping events — rate-limit notices,
// an init banner — that a caller consuming text has no use for, and a release
// adding another one is not a reason to fail a run that is working.
func (d *decoder) Decode(line []byte) (agentic.Event, error) {
	var ev streamEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		return agentic.Event{}, err
	}

	switch ev.Type {
	case "assistant":
		return d.assistantEvent(ev, line), nil

	case "user":
		// A tool's output comes back on a USER line, not an assistant one:
		// the transcript models a tool as something that answers the agent.
		// Reading tool results off assistant lines finds none, and a caller
		// watching the agent work never sees what its tools returned.
		return d.userEvent(ev, line), nil

	case "result":
		// The terminal line of a stream is the same document a non-streaming
		// run prints, so it is read by the same function rather than a second
		// implementation that could drift from it.
		result, err := d.Parse(line, nil, 0)
		if err != nil {
			return agentic.Event{}, err
		}
		if d.schema && len(result.Structured) == 0 {
			// The CLI calls this a success: exit 0, is_error false, subtype
			// "success", and a result field holding the agent's prose account
			// of why it could not satisfy the schema. It is not a success to a
			// caller that asked for JSON, and nothing else in the envelope
			// marks it, so the verdict is corrected here.
			result.IsError = true
		}
		d.result, d.complete = result, true
		// Recorded, not yielded. The driver builds the terminal event from
		// Result, so a stream cannot announce one outcome and its fold another.
		return agentic.Event{}, nil

	default:
		return agentic.Event{}, nil
	}
}

// Result is the verdict the terminal line carried.
func (d *decoder) Result() (agentic.Result, bool) { return d.result, d.complete }

// assistantEvent renders one assistant turn.
//
// A turn's content is a list of blocks, and only the first meaningful one
// becomes the Event: a caller wanting the rest reads Raw, which is why the
// undecoded line is carried. Thinking blocks match nothing here and are
// skipped — they are the agent's reasoning, not its output.
func (p *dialect) assistantEvent(ev streamEvent, line []byte) agentic.Event {
	for _, block := range ev.Message.Content {
		switch block.Type {
		case "text":
			if block.Text != "" {
				return agentic.Event{Kind: agentic.EventKindText, Text: block.Text, Raw: clone(line)}
			}
		case "tool_use":
			return agentic.Event{Kind: agentic.EventKindToolUse, Text: block.Name, Raw: clone(line)}
		}
	}
	return agentic.Event{}
}

// userEvent renders a tool answering the agent.
func (p *dialect) userEvent(ev streamEvent, line []byte) agentic.Event {
	for _, block := range ev.Message.Content {
		if block.Type == "tool_result" {
			return agentic.Event{
				Kind: agentic.EventKindToolResult,
				Text: toolResultText(block.Content),
				Raw:  clone(line),
			}
		}
	}
	return agentic.Event{}
}

// toolResultText renders a tool's output as text.
//
// The field is a bare string for most tools and a list of content blocks for
// those returning structured output. A caller that only wants to display
// progress should not have to know which, and one that needs the structure
// reads Raw.
func toolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}

	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}

	var parts []string
	for _, block := range blocks {
		if block.Type == "text" && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// clone copies the line, because the driver scans into a buffer it reuses for
// the next one — an Event holding the original would change under its reader.
func clone(line []byte) []byte {
	out := make([]byte, len(line))
	copy(out, line)
	return out
}
