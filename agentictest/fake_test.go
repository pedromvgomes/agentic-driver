package agentictest

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestFakeCapturesStdin(t *testing.T) {
	fake := (&Fake{Stdout: "ok"}).Build(t)

	const prompt = "line one\narg:not an argument\nENV\nmulti-line prompt"
	cmd := exec.Command(fake.Path(), "--flag", "value")
	cmd.Stdin = strings.NewReader(prompt)
	if err := cmd.Run(); err != nil {
		t.Fatalf("run the fake agent: %v", err)
	}

	if got := fake.Stdin(t); got != prompt {
		t.Errorf("Stdin() = %q, want %q", got, prompt)
	}

	inv := fake.Recorded(t)
	if want := []string{"--flag", "value"}; !slices.Equal(inv.Args, want) {
		t.Errorf("Recorded().Args = %v, want %v; stdin containing arg:/ENV lines must not leak into it", inv.Args, want)
	}
	if len(inv.Env) == 0 {
		t.Error("Recorded().Env is empty; stdin capture must not have consumed the ENV block")
	}
}

// The stdin is a pipe whose write end stays open, so a fake that read it would
// block waiting for an EOF that never comes. Finishing at all is the proof it
// never read.
func TestFakeIgnoringStdinNeverReadsIt(t *testing.T) {
	fake := (&Fake{Stdout: "ok", IgnoreStdin: true}).Build(t)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer func() { _ = w.Close() }()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, fake.Path())
	cmd.Stdin = r
	err = cmd.Run()
	_ = r.Close()
	if err != nil {
		t.Fatalf("run the fake agent: %v (a fake that waits on stdin is killed by the deadline)", err)
	}

	if _, err := os.Stat(fake.stdinPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stdin capture exists (stat error %v), want none from a fake that ignores stdin", err)
	}
}

func TestFakeStdinEmptyWhenNothingPiped(t *testing.T) {
	fake := (&Fake{Stdout: "ok"}).Build(t)

	cmd := exec.Command(fake.Path())
	if err := cmd.Run(); err != nil {
		t.Fatalf("run the fake agent: %v", err)
	}

	if got := fake.Stdin(t); got != "" {
		t.Errorf("Stdin() = %q, want empty when nothing was piped", got)
	}
}
