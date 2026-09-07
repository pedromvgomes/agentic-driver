//go:build unix

package codex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What lands under the version root is EXECUTED, which is what makes adopting
// someone else's directory worse here than anywhere else in this package. A
// directory open to another account is somewhere a binary can be replaced
// between the rename that published it and the fork that runs it, and the
// digest checked on the way in says nothing about a file swapped afterwards.
//
// MkdirAll succeeds on a directory that already exists whatever its mode, so
// creating one proves nothing: the refusal has to be a check of its own.
func TestAVersionRootOpenToOtherUsersIsNotAdopted(t *testing.T) {
	body := wellFormed(t)
	pin(t, "9.9.9", integrity(body))
	reg := serve(t, "9.9.9", body)

	providers := t.TempDir()
	inst, err := NewInstaller(providers, WithBaseURL(reg.url))
	if err != nil {
		t.Skipf("this platform vendors no Codex build: %v", err)
	}

	// Created first, by something else, world-writable — the shape this refuses.
	if err := os.MkdirAll(filepath.Join(providers, ID), 0o777); err != nil {
		t.Fatalf("prepare the directory: %v", err)
	}

	_, err = inst.Install(t.Context(), "9.9.9")
	if err == nil {
		t.Fatal("Install adopted a directory open to other users")
	}
	if !strings.Contains(err.Error(), "open to other users") {
		t.Errorf("Install error = %v, want it to name the permissions", err)
	}
}

// A symlink standing where the version root should be sends every install
// somewhere this package never chose, so it is refused rather than followed.
func TestASymlinkStandingInForTheVersionRootIsRefused(t *testing.T) {
	body := wellFormed(t)
	pin(t, "9.9.9", integrity(body))
	reg := serve(t, "9.9.9", body)

	providers := t.TempDir()
	inst, err := NewInstaller(providers, WithBaseURL(reg.url))
	if err != nil {
		t.Skipf("this platform vendors no Codex build: %v", err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(providers, ID)); err != nil {
		t.Fatalf("prepare the symlink: %v", err)
	}

	if _, err := inst.Install(t.Context(), "9.9.9"); err == nil {
		t.Fatal("Install followed a symlink standing in for the version root")
	}
}

// The archive's group and other bits describe the machine that built it.
// Honouring them would open a tree this package has just promised is its own.
func TestExtractedFilesAdmitNobodyButTheOwner(t *testing.T) {
	body := wellFormed(t)
	pin(t, "9.9.9", integrity(body))
	inst := installer(t, serve(t, "9.9.9", body))

	result, err := inst.Install(t.Context(), "9.9.9")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	info, err := os.Stat(result.Path)
	if err != nil {
		t.Fatalf("stat the installed binary: %v", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("the installed binary is mode %#o, want nothing for group or other", perm)
	}
	if perm := info.Mode().Perm(); perm&0o100 == 0 {
		t.Errorf("the installed binary is mode %#o, want it executable by its owner", perm)
	}
}
