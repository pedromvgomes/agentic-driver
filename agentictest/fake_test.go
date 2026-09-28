package agentictest

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
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
