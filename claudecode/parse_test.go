package claudecode

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	agentic "github.com/pedromvgomes/agentic-driver"
	"slices"
	"strings"
)

// golden reads a committed envelope. The files under testdata are raw output
// from the real CLI, which is what makes Parse a pure function tested against
// the thing it has to survive rather than against a fixture written to match
// it.
func golden(t *testing.T, name string) []byte {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	return raw
}

func testProvider(t *testing.T) *Provider {
	t.Helper()

	p, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func TestASuccessfulTurnCarriesTextSessionAndUsage(t *testing.T) {
	p := testProvider(t)

	got, err := p.Parse(golden(t, "success.json"), nil, 0)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if got.IsError {
		t.Error("IsError is set on a successful turn")
	}
	if got.Text != "ok" {
		t.Errorf("Text = %q, want %q", got.Text, "ok")
	}
	if got.SessionID == "" {
		t.Error("SessionID is empty, so the turn cannot be resumed")
	}
	if got.Turns != 1 {
		t.Errorf("Turns = %d, want 1", got.Turns)
	}
	if got.Usage.OutputTokens == 0 || got.Usage.CacheReadTokens == 0 {
		t.Errorf("Usage = %+v, want the envelope's token counts", got.Usage)
	}
	if got.Usage.CostUSD == 0 {
		t.Error("CostUSD is zero, but the envelope reports one")
	}
}

// A rejected credential is reported in the JSON body and exits non-zero. The
// verdict has to survive that exit: discarding stdout because the process
// failed turns "this token is no good" into "the provider is down", and sends
// the caller looking for an outage that is not happening.
func TestARejectedCredentialIsAVerdictNotAFailedRun(t *testing.T) {
	p := testProvider(t)

	got, err := p.Parse(golden(t, "rejected-auth.json"), nil, 1)
	if err != nil {
		t.Fatalf("Parse discarded a verdict that arrived with a non-zero exit: %v", err)
	}

	if !got.IsError {
		t.Fatal("IsError is not set for a 401")
	}
	if got.Text == "" {
		t.Error("Text is empty, so nothing says why the turn failed")
	}
}

// The envelope for a rejected token says subtype "success" alongside is_error
// true. Keying on the subtype reports the failure as a clean run.
func TestSubtypeIsNeverWhatDecidesSuccess(t *testing.T) {
	p := testProvider(t)

	var env envelope
	decode(t, golden(t, "rejected-auth.json"), &env)

	if env.Subtype != "success" {
		t.Skipf("the golden envelope does not carry the contradiction this test exists for (subtype=%q)", env.Subtype)
	}

	got, err := p.Parse(golden(t, "rejected-auth.json"), nil, 1)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !got.IsError {
		t.Error(`IsError is false for an envelope whose subtype is "success" and whose is_error is true`)
	}
}

// A turn stopped by its limit carries a null result, so keying only on the
// result field reports a failed run as an empty success.
func TestATurnStoppedByItsLimitSaysSo(t *testing.T) {
	p := testProvider(t)

	got, err := p.Parse(golden(t, "max-turns.json"), nil, 1)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if !got.IsError {
		t.Error("IsError is not set for a turn limit")
	}
	if got.Text == "" {
		t.Error("Text is empty, so a caller cannot tell a turn limit from an empty answer")
	}
	if got.Usage.OutputTokens == 0 {
		t.Error("Usage is empty, but the tokens up to the limit were still spent")
	}
}

// A usage error prints nothing on stdout and its explanation on stderr. Without
// carrying stderr into the error, the caller is told only that something exited
// non-zero.
func TestAUsageErrorIsExplainedFromStderr(t *testing.T) {
	p := testProvider(t)

	_, err := p.Parse(nil, golden(t, "usage-error.stderr"), 1)
	if err == nil {
		t.Fatal("Parse accepted empty stdout as a result")
	}
	if want := "unknown option"; !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q, want it to carry the CLI's own explanation (%q)", err, want)
	}
}

// Every field of the envelope is optional to a JSON decoder, so these documents
// all decode successfully into a zero value — and a zero value has IsError
// false. Positive evidence of a finished turn is what stops that reading as
// success.
func TestADocumentThatIsNotAResultIsNotSuccess(t *testing.T) {
	for _, stdout := range []string{`null`, `{}`, `[]`, `{"type":"error","error":"boom"}`} {
		t.Run(stdout, func(t *testing.T) {
			p := testProvider(t)

			got, err := p.Parse([]byte(stdout), nil, 0)
			if err == nil {
				t.Fatalf("Parse accepted %s as a finished turn: %+v", stdout, got)
			}
		})
	}
}

func TestParseIsIndifferentToTheExitCode(t *testing.T) {
	p := testProvider(t)
	raw := golden(t, "success.json")

	first, err := p.Parse(raw, nil, 0)
	if err != nil {
		t.Fatalf("Parse at exit 0: %v", err)
	}
	second, err := p.Parse(raw, nil, 137)
	if err != nil {
		t.Fatalf("Parse at exit 137: %v", err)
	}
	if first != second {
		t.Errorf("the same envelope parsed differently by exit code:\n 0 = %+v\n137 = %+v", first, second)
	}
}

func TestCommandRefusesAnEmptyPrompt(t *testing.T) {
	p := testProvider(t)

	_, err := p.StreamCommand(agentic.Request{})
	if !errors.Is(err, agentic.ErrInvalidRequest) {
		t.Errorf("error = %v, want ErrInvalidRequest", err)
	}
}

// The flag takes an EMPTY argument, and it must come first: a later positional
// could otherwise shadow it, and a settings file's apiKeyHelper outranks any
// injected credential while being invisible to the environment.
func TestEveryInvocationRefusesToLoadSettings(t *testing.T) {
	p := testProvider(t)

	for name, build := range map[string]func(agentic.Request) (agentic.Invocation, error){
		"StreamCommand": p.StreamCommand,
	} {
		t.Run(name, func(t *testing.T) {
			inv, err := build(agentic.Request{Prompt: "hi"})
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if len(inv.Args) < 2 || inv.Args[0] != "--setting-sources" || inv.Args[1] != "" {
				t.Errorf("argv starts %q, want --setting-sources with an empty argument first", inv.Args)
			}
		})
	}
}

func TestOptionalRequestFieldsAreFlagsOnlyWhenSet(t *testing.T) {
	p := testProvider(t)

	bare, err := p.StreamCommand(agentic.Request{Prompt: "hi"})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	for _, flag := range []string{"--model", "--max-turns", "--resume"} {
		if slices.Contains(bare.Args, flag) {
			t.Errorf("argv carries %s for a request that did not set it: %q", flag, bare.Args)
		}
	}

	full, err := p.StreamCommand(agentic.Request{Prompt: "hi", Model: "claude-opus-5", MaxTurns: 3, SessionID: "abc"})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	for _, want := range []string{"--model", "claude-opus-5", "--max-turns", "3", "--resume", "abc"} {
		if !slices.Contains(full.Args, want) {
			t.Errorf("argv is missing %q: %q", want, full.Args)
		}
	}
}

// Streaming and non-streaming runs are the same dialect with a different output
// format, so the flags a request asked for must appear in both.
func TestStreamCommandAsksForNewlineDelimitedEvents(t *testing.T) {
	p := testProvider(t)

	inv, err := p.StreamCommand(agentic.Request{Prompt: "hi", MaxTurns: 2})
	if err != nil {
		t.Fatalf("StreamCommand: %v", err)
	}
	for _, want := range []string{"stream-json", "--verbose", "--max-turns", "2"} {
		if !slices.Contains(inv.Args, want) {
			t.Errorf("argv is missing %q: %q", want, inv.Args)
		}
	}
}

func TestTheTokenIsCarriedByEnvironmentNotArgv(t *testing.T) {
	p := testProvider(t)

	env := p.AuthEnv("sk-ant-oat01-secret")
	if env["CLAUDE_CODE_OAUTH_TOKEN"] != "sk-ant-oat01-secret" {
		t.Errorf("AuthEnv = %v, want the token under CLAUDE_CODE_OAUTH_TOKEN", env)
	}

	inv, err := p.StreamCommand(agentic.Request{Prompt: "hi"})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	for _, arg := range inv.Args {
		if strings.Contains(arg, "sk-ant-") {
			t.Errorf("argv carries credential material, where every process on the machine can read it: %q", arg)
		}
	}
}

// Every variable that outranks CLAUDE_CODE_OAUTH_TOKEN has to be named, because
// this is the failure with no symptom: the run works, the answers come back
// correct, and the misroute shows up weeks later on an invoice.
func TestDenyEnvNamesEveryOverridingVariable(t *testing.T) {
	p := testProvider(t)
	denied := p.DenyEnv()

	for _, name := range []string{
		"ANTHROPIC_API_KEY",
		"ANTHROPIC_AUTH_TOKEN",
		"ANTHROPIC_BASE_URL",
		"ANTHROPIC_CUSTOM_HEADERS",
		"CLAUDE_CODE_USE_BEDROCK",
		"CLAUDE_CODE_USE_VERTEX",
		"AWS_BEARER_TOKEN_BEDROCK",
		"GOOGLE_APPLICATION_CREDENTIALS",
	} {
		if !slices.Contains(denied, name) {
			t.Errorf("DenyEnv does not name %s", name)
		}
	}
}
