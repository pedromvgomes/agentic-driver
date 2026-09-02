package agentic_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	agentic "github.com/pedromvgomes/agentic-driver"
	"github.com/pedromvgomes/agentic-driver/agentictest"
)

// streaming is a stub whose events are one JSON object per line.
type streaming struct {
	stub
}

func (s *streaming) StreamCommand(req agentic.Request) (agentic.Invocation, error) {
	inv, err := s.stub.Command(req)
	if err != nil {
		return agentic.Invocation{}, err
	}
	inv.Args = append(inv.Args, "--stream")
	return inv, nil
}

func (s *streaming) ParseEvent(line []byte) (agentic.Event, error) {
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
		return agentic.Event{Kind: agentic.EventKindResult, Result: agentic.Result{Text: event.Text}}, nil
	default:
		return agentic.Event{}, nil
	}
}

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
	d := driver(t, &streaming{}, fake)

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
	d := driver(t, &streaming{}, fake)

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

func TestStreamUsesTheStreamingDialect(t *testing.T) {
	fake := (&agentictest.Fake{Stdout: eventLines}).Build(t)
	d := driver(t, &streaming{}, fake)

	if _, err := collect(t, d, agentic.Request{Prompt: "hi"}); err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var sawStreamFlag bool
	for _, arg := range fake.Recorded(t).Args {
		if arg == "--stream" {
			sawStreamFlag = true
		}
	}
	if !sawStreamFlag {
		t.Errorf("argv = %q, want StreamCommand's own flags", fake.Recorded(t).Args)
	}
}

// There is no envelope left to parse once a stream has ended, so a non-zero
// exit is a failed run rather than a verdict something downstream could read.
func TestAStreamThatExitsNonZeroIsAFailedRun(t *testing.T) {
	fake := (&agentictest.Fake{Stdout: `{"kind":"text","text":"one"}` + "\n", Stderr: "boom", ExitCode: 3}).Build(t)
	d := driver(t, &streaming{}, fake)

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
	d := driver(t, &streaming{}, fake, agentic.WithTimeout(300*time.Millisecond))

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
	d := driver(t, &streaming{}, fake, agentic.WithTimeout(5*time.Minute))

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
