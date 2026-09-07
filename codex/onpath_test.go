package codex

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	agentic "github.com/pedromvgomes/agentic-driver"
)

// onPath builds the PATH provider, which is the one every dialect test wants:
// flag spelling, the envelope and the credential vocabulary are the same object
// whichever binary ends up running, and a vendored provider would drag a
// providers root into tests that have nothing to say about installing.
func onPath(t *testing.T) *PathProvider {
	t.Helper()

	p, err := NewOnPath()
	if err != nil {
		t.Fatalf("NewOnPath: %v", err)
	}
	return p
}

// vendored builds the pinned provider, skipping where this package vendors no
// build for the machine running the test.
func vendored(t *testing.T) *Provider {
	t.Helper()

	p, err := New(t.TempDir())
	if err != nil {
		t.Skipf("this platform vendors no Codex build: %v", err)
	}
	return p
}

// Pinning a version is a request for a specific build, and a provider that runs
// whatever PATH resolves to cannot honour it. Refusing is what stops the
// request from being answered with a substitution nobody was told about.
func TestPinningAVersionNeedsAVendoredInstall(t *testing.T) {
	if _, err := NewOnPath(WithVersion(PinnedVersion)); err == nil {
		t.Fatal("NewOnPath accepted WithVersion; a PATH lookup cannot honour a pin")
	}
}

// The absence is the capability. A driver asks whether it can install by
// asserting on the interface, so a PathProvider that satisfied Pinner would
// answer yes and then hand out a path it never chose.
func TestThePathProviderClaimsNoControlOverTheBinary(t *testing.T) {
	var p any = onPath(t)

	if _, ok := p.(agentic.Pinner); ok {
		t.Error("PathProvider implements Pinner; it runs a build it did not choose")
	}
	if _, ok := p.(agentic.Installer); ok {
		t.Error("PathProvider implements Installer; it verified nothing")
	}
}

// Vendoring settles which bytes run and says nothing about who built them. The
// two interfaces exist to keep those apart, so the vendored provider must
// satisfy exactly the first.
func TestTheVendoredProviderPinsWithoutClaimingProvenance(t *testing.T) {
	var p any = vendored(t)

	if _, ok := p.(agentic.Pinner); !ok {
		t.Error("Provider does not implement Pinner; it vendors a binary at a pinned version")
	}
	if _, ok := p.(agentic.Installer); ok {
		t.Error("Provider implements Installer; no publisher signature is checked on every platform it vendors on")
	}
}

// A version with no committed digest could never be installed, so accepting one
// here builds a provider whose every Install fails — at the call that needed the
// binary rather than the call that chose it.
func TestAVersionWithNoCommittedDigestIsRefusedAtConstruction(t *testing.T) {
	if _, err := New(t.TempDir(), WithVersion("0.1.0")); err == nil {
		t.Fatal("New accepted an unpinned version; there is nothing to check its download against")
	}
}

// The binary is named by an absolute path rather than looked up, so nothing on
// PATH and no repointed symlink can substitute a different build between the
// pin and the exec.
func TestTheVendoredBinaryIsNamedByAnAbsolutePath(t *testing.T) {
	path := vendored(t).BinaryPath()

	if !filepath.IsAbs(path) {
		t.Fatalf("BinaryPath() = %q, want an absolute path", path)
	}
	segments := strings.Split(filepath.ToSlash(path), "/")
	if !slices.Contains(segments, PinnedVersion) {
		t.Errorf("BinaryPath() = %q, want it to name the pinned version %s", path, PinnedVersion)
	}
}

// The provider's own retention protects the version it is about to execute,
// which is the reason that protection lives here rather than with the caller: a
// caller passing a keep count cannot know which build the driver would run.
func TestTheProviderProtectsItsPinFromItsOwnRetention(t *testing.T) {
	p := vendored(t)

	// Newer than the pin, so recency alone would evict it: the pin survives
	// only because the provider names it as protected. Staging versions OLDER
	// than the pin would keep it on recency and assert nothing.
	for _, v := range []string{PinnedVersion, "99.0.0", "99.1.0"} {
		dir := filepath.Join(p.installer.root, v, "bin")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("stage %s: %v", v, err)
		}
		if err := os.WriteFile(filepath.Join(dir, BinaryName), []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatalf("stage %s: %v", v, err)
		}
	}

	if err := p.Prune(t.Context(), 1); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	installed, err := p.Installed(t.Context())
	if err != nil {
		t.Fatalf("Installed: %v", err)
	}
	if !slices.Contains(installed, PinnedVersion) {
		t.Errorf("Installed() = %v, want it to still hold the pinned %s", installed, PinnedVersion)
	}
	// The newest, kept on recency, and the middle one, kept by neither.
	if !slices.Contains(installed, "99.1.0") {
		t.Errorf("Installed() = %v, want it to hold the newest version", installed)
	}
	if slices.Contains(installed, "99.0.0") {
		t.Errorf("Installed() = %v, want the unprotected older version pruned", installed)
	}
}

// A version pinned for other platforms but not this one is refused where it is
// chosen, not where the binary is finally needed.
//
// It fails exactly as an unknown version does — there is no digest to judge a
// download against — so a provider that accepted it would be one whose every
// Install fails, at the call that wanted the CLI rather than the call that
// configured it.
func TestAVersionPinnedForOtherPlatformsIsRefusedHere(t *testing.T) {
	here, err := PlatformKey(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skipf("this platform vendors no Codex build: %v", err)
	}

	// Every vendored platform except this machine's.
	elsewhere := map[string]string{}
	for _, platform := range []string{"darwin-arm64", "darwin-x64", "linux-arm64", "linux-x64"} {
		if platform != here {
			elsewhere[platform] = "sha512-" + strings.Repeat("A", 86) + "=="
		}
	}
	pinnedDigests["98.0.0"] = elsewhere
	t.Cleanup(func() { delete(pinnedDigests, "98.0.0") })

	_, err = New(t.TempDir(), WithVersion("98.0.0"))
	if err == nil {
		t.Fatal("New accepted a version with no digest for this platform")
	}
	if !errors.Is(err, ErrPlatformUnsupported) {
		t.Errorf("New error = %v, want ErrPlatformUnsupported", err)
	}
}
