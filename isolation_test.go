package agentic_test

import (
	"strings"
	"testing"

	agentic "github.com/pedromvgomes/agentic-driver"
	"github.com/pedromvgomes/agentic-driver/agentictest"
	"github.com/pedromvgomes/agentic-driver/claudecode"
	"github.com/pedromvgomes/agentic-driver/codex"
)

// isolators is every provider that can be handed a credential. A new one
// belongs here: the property below is not something a provider can be trusted
// to get right on its own, because getting it wrong produces a plain
// authentication error miles from its cause.
func isolators(t *testing.T) map[string]agentic.Isolator {
	t.Helper()

	claude, err := claudecode.New(t.TempDir())
	if err != nil {
		t.Fatalf("claudecode.New: %v", err)
	}
	return map[string]agentic.Isolator{
		claudecode.ID: claude,
		codex.ID:      codex.New(),
	}
}

// A variable that carries a credential is by definition one that can REDIRECT
// that credential when it arrives from anywhere else, so a provider naming its
// own auth variable in DenyEnv is correct rather than a mistake. What must hold
// is that the driver's own injection survives the scrub anyway.
func TestTheInjectedCredentialSurvivesEveryProvidersDenyList(t *testing.T) {
	const token = "the-token"

	for id, iso := range isolators(t) {
		t.Run(id, func(t *testing.T) {
			fake := (&agentictest.Fake{Stdout: okEnvelope}).Build(t)
			d, err := agentic.New(passthrough{iso},
				agentic.WithBinary(fake.Path()),
				agentic.WithCredentials(agentic.Isolated(token)),
				agentic.WithHome(t.TempDir()))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if _, err := d.Run(t.Context(), agentic.Request{Prompt: "hi"}); err != nil {
				t.Fatalf("Run: %v", err)
			}

			env := fake.Recorded(t).Env
			for name, want := range iso.AuthEnv(token) {
				if got := env[name]; got != want {
					t.Errorf("%s = %q in the child, want %q — the credential did not survive isolation", name, got, want)
				}
			}
		})
	}
}

// A deny list that names nothing cannot be a backstop against anything.
func TestEveryProviderNamesTheVariablesThatCanRedirectIt(t *testing.T) {
	for id, iso := range isolators(t) {
		t.Run(id, func(t *testing.T) {
			if len(iso.DenyEnv()) == 0 {
				t.Error("DenyEnv is empty, so nothing backstops a later pass-through")
			}
			for _, name := range iso.DenyEnv() {
				if strings.TrimSpace(name) != name || name == "" {
					t.Errorf("DenyEnv contains %q, which no environment variable is named", name)
				}
			}
		})
	}
}

// passthrough gives a real provider's Isolator the minimal Provider surface the
// driver needs, so the property above is tested against the actual deny lists
// rather than a stand-in.
type passthrough struct {
	agentic.Isolator
}

func (passthrough) Descriptor() agentic.Descriptor {
	return agentic.Descriptor{ID: "under-test", DisplayName: "Under test", Binary: "fake-agent"}
}

func (passthrough) StreamCommand(req agentic.Request) (agentic.Invocation, error) {
	return agentic.Invocation{Args: []string{"--prompt", req.Prompt}}, nil
}

func (passthrough) NewDecoder(agentic.Request) agentic.Decoder { return &passthroughDecoder{} }

// passthroughDecoder treats every line as the answer, which is enough for tests
// about the environment a child is given rather than what it said.
type passthroughDecoder struct {
	text string
	seen bool
}

func (d *passthroughDecoder) Decode(line []byte) (agentic.Event, error) {
	d.text, d.seen = string(line), true
	return agentic.Event{Kind: agentic.EventKindText, Text: d.text}, nil
}

func (d *passthroughDecoder) Result() (agentic.Result, bool) {
	return agentic.Result{Text: d.text}, d.seen
}
