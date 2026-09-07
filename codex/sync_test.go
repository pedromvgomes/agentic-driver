//go:build unix

package codex

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// syncTree reaches every directory in the tree, not only its root.
//
// The root's entry names `bin`; the entry inside `bin` naming the executable
// lives in a directory of its own, and flushing only the root leaves that inner
// name unflushed — a publishing rename that reaches the disk ahead of the
// binary it is supposed to be publishing.
//
// A directory that cannot be opened is what makes the difference observable:
// syncing the root alone succeeds and never touches it, so a failure here is
// proof the walk descended.
func TestSyncTreeFlushesEveryDirectoryInTheTree(t *testing.T) {
	if os.Getuid() == 0 { //nolint:forbidigo // the permission check below is meaningless as root
		t.Skip("root ignores the permission bits this test relies on")
	}

	root := t.TempDir()
	nested := filepath.Join(root, "bin")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatalf("prepare the tree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nested, BinaryName), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatalf("prepare the tree: %v", err)
	}

	// Syncing the root reads only the root, so this is invisible to it.
	if err := syncDir(root); err != nil {
		t.Fatalf("syncDir on the root: %v", err)
	}

	if err := os.Chmod(nested, 0o000); err != nil {
		t.Fatalf("close the nested directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(nested, 0o700) })

	err := syncTree(root)
	if err == nil {
		t.Fatal("syncTree reported success without reaching the nested directory")
	}
	if !strings.Contains(err.Error(), "bin") {
		t.Errorf("syncTree error = %v, want it to name the directory it could not flush", err)
	}
}

// Every directory of a real platform tree is flushed, so the guarantee holds for
// the shape this actually installs rather than only for a two-level fixture.
func TestSyncTreeDescendsThroughAWholePlatformTree(t *testing.T) {
	if os.Getuid() == 0 { //nolint:forbidigo // the permission check below is meaningless as root
		t.Skip("root ignores the permission bits this test relies on")
	}

	root := t.TempDir()
	deep := filepath.Join(root, "codex-resources", "zsh", "bin")
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatalf("prepare the tree: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o700); err != nil {
		t.Fatalf("prepare the tree: %v", err)
	}

	if err := syncTree(root); err != nil {
		t.Fatalf("syncTree on a well-formed tree: %v", err)
	}

	// The deepest directory, which only a full descent reaches.
	if err := os.Chmod(deep, 0o000); err != nil {
		t.Fatalf("close the deepest directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(deep, 0o700) })

	if err := syncTree(root); err == nil {
		t.Error("syncTree reported success without reaching the deepest directory")
	}
}

// Publishing flushes the staged tree through the whole-tree flusher.
//
// The seam is the only way to state this: a call site reverted to flushing the
// tree's root alone still succeeds, still publishes, and still passes every
// other test here, because the difference between the two shows up only as a
// torn directory after a power cut.
func TestPublishingFlushesTheWholeStagedTree(t *testing.T) {
	body := wellFormed(t)
	pin(t, "9.9.9", integrity(body))
	inst := installer(t, serve(t, "9.9.9", body))

	var flushed []string
	inst.syncStaged = func(root string) error {
		if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				flushed = append(flushed, path)
			}
			return nil
		}); err != nil {
			return err
		}
		return syncTree(root)
	}

	if _, err := inst.Install(t.Context(), "9.9.9"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(flushed) == 0 {
		t.Fatal("publishing never flushed the staged tree")
	}

	// Every directory of the tree, not just the one that was renamed.
	for _, want := range []string{"bin", "codex-path"} {
		if !slices.ContainsFunc(flushed, func(p string) bool { return filepath.Base(p) == want }) {
			t.Errorf("flushed %v, want it to include the %s directory", flushed, want)
		}
	}
}
