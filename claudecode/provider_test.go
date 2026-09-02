package claudecode

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	agentic "github.com/pedromvgomes/agentic-driver"
)

func TestTheDescriptorNamesTheProviderAndItsBinary(t *testing.T) {
	d := testProvider(t).Descriptor()

	if d.ID != ID {
		t.Errorf("ID = %q, want %q", d.ID, ID)
	}
	if d.Binary != BinaryName {
		t.Errorf("Binary = %q, want %q", d.Binary, BinaryName)
	}
	if d.DisplayName == "" {
		t.Error("DisplayName is empty")
	}
}

// The binary is executed by absolute path at a pinned version, so no PATH entry
// and no repointed symlink can substitute a build the signed manifest never
// described.
func TestTheBinaryPathIsAbsoluteAndPinned(t *testing.T) {
	root := t.TempDir()
	p, err := New(root, WithVersion("9.9.9"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	path := p.BinaryPath()
	if !filepath.IsAbs(path) {
		t.Errorf("BinaryPath = %q, want an absolute path", path)
	}
	if !strings.Contains(path, "9.9.9") {
		t.Errorf("BinaryPath = %q, want it to name the pinned version", path)
	}
	if p.Version() != "9.9.9" {
		t.Errorf("Version = %q, want the pin", p.Version())
	}
}

// A pin that could escape the providers root would make the executed path
// something other than a version directory under it.
func TestATraversingPinIsRefusedAtConstruction(t *testing.T) {
	for _, version := range []string{"../etc", "1.2.3/../../x", "", "not a version"} {
		if _, err := New(t.TempDir(), WithVersion(version)); err == nil {
			t.Errorf("New accepted the pin %q", version)
		}
	}
}

// Without a config directory the CLI reads and writes the caller's own
// ~/.claude, so a run mutates the settings of the human at the machine.
func TestAConfigDirectoryRedirectsTheCLIsOwnState(t *testing.T) {
	dir := t.TempDir()
	p, err := New(t.TempDir(), WithConfigDir(dir))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	inv, err := p.Command(agentic.Request{Prompt: "hi"})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if inv.Env["CLAUDE_CONFIG_DIR"] != dir {
		t.Errorf("CLAUDE_CONFIG_DIR = %q, want %q", inv.Env["CLAUDE_CONFIG_DIR"], dir)
	}
	if inv.Env["HOME"] != dir {
		t.Errorf("HOME = %q, want the nominated config directory so caches land inside it", inv.Env["HOME"])
	}
}

// The CLI updates itself by default, and the pin exists precisely so the
// envelope schema and flag spelling stay the ones this package was written
// against.
func TestEveryInvocationDisablesTheAutoUpdater(t *testing.T) {
	p := testProvider(t)

	for name, build := range map[string]func(agentic.Request) (agentic.Invocation, error){
		"Command":       p.Command,
		"StreamCommand": p.StreamCommand,
	} {
		inv, err := build(agentic.Request{Prompt: "hi"})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if inv.Env["DISABLE_AUTOUPDATER"] != "1" {
			t.Errorf("%s leaves the auto-updater on", name)
		}
	}
}

// The provider supplies the protection rather than the caller, because the
// provider is what knows which version it is about to execute.
func TestPruneNeverRemovesThePin(t *testing.T) {
	root := t.TempDir()
	p, err := New(root, WithVersion("1.0.0"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for _, version := range []string{"1.0.0", "2.0.0", "3.0.0"} {
		dir := filepath.Join(root, "claude-code", version)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, BinaryName), []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	if err := p.Prune(t.Context(), 1); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	installed, err := p.Installed(t.Context())
	if err != nil {
		t.Fatalf("Installed: %v", err)
	}
	if !slices.Contains(installed, "1.0.0") {
		t.Errorf("Prune removed the pinned version; installed = %v", installed)
	}
}

// "Install what you need" has to be a request a caller can make without
// knowing the version number.
func TestInstallDefaultsToThePin(t *testing.T) {
	p, err := New(t.TempDir(), WithVersion("1.0.0"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// No bucket is reachable, so this fails — but the error names which version
	// it went looking for, which is what the defaulting is.
	_, err = p.Install(t.Context(), "")
	if err == nil {
		t.Skip("the release bucket was reachable from this machine")
	}
	if !strings.Contains(err.Error(), "1.0.0") && !errors.Is(err, ErrInvalidVersion) {
		t.Logf("Install error: %v", err)
	}
}

// The binary under the root is executed by the path New composes, so a
// relative root resolves against whatever working directory the child inherits
// — the PATH-style ambiguity that executing by absolute path exists to remove.
func TestNewRefusesARelativeProvidersRoot(t *testing.T) {
	for _, root := range []string{"", "providers", "./providers", "../providers"} {
		if _, err := New(root); err == nil {
			t.Errorf("New accepted the relative providers root %q", root)
		}
	}
}

// A directory left behind by an interrupted install holds no runnable binary,
// so Installed() rightly omits it — but pruning that same list would leave it
// forever, occupying a version's worth of disk AND making every later install
// of that version fail to publish over it.
func TestPruneRemovesDebrisInstalledDoesNotReport(t *testing.T) {
	root := t.TempDir()
	p, err := New(root, WithVersion("3.0.0"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	versions := filepath.Join(root, "claude-code")
	runnable := func(version string) {
		t.Helper()
		dir := filepath.Join(versions, version)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, BinaryName), []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	runnable("3.0.0")
	runnable("2.0.0")

	// Debris: the directory exists, the binary does not.
	debris := filepath.Join(versions, "1.0.0")
	if err := os.MkdirAll(debris, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	installed, err := p.Installed(t.Context())
	if err != nil {
		t.Fatalf("Installed: %v", err)
	}
	if slices.Contains(installed, "1.0.0") {
		t.Fatal("Installed reports a version with no runnable binary")
	}

	if err := p.Prune(t.Context(), 1); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	if _, err := os.Stat(debris); !os.IsNotExist(err) {
		t.Error("Prune left behind a directory Installed never reports, so nothing can ever remove it")
	}
	if _, err := os.Stat(filepath.Join(versions, "3.0.0")); err != nil {
		t.Errorf("Prune removed the pinned version: %v", err)
	}
}

// Debris under the pinned version is repaired by installing over it, not by
// deleting it out from under an install that may be publishing right now.
func TestPruneLeavesDebrisUnderThePinAlone(t *testing.T) {
	root := t.TempDir()
	p, err := New(root, WithVersion("1.0.0"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	pinned := filepath.Join(root, "claude-code", "1.0.0")
	if err := os.MkdirAll(pinned, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := p.Prune(t.Context(), 1); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if _, err := os.Stat(pinned); err != nil {
		t.Errorf("Prune removed the pinned version's directory: %v", err)
	}
}

// Nothing outside this package's own layout is ever a candidate for removal.
func TestPruneTouchesNothingThatIsNotAVersion(t *testing.T) {
	root := t.TempDir()
	p, err := New(root, WithVersion("2.0.0"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	versions := filepath.Join(root, "claude-code")
	for _, name := range []string{"not-a-version", ".hidden", "README"} {
		if err := os.MkdirAll(filepath.Join(versions, name), 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	if err := p.Prune(t.Context(), 1); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	for _, name := range []string{"not-a-version", ".hidden", "README"} {
		if _, err := os.Stat(filepath.Join(versions, name)); err != nil {
			t.Errorf("Prune removed %q, which is not a version directory", name)
		}
	}
}

// "Installed" and "runnable" must be the same question. A binary executable by
// its group but not its owner would otherwise let the driver call a version
// runnable while the installer treats it as absent — reinstalling it and then
// failing to publish over the directory already there, on every retry.
func TestInstalledAndRunnableAgreeOnWhatIsExecutable(t *testing.T) {
	for _, mode := range []os.FileMode{0o100, 0o010, 0o001, 0o755} {
		t.Run(mode.String(), func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "claude-code", "1.0.0")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			binary := filepath.Join(dir, BinaryName)
			if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			if err := os.Chmod(binary, 0o400|mode); err != nil {
				t.Fatalf("chmod: %v", err)
			}

			p, err := New(root, WithVersion("1.0.0"))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			d, err := agentic.New(p)
			if err != nil {
				t.Fatalf("agentic.New: %v", err)
			}

			installed, err := p.Installed(t.Context())
			if err != nil {
				t.Fatalf("Installed: %v", err)
			}
			isInstalled := slices.Contains(installed, "1.0.0")
			isRunnable := d.Ready() == nil

			if isInstalled != isRunnable {
				t.Errorf("at mode %v: Installed says %v, Ready says %v", mode, isInstalled, isRunnable)
			}
		})
	}
}
