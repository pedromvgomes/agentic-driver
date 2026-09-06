package claudecode

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	agentic "github.com/pedromvgomes/agentic-driver"
)

// The developer-machine path: I have claude on PATH, use it. This is what
// ambient credentials exist for, and it needs a provider that resolves the same
// way — a vendoring provider resolves to a pinned path a developer machine has
// no reason to contain, and reports it only when Run fails to exec.
func TestAProviderOnPathFindsTheBinaryTheDeveloperAlreadyHas(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, BinaryName)
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("PATH", dir)

	p, err := NewOnPath()
	if err != nil {
		t.Fatalf("NewOnPath: %v", err)
	}
	d, err := agentic.New(p)
	if err != nil {
		t.Fatalf("agentic.New: %v", err)
	}

	if d.Binary() != stub {
		t.Errorf("Binary() = %q, want the claude on PATH at %q", d.Binary(), stub)
	}
	if err := d.Ready(); err != nil {
		t.Errorf("Ready = %v, want nil for a claude that is on PATH", err)
	}
}

// A provider that runs someone else's binary cannot claim the guarantee that
// comes with installing one, and the driver reads that from the type rather
// than taking anyone's word for it.
func TestAProviderOnPathOffersNoInstaller(t *testing.T) {
	p, err := NewOnPath()
	if err != nil {
		t.Fatalf("NewOnPath: %v", err)
	}

	if _, ok := any(p).(agentic.Installer); ok {
		t.Error("PathProvider implements Installer, claiming to have verified a build it merely found")
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, BinaryName), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("PATH", dir)

	d, err := agentic.New(p)
	if err != nil {
		t.Fatalf("agentic.New: %v", err)
	}
	if _, err := d.Install(t.Context(), ""); !errors.Is(err, agentic.ErrInstallUnsupported) {
		t.Errorf("Install error = %v, want ErrInstallUnsupported", err)
	}
}

// A caller pinning a version is asking for a specific build. Answering that
// with "whatever is on PATH" is the silent substitution the pin exists to
// prevent.
func TestAPinnedVersionIsRefusedRatherThanIgnoredOnPath(t *testing.T) {
	if _, err := NewOnPath(WithVersion("2.1.258")); err == nil {
		t.Error("NewOnPath accepted a pinned version it cannot honour")
	}
}

// Both providers speak the same dialect; only the capability differs.
func TestBothProvidersSpeakTheSameDialect(t *testing.T) {
	vendored, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	onPath, err := NewOnPath()
	if err != nil {
		t.Fatalf("NewOnPath: %v", err)
	}

	req := agentic.Request{Prompt: "hi", Model: "opus", MaxTurns: 2}

	a, err := vendored.StreamCommand(req)
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	b, err := onPath.StreamCommand(req)
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if len(a.Args) != len(b.Args) {
		t.Fatalf("argv differs between providers:\nvendored = %q\non path  = %q", a.Args, b.Args)
	}
	for i := range a.Args {
		if a.Args[i] != b.Args[i] {
			t.Errorf("argv differs at %d: %q vs %q", i, a.Args[i], b.Args[i])
		}
	}
	if vendored.ResolveModel("opus") != onPath.ResolveModel("opus") {
		t.Error("the two providers resolve model aliases differently")
	}
	if vendored.Descriptor().ID != onPath.Descriptor().ID {
		t.Error("the two providers report different IDs for the same CLI")
	}
}
