package agentic_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	agentic "github.com/pedromvgomes/agentic-driver"
	"github.com/pedromvgomes/agentic-driver/agentictest"
)

// stub is a provider with no dialect worth speaking of. It exists so the tests
// below are about the driver: the process, the environment and the contexts,
// which is everything the library owns.
type stub struct {
	args     []string
	env      map[string]string
	auth     map[string]string
	deny     []string
	parseErr error
}

func (s *stub) Descriptor() agentic.Descriptor {
	return agentic.Descriptor{ID: "stub", DisplayName: "Stub", Binary: "fake-agent"}
}

func (s *stub) StreamCommand(req agentic.Request) (agentic.Invocation, error) {
	if req.Prompt == "" {
		return agentic.Invocation{}, errors.New("stub: no prompt")
	}
	return agentic.Invocation{Args: append([]string{"--prompt", req.Prompt}, s.args...), Env: s.env}, nil
}

func (s *stub) NewDecoder() agentic.Decoder { return &stubDecoder{stub: s} }

// stubDecoder reads either shape a test needs: a line naming an event kind, or
// a bare Result document standing in for a run that only ever says one thing.
type stubDecoder struct {
	*stub
	result   agentic.Result
	complete bool
}

func (d *stubDecoder) Decode(line []byte) (agentic.Event, error) {
	if d.parseErr != nil {
		return agentic.Event{}, d.parseErr
	}

	var event struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(line, &event); err != nil {
		return agentic.Event{}, err
	}
	switch event.Kind {
	case "text":
		return agentic.Event{Kind: agentic.EventKindText, Text: event.Text}, nil
	case "result":
		d.result, d.complete = agentic.Result{Text: event.Text}, true
		return agentic.Event{}, nil
	case "":
		// Not an event line at all. A whole Result document is how a provider
		// with nothing to stream says everything it has to say.
		var result agentic.Result
		if err := json.Unmarshal(line, &result); err != nil {
			return agentic.Event{}, err
		}
		d.result, d.complete = result, true
		return agentic.Event{}, nil
	default:
		return agentic.Event{}, nil
	}
}

func (d *stubDecoder) Result() (agentic.Result, bool) { return d.result, d.complete }

// isolating is a stub that can also be handed a credential.
type isolating struct {
	stub
}

func (i *isolating) AuthEnv(token string) map[string]string {
	if i.auth != nil {
		return i.auth
	}
	return map[string]string{"STUB_TOKEN": token}
}

func (i *isolating) DenyEnv() []string { return i.deny }

func driver(t *testing.T, p agentic.Provider, f *agentictest.Fake, opts ...agentic.Option) *agentic.Driver {
	t.Helper()

	opts = append([]agentic.Option{agentic.WithBinary(f.Path())}, opts...)
	d, err := agentic.New(p, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d
}

const okEnvelope = `{"Text":"ok","SessionID":"s1","Turns":1}`

func TestRunReturnsWhatTheProviderParsed(t *testing.T) {
	fake := (&agentictest.Fake{Stdout: okEnvelope}).Build(t)
	d := driver(t, &stub{}, fake)

	got, err := d.Run(t.Context(), agentic.Request{Prompt: "hi"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Text != "ok" || got.SessionID != "s1" {
		t.Errorf("Result = %+v, want the parsed envelope", got)
	}
}

// A CLI reports a rejected credential or a refused request in its output and
// may still exit non-zero. Discarding stdout because the process failed turns a
// clear verdict into a spurious outage, and sends the caller hunting a problem
// that is not there.
func TestAVerdictSurvivesANonZeroExit(t *testing.T) {
	fake := (&agentictest.Fake{Stdout: `{"Text":"your token was rejected","IsError":true}`, ExitCode: 1}).Build(t)
	d := driver(t, &stub{}, fake)

	got, err := d.Run(t.Context(), agentic.Request{Prompt: "hi"})
	if err != nil {
		t.Fatalf("Run discarded a parseable verdict because the process exited 1: %v", err)
	}
	if !got.IsError || got.Text == "" {
		t.Errorf("Result = %+v, want the CLI's own verdict", got)
	}
}

// Output that cannot be parsed is not a verdict about anything, so it is the
// one case a non-zero exit becomes an error.
func TestUnreadableOutputIsAFailedRun(t *testing.T) {
	fake := (&agentictest.Fake{Stdout: "not json", Stderr: "boom", ExitCode: 2}).Build(t)
	d := driver(t, &stub{}, fake)

	_, err := d.Run(t.Context(), agentic.Request{Prompt: "hi"})
	if !errors.Is(err, agentic.ErrProviderUnavailable) {
		t.Fatalf("error = %v, want ErrProviderUnavailable", err)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error = %q, want it to carry the CLI's own stderr", err)
	}
}

// Reporting a caller that went away as a timeout sends people looking for a
// stall that never happened. Only keeping the parent context alongside the
// derived one can tell the two apart.
func TestACancelledCallerIsNotAHungCLI(t *testing.T) {
	fake := (&agentictest.Fake{Stdout: okEnvelope, SleepSeconds: 30}).Build(t)
	d := driver(t, &stub{}, fake, agentic.WithTimeout(30*time.Second))

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	_, err := d.Run(ctx, agentic.Request{Prompt: "hi"})
	if !errors.Is(err, agentic.ErrProviderUnavailable) {
		t.Fatalf("error = %v, want ErrProviderUnavailable", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %q, want it to name the caller's cancellation", err)
	}
	if strings.Contains(err.Error(), "did not finish within") {
		t.Errorf("error = %q, want it not to blame a timeout that did not happen", err)
	}
}

func TestATimeoutSaysHowLongItWaited(t *testing.T) {
	fake := (&agentictest.Fake{Stdout: okEnvelope, SleepSeconds: 30}).Build(t)
	d := driver(t, &stub{}, fake, agentic.WithTimeout(200*time.Millisecond))

	_, err := d.Run(t.Context(), agentic.Request{Prompt: "hi"})
	if !errors.Is(err, agentic.ErrProviderUnavailable) {
		t.Fatalf("error = %v, want ErrProviderUnavailable", err)
	}
	if !strings.Contains(err.Error(), "did not finish within") {
		t.Errorf("error = %q, want it to say the deadline passed", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Errorf("error = %q, want a timeout not to be reported as the caller leaving", err)
	}
}

func TestRequestTimeoutOverridesTheDriverDefault(t *testing.T) {
	fake := (&agentictest.Fake{Stdout: okEnvelope, SleepSeconds: 30}).Build(t)
	d := driver(t, &stub{}, fake, agentic.WithTimeout(time.Hour))

	start := time.Now()
	_, err := d.Run(t.Context(), agentic.Request{Prompt: "hi", Timeout: 200 * time.Millisecond})
	if err == nil {
		t.Fatal("Run ignored the request's own timeout")
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Errorf("Run waited %s, so the driver default was used instead of the request's", elapsed)
	}
}

// These CLIs spawn children of their own, and signalling only the process the
// driver started leaves them running. In a long-lived caller they accumulate
// for as long as it is up.
func TestATimeoutKillsTheWholeProcessGroup(t *testing.T) {
	fake := (&agentictest.Fake{Stdout: okEnvelope, SleepSeconds: 30, SpawnChild: true}).Build(t)
	d := driver(t, &stub{}, fake, agentic.WithTimeout(300*time.Millisecond))

	if _, err := d.Run(t.Context(), agentic.Request{Prompt: "hi"}); err == nil {
		t.Fatal("Run did not time out")
	}

	pid := fake.ChildPID(t)
	if pid == 0 {
		t.Fatal("the fake never recorded the child it spawned")
	}
	if alive(t, pid) {
		t.Errorf("the process the CLI spawned (pid %d) outlived the cancelled run", pid)
	}
}

// alive reports whether a process still exists, waiting a little for one that
// is on its way out.
//
// Signal 0 is the probe: the kernel checks the target exists and permissions
// allow signalling it, then delivers nothing. It must be a syscall.Signal —
// os.Process.Signal type-asserts, so an os.Signal that is not one returns
// "unsupported signal type" for a perfectly healthy process, which reads as
// "gone" to any caller that only checks for a non-nil error.
func alive(t *testing.T, pid int) bool {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for {
		proc, err := os.FindProcess(pid)
		if err != nil {
			return false
		}
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			return false
		}
		if time.Now().After(deadline) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestTheProviderBuildsTheArgv(t *testing.T) {
	fake := (&agentictest.Fake{Stdout: okEnvelope}).Build(t)
	d := driver(t, &stub{args: []string{"--flag", ""}}, fake)

	if _, err := d.Run(t.Context(), agentic.Request{Prompt: "hi"}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := fake.Recorded(t).Args
	want := []string{"--prompt", "hi", "--flag", ""}
	if len(got) != len(want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("argv = %q, want %q", got, want)
			break
		}
	}
}

func TestAProvidersRefusalStartsNoProcess(t *testing.T) {
	fake := (&agentictest.Fake{Stdout: okEnvelope}).Build(t)
	d := driver(t, &stub{}, fake)

	if _, err := d.Run(t.Context(), agentic.Request{}); err == nil {
		t.Fatal("Run accepted a request the provider refused")
	}
	if fake.Ran() {
		t.Error("a refused request still spawned a process")
	}
}

// Dropping the field would start a fresh session that answers perfectly well,
// with nothing in the reply saying the history was never read.
func TestAResumeIsRefusedByAProviderThatCannotDoIt(t *testing.T) {
	fake := (&agentictest.Fake{Stdout: okEnvelope}).Build(t)
	d := driver(t, &stub{}, fake)

	_, err := d.Run(t.Context(), agentic.Request{Prompt: "hi", SessionID: "s1"})
	if !errors.Is(err, agentic.ErrResumeUnsupported) {
		t.Fatalf("error = %v, want ErrResumeUnsupported", err)
	}
	if fake.Ran() {
		t.Error("a resume the provider cannot honour still spawned a process")
	}
}

// A run whose roster was dropped answers the prompt itself, competently and
// with none of the context the delegation existed to supply.
func TestARosterIsRefusedByAProviderThatCannotDefineAgents(t *testing.T) {
	fake := (&agentictest.Fake{Stdout: okEnvelope}).Build(t)
	d := driver(t, &stub{}, fake)

	_, err := d.Run(t.Context(), agentic.Request{
		Prompt: "hi",
		Agents: map[string]agentic.Agent{"curator": {Description: "curates", Prompt: "you curate"}},
	})
	if !errors.Is(err, agentic.ErrAgentsUnsupported) {
		t.Fatalf("error = %v, want ErrAgentsUnsupported", err)
	}
	if fake.Ran() {
		t.Error("a roster the provider cannot honour still spawned a process")
	}
}

// A dropped restriction fails in the dangerous direction: the run proceeds
// under the CLI's own defaults, which are wider than what was asked for.
func TestScriptedPermissionsAreRefusedByAProviderThatCannotApplyThem(t *testing.T) {
	for name, req := range map[string]agentic.Request{
		"allowed tools":   {Prompt: "hi", AllowedTools: []string{"Read"}},
		"permission mode": {Prompt: "hi", PermissionMode: "acceptEdits"},
	} {
		t.Run(name, func(t *testing.T) {
			fake := (&agentictest.Fake{Stdout: okEnvelope}).Build(t)
			d := driver(t, &stub{}, fake)

			_, err := d.Run(t.Context(), req)
			if !errors.Is(err, agentic.ErrPermissionsUnsupported) {
				t.Fatalf("error = %v, want ErrPermissionsUnsupported", err)
			}
			if fake.Ran() {
				t.Error("a grant the provider cannot honour still spawned a process")
			}
		})
	}
}

// An empty roster is the absence of a request, not a request a provider has to
// be able to honour — refusing it would make the field unusable to any caller
// that builds one conditionally.
func TestAnEmptyRosterAsksNothingOfTheProvider(t *testing.T) {
	fake := (&agentictest.Fake{Stdout: okEnvelope}).Build(t)
	d := driver(t, &stub{}, fake)

	if _, err := d.Run(t.Context(), agentic.Request{
		Prompt:       "hi",
		Agents:       map[string]agentic.Agent{},
		AllowedTools: []string{},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestInstallingIsRefusedByAProviderThatVendorsNothing(t *testing.T) {
	fake := (&agentictest.Fake{Stdout: okEnvelope}).Build(t)
	d := driver(t, &stub{}, fake)

	if _, err := d.Install(t.Context(), ""); !errors.Is(err, agentic.ErrInstallUnsupported) {
		t.Errorf("error = %v, want ErrInstallUnsupported", err)
	}
}

func TestIsolatedCredentialsAreRefusedAtConstruction(t *testing.T) {
	fake := (&agentictest.Fake{Stdout: okEnvelope}).Build(t)

	_, err := agentic.New(&stub{}, agentic.WithBinary(fake.Path()), agentic.WithCredentials(agentic.Isolated("t")))
	if !errors.Is(err, agentic.ErrIsolationUnsupported) {
		t.Errorf("error = %v, want ErrIsolationUnsupported", err)
	}
}

func TestTheDriverModelIsUsedWhenTheRequestNamesNone(t *testing.T) {
	fake := (&agentictest.Fake{Stdout: okEnvelope}).Build(t)
	d := driver(t, &stub{args: []string{"--model-was", "x"}}, fake, agentic.WithModel("chosen-model"))

	if _, err := d.Run(t.Context(), agentic.Request{Prompt: "hi"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if d.Model() != "chosen-model" {
		t.Errorf("Model() = %q, want the driver's default", d.Model())
	}
}

func TestARequestModelOverridesTheDriverDefault(t *testing.T) {
	fake := (&agentictest.Fake{Stdout: okEnvelope}).Build(t)
	p := &modelRecording{}
	d := driver(t, p, fake, agentic.WithModel("driver-model"))

	if _, err := d.Run(t.Context(), agentic.Request{Prompt: "hi", Model: "request-model"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if p.saw != "request-model" {
		t.Errorf("the provider saw model %q, want the request's own", p.saw)
	}
}

// The alias is resolved before the provider is asked to build a command, so
// every provider gets a concrete name without having to remember to resolve
// one itself.
func TestAnAliasIsResolvedBeforeTheProviderSeesIt(t *testing.T) {
	fake := (&agentictest.Fake{Stdout: okEnvelope}).Build(t)
	p := &modelRecordingResolver{}
	d := driver(t, p, fake, agentic.WithModel("family"))

	if _, err := d.Run(t.Context(), agentic.Request{Prompt: "hi"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if p.saw != "family-9" {
		t.Errorf("the provider saw model %q, want the alias resolved to a concrete name", p.saw)
	}
}

// A caller can ask what a name means without first asking whether the provider
// resolves anything.
func TestResolveModelPassesThroughForAProviderThatDoesNotResolve(t *testing.T) {
	fake := (&agentictest.Fake{Stdout: okEnvelope}).Build(t)
	d := driver(t, &stub{}, fake)

	if got := d.ResolveModel("family"); got != "family" {
		t.Errorf("ResolveModel = %q, want the name unchanged", got)
	}
}

// modelRecording captures the model the driver settled on.
type modelRecording struct {
	stub
	saw string
}

func (m *modelRecording) StreamCommand(req agentic.Request) (agentic.Invocation, error) {
	m.saw = req.Model
	return m.stub.StreamCommand(req)
}

type modelRecordingResolver struct {
	modelRecording
}

func (m *modelRecordingResolver) ResolveModel(name string) string {
	if name == "family" {
		return "family-9"
	}
	return name
}

// A caller displaying "active model" needs the concrete name, not the alias it
// was configured with — and needs to tell "no model chosen, the CLI decides"
// apart from a choice.
func TestModelReportsTheResolvedActiveModel(t *testing.T) {
	fake := (&agentictest.Fake{Stdout: okEnvelope}).Build(t)

	unset := driver(t, &modelRecordingResolver{}, fake)
	if got := unset.Model(); got != "" {
		t.Errorf("Model() = %q, want empty when no model has been chosen", got)
	}

	aliased := driver(t, &modelRecordingResolver{}, fake, agentic.WithModel("family"))
	if got := aliased.Model(); got != "family-9" {
		t.Errorf("Model() = %q, want the alias resolved to a concrete name", got)
	}

	concrete := driver(t, &modelRecordingResolver{}, fake, agentic.WithModel("family-9"))
	if got := concrete.Model(); got != "family-9" {
		t.Errorf("Model() = %q, want a concrete name unchanged", got)
	}
}

// A caller that cancelled — shutting down, or abandoning the request — must
// never be told the run succeeded, even when the CLI managed to flush a
// complete envelope first. Acting on a result the caller stopped waiting for is
// how a shutdown becomes a half-finished operation.
func TestACancelledCallerIsNeverToldItSucceeded(t *testing.T) {
	// The envelope is printed, then the process lingers so cancellation lands
	// after there is something perfectly parseable on stdout.
	fake := (&agentictest.Fake{Stdout: okEnvelope, SleepSeconds: 30}).Build(t)
	d := driver(t, &stub{}, fake, agentic.WithTimeout(30*time.Second))

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()

	_, err := d.Run(ctx, agentic.Request{Prompt: "hi"})
	if err == nil {
		t.Fatal("Run reported success to a caller that had cancelled")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %q, want it to name the caller's cancellation", err)
	}
}

// A child that writes without end decides how much memory the parent spends,
// right up until the deadline.
func TestOutputIsBoundedWhileTheChildRuns(t *testing.T) {
	fake := (&agentictest.Fake{Stdout: strings.Repeat("x", 200_000), Stderr: strings.Repeat("y", 200_000), ExitCode: 1}).Build(t)
	d := driver(t, &stub{}, fake)

	// The point is that this returns at all, with a bounded error, rather than
	// the buffers having grown to whatever the child chose to write.
	_, err := d.Run(t.Context(), agentic.Request{Prompt: "hi"})
	if err == nil {
		t.Fatal("Run accepted unparseable output")
	}
	if len(err.Error()) > 1_000 {
		t.Errorf("the error is %d bytes; output was not bounded", len(err.Error()))
	}
}

// "Configured" and "runnable" are different states — a provider that vendors
// its binary is constructed before that binary exists — and a caller needs to
// tell them apart without spawning a process to find out.
func TestReadyDistinguishesConfiguredFromRunnable(t *testing.T) {
	fake := (&agentictest.Fake{Stdout: okEnvelope}).Build(t)

	d := driver(t, &stub{}, fake)
	if err := d.Ready(); err != nil {
		t.Errorf("Ready = %v, want nil for an installed binary", err)
	}

	missing, err := agentic.New(&stub{}, agentic.WithBinary(filepath.Join(t.TempDir(), "absent")))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := missing.Ready(); !errors.Is(err, agentic.ErrProviderUnavailable) {
		t.Errorf("Ready = %v, want ErrProviderUnavailable for a missing binary", err)
	}
}

// A directory with the right name, a zero-byte file, and one without its
// execute bit all satisfy a stat, and none of them can be run.
func TestReadyRefusesWhatCannotActuallyBeExecuted(t *testing.T) {
	dir := t.TempDir()

	asDir := filepath.Join(dir, "as-dir")
	if err := os.MkdirAll(asDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0o700); err != nil {
		t.Fatalf("write: %v", err)
	}
	unexecutable := filepath.Join(dir, "unexecutable")
	if err := os.WriteFile(unexecutable, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	for name, path := range map[string]string{
		"a directory":    asDir,
		"an empty file":  empty,
		"no execute bit": unexecutable,
	} {
		t.Run(name, func(t *testing.T) {
			d, err := agentic.New(&stub{}, agentic.WithBinary(path))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if err := d.Ready(); err == nil {
				t.Errorf("Ready accepted %s", name)
			}
		})
	}
}

// "fork/exec …: no such file or directory" does not tell anyone what to do.
func TestAMissingBinarySaysWhatToDoAboutIt(t *testing.T) {
	d, err := agentic.New(&stub{}, agentic.WithBinary(filepath.Join(t.TempDir(), "absent")))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, runErr := d.Run(t.Context(), agentic.Request{Prompt: "hi"})
	if !errors.Is(runErr, agentic.ErrProviderUnavailable) {
		t.Fatalf("error = %v, want ErrProviderUnavailable", runErr)
	}
	if !strings.Contains(runErr.Error(), "not installed") {
		t.Errorf("error = %q, want it to say the binary is not installed", runErr)
	}
	// The stub vendors nothing, so pointing at Install would send the caller to
	// a method it does not have.
	if strings.Contains(runErr.Error(), "call Install") {
		t.Errorf("error = %q, offers Install for a provider that vendors nothing", runErr)
	}
}

// A bound the CLI cannot express is refused before a process starts, for the
// same reason a dropped roster or tool grant is: the failure is silent. A run
// whose cap went missing does not fail — it runs as long as it likes, and
// nothing in the answer says the limit was never applied.
func TestATurnLimitIsRefusedByAProviderThatCannotBoundTheLoop(t *testing.T) {
	fake := (&agentictest.Fake{Stdout: okEnvelope}).Build(t)
	d := driver(t, &stub{}, fake)

	_, err := d.Run(t.Context(), agentic.Request{Prompt: "hi", MaxTurns: 3})
	if !errors.Is(err, agentic.ErrTurnLimitUnsupported) {
		t.Errorf("error = %v, want ErrTurnLimitUnsupported", err)
	}
	if fake.Ran() {
		t.Error("the CLI was spawned for a bound it cannot honour")
	}
}

// The gate is the capability, not the field: a provider that can bound the loop
// is handed the request untouched.
func TestATurnLimitReachesAProviderThatCanBoundTheLoop(t *testing.T) {
	fake := (&agentictest.Fake{Stdout: okEnvelope}).Build(t)
	d := driver(t, &limiting{}, fake)

	if _, err := d.Run(t.Context(), agentic.Request{Prompt: "hi", MaxTurns: 3}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !slices.Contains(fake.Recorded(t).Args, "--max-turns") {
		t.Errorf("argv = %q, want the bound the provider spelled", fake.Recorded(t).Args)
	}
}

// limiting is a stub that can bound the agent loop.
type limiting struct {
	stub
}

func (l *limiting) TurnLimitArgs(maxTurns int) ([]string, error) {
	return []string{"--max-turns", strconv.Itoa(maxTurns)}, nil
}

func (l *limiting) StreamCommand(req agentic.Request) (agentic.Invocation, error) {
	inv, err := l.stub.StreamCommand(req)
	if err != nil {
		return agentic.Invocation{}, err
	}
	if req.MaxTurns > 0 {
		args, err := l.TurnLimitArgs(req.MaxTurns)
		if err != nil {
			return agentic.Invocation{}, err
		}
		inv.Args = append(inv.Args, args...)
	}
	return inv, nil
}
