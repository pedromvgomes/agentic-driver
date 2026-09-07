package codex

import (
	"errors"
	"os"
	"runtime"
	"testing"
)

// npm names platforms the Node way and Go names them its own way, so the two
// are translated rather than assumed equal. Tested with literal inputs because
// every other caller passes runtime.GOOS and runtime.GOARCH: an amd64 mapping
// checked only on the machine running the suite is checked on Apple silicon and
// nowhere else, which is exactly where the x64 spelling would go unnoticed.
func TestPlatformKeyNamesEachVendoredBuild(t *testing.T) {
	for _, c := range []struct {
		goos, goarch, want string
	}{
		{"darwin", "arm64", "darwin-arm64"},
		{"darwin", "amd64", "darwin-x64"},
		{"linux", "arm64", "linux-arm64"},
		{"linux", "amd64", "linux-x64"},
	} {
		got, err := PlatformKey(c.goos, c.goarch)
		if err != nil {
			t.Errorf("PlatformKey(%q, %q): %v", c.goos, c.goarch, err)
			continue
		}
		if got != c.want {
			t.Errorf("PlatformKey(%q, %q) = %q, want %q", c.goos, c.goarch, got, c.want)
		}
	}
}

// A platform this package does not vendor on is refused rather than guessed at.
//
// Windows is the case that matters: it runs a codex binary, and NewOnPath is
// the answer there, but a vendored install would publish a tree whose binary
// carries no execute bit for Executable to find — installed correctly and
// reported missing forever after.
func TestPlatformKeyRefusesWhatIsNotVendored(t *testing.T) {
	for _, c := range []struct{ goos, goarch string }{
		{"windows", "amd64"},
		{"windows", "arm64"},
		{"freebsd", "amd64"},
		{"linux", "386"},
		{"darwin", "riscv64"},
	} {
		if _, err := PlatformKey(c.goos, c.goarch); !errors.Is(err, ErrPlatformUnsupported) {
			t.Errorf("PlatformKey(%q, %q) error = %v, want ErrPlatformUnsupported", c.goos, c.goarch, err)
		}
	}
}

// The digest table is what a download is judged against, so a version it does
// not describe is refused before any request is made, and a version pinned for
// other platforms is refused on this one.
func TestPinnedDigestAnswersOnlyForCommittedBuilds(t *testing.T) {
	if _, err := PinnedDigest("0.0.1", "linux-x64"); !errors.Is(err, ErrUnpinnedVersion) {
		t.Errorf("PinnedDigest for an unknown version = %v, want ErrUnpinnedVersion", err)
	}
	if _, err := PinnedDigest(PinnedVersion, "plan9-mips"); !errors.Is(err, ErrPlatformUnsupported) {
		t.Errorf("PinnedDigest for an unvendored platform = %v, want ErrPlatformUnsupported", err)
	}

	digest, err := PinnedDigest(PinnedVersion, "linux-x64")
	if err != nil {
		t.Fatalf("PinnedDigest for the pin: %v", err)
	}
	// npm's own Subresource Integrity spelling, committed verbatim so an
	// operator can compare it against `npm view` without decoding anything.
	if len(digest) < len("sha512-") || digest[:len("sha512-")] != "sha512-" {
		t.Errorf("PinnedDigest = %q, want npm's sha512- integrity spelling", digest)
	}
}

// Every platform this package vendors on must have a digest for every pinned
// version. A version pinned for some platforms and not others builds a provider
// that constructs on one machine and refuses on another.
func TestEveryPinnedVersionCoversEveryVendoredPlatform(t *testing.T) {
	vendored := []string{"darwin-arm64", "darwin-x64", "linux-arm64", "linux-x64"}

	for version, digests := range pinnedDigests {
		for _, platform := range vendored {
			if _, ok := digests[platform]; !ok {
				t.Errorf("version %s has no digest for %s", version, platform)
			}
		}
		if len(digests) != len(vendored) {
			t.Errorf("version %s pins %d platforms, want exactly %d", version, len(digests), len(vendored))
		}
	}
}

// In CI, a runner this package does not vendor on is a failure rather than a
// skip.
//
// Every helper that stands up an installer skips when PlatformKey refuses the
// host, so on an unsupported runner the whole install and release suite reports
// as passing while exercising nothing. This is the one test that refuses, so
// "no coverage at all" cannot look like "everything green".
//
// Gated on CI because an unsupported platform is a legitimate place to develop:
// codex runs there through NewOnPath, and only the vendored half is absent.
func TestCIRunsOnAPlatformThisPackageVendorsOn(t *testing.T) {
	if os.Getenv("CI") == "" {
		t.Skip("not CI; an unsupported platform is a fine place to develop")
	}

	if _, err := PlatformKey(runtime.GOOS, runtime.GOARCH); err != nil {
		t.Fatalf("CI runs on %s/%s, which vendors no Codex build, so the install suite is silently skipping: %v",
			runtime.GOOS, runtime.GOARCH, err)
	}
}
