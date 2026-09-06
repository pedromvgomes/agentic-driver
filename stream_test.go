package agentic_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	agentic "github.com/pedromvgomes/agentic-driver"
	"github.com/pedromvgomes/agentic-driver/agentictest"
)

func collect(t *testing.T, d *agentic.Driver, req agentic.Request) ([]agentic.Event, error) {
	t.Helper()

	seq, err := d.Stream(t.Context(), req)
	if err != nil {
		return nil, err
	}

	var events []agentic.Event
	for event, err := range seq {
		if err != nil {
			return events, err
		}
		events = append(events, event)
	}
	return events, nil
}

const eventLines = `{"kind":"noise"}
{"kind":"text","text":"one"}
{"kind":"text","text":"two"}
{"kind":"result","text":"done"}
`

func TestStreamYieldsEveryModelledEvent(t *testing.T) {
	fake := (&agentictest.Fake{Stdout: eventLines}).Build(t)
	d := driver(t, &stub{}, fake)

	events, err := collect(t, d, agentic.Request{Prompt: "hi"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events, want the two text events and the result", len(events))
	}
	if events[0].Text != "one" || events[1].Text != "two" {
		t.Errorf("text events = %q, %q", events[0].Text, events[1].Text)
	}
	if events[2].Kind != agentic.EventKindResult || events[2].Result.Text != "done" {
		t.Errorf("last event = %+v, want the terminal result", events[2])
	}
}

// A CLI adding an event type is not a reason to fail a run that is otherwise
// working, so an unmodelled line is skipped rather than yielded as an error.
func TestAnUnmodelledLineDoesNotEndTheStream(t *testing.T) {
	fake := (&agentictest.Fake{Stdout: eventLines}).Build(t)
	d := driver(t, &stub{}, fake)

	events, err := collect(t, d, agentic.Request{Prompt: "hi"})
	if err != nil {
		t.Fatalf("the noise line ended the stream: %v", err)
	}
	for _, event := range events {
		if event.Kind == agentic.EventKindUnknown {
			t.Error("an unmodelled event was yielded to the caller")
		}
	}
}

// Run and Stream are the same invocation. A provider with one argv builder per
// mode would have two dialects to keep in step, and the day they drift is the
// day the same Request is answered two different ways.
func TestRunAndStreamIssueTheSameArgv(t *testing.T) {
	req := agentic.Request{Prompt: "hi"}

	streamed := (&agentictest.Fake{Stdout: eventLines}).Build(t)
	if _, err := collect(t, driver(t, &stub{}, streamed), req); err != nil {
		t.Fatalf("Stream: %v", err)
	}

	ran := (&agentictest.Fake{Stdout: eventLines}).Build(t)
	if _, err := driver(t, &stub{}, ran).Run(t.Context(), req); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !slices.Equal(streamed.Recorded(t).Args, ran.Recorded(t).Args) {
		t.Errorf("Run and Stream built different commands:\nstream = %q\nrun    = %q",
			streamed.Recorded(t).Args, ran.Recorded(t).Args)
	}
}

// A stream that stopped before its provider said how the turn ended made no
// statement about the request, so there is no verdict to report and the exit
// code is all that is left to go on.
func TestAStreamThatEndsWithoutAResultIsAFailedRun(t *testing.T) {
	fake := (&agentictest.Fake{Stdout: `{"kind":"text","text":"one"}` + "\n", Stderr: "boom", ExitCode: 3}).Build(t)
	d := driver(t, &stub{}, fake)

	events, err := collect(t, d, agentic.Request{Prompt: "hi"})
	if !errors.Is(err, agentic.ErrProviderUnavailable) {
		t.Fatalf("error = %v, want ErrProviderUnavailable", err)
	}
	if len(events) != 1 {
		t.Errorf("got %d events, want the ones that arrived before the failure to still be delivered", len(events))
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error = %q, want it to carry the CLI's own stderr", err)
	}
}

func TestAStreamTimeoutSaysHowLongItWaited(t *testing.T) {
	fake := (&agentictest.Fake{Stdout: eventLines, SleepSeconds: 30}).Build(t)
	d := driver(t, &stub{}, fake, agentic.WithTimeout(300*time.Millisecond))

	_, err := collect(t, d, agentic.Request{Prompt: "hi"})
	if !errors.Is(err, agentic.ErrProviderUnavailable) {
		t.Fatalf("error = %v, want ErrProviderUnavailable", err)
	}
	if !strings.Contains(err.Error(), "did not finish within") {
		t.Errorf("error = %q, want it to say the deadline passed", err)
	}
}

// A caller that stops reading has to leave nothing running behind it.
//
// The child is still working when the caller walks away, and these CLIs spawn
// children of their own — so the check is that the GRANDCHILD is gone, not that
// the process the driver started is. Signalling only the latter is exactly the
// bug the process group exists to prevent, and it leaves sleepers accumulating
// for the life of the caller.
func TestAbandoningAStreamKillsTheChild(t *testing.T) {
	fake := (&agentictest.Fake{Stdout: eventLines, LingerSeconds: 60, SpawnChild: true}).Build(t)
	d := driver(t, &stub{}, fake, agentic.WithTimeout(5*time.Minute))

	seq, err := d.Stream(t.Context(), agentic.Request{Prompt: "hi"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var seen int
	start := time.Now()
	for range seq {
		// One event is enough. Abandoning the sequence here is the behaviour
		// under test.
		seen++
		break
	}
	abandoned := time.Since(start)

	if seen == 0 {
		t.Fatal("the stream yielded nothing, so nothing was abandoned")
	}
	// Walking away has to return, not block until the child finishes on its
	// own. Reaping before cancelling would make abandoning a stream wait out
	// however long the CLI had left to run.
	if abandoned > 20*time.Second {
		t.Errorf("abandoning the stream took %s; the caller was made to wait for the child", abandoned)
	}

	pid := fake.ChildPID(t)
	if pid == 0 {
		t.Fatal("the fake never recorded the child it spawned")
	}
	if alive(t, pid) {
		t.Errorf("the process the CLI spawned (pid %d) outlived the abandoned stream", pid)
	}
}

// The rule the fold depends on. A CLI reports a rejected credential or an
// unsupported model by finishing its stream properly and THEN exiting non-zero.
// Judging the exit code first turns every one of those verdicts into a spurious
// outage, which is the distinction the library exists to keep.
func TestATerminalResultOutranksANonZeroExit(t *testing.T) {
	fake := (&agentictest.Fake{
		Stdout:   `{"kind":"result","text":"your token was rejected"}` + "\n",
		Stderr:   "401 unauthorized",
		ExitCode: 1,
	}).Build(t)

	result, err := driver(t, &stub{}, fake).Run(t.Context(), agentic.Request{Prompt: "hi"})
	if err != nil {
		t.Fatalf("a completed turn that exited non-zero was reported as an outage: %v", err)
	}
	if result.Text != "your token was rejected" {
		t.Errorf("Text = %q, want the verdict the CLI reported", result.Text)
	}
}

// Same rule, one step further: a CLI that flushes its last event and then hangs
// holding stdout open has stalled in its teardown, not in the work. The answer
// arrived and was paid for.
func TestATerminalResultOutranksATimeout(t *testing.T) {
	fake := (&agentictest.Fake{
		Stdout:        `{"kind":"result","text":"done"}` + "\n",
		LingerSeconds: 30,
	}).Build(t)

	d := driver(t, &stub{}, fake, agentic.WithTimeout(300*time.Millisecond))
	result, err := d.Run(t.Context(), agentic.Request{Prompt: "hi"})
	if err != nil {
		t.Fatalf("a flushed result was discarded because the child lingered: %v", err)
	}
	if result.Text != "done" {
		t.Errorf("Text = %q, want the answer that arrived before the stall", result.Text)
	}
}
