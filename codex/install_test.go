package codex

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	agentic "github.com/pedromvgomes/agentic-driver"
)

// testTriple is the vendor directory name inside a fabricated package. Its
// value is irrelevant to every test here: extract reads the triple out of the
// archive rather than composing it, which is what keeps the Go-to-Rust platform
// mapping from being written twice.
const testTriple = "aarch64-apple-darwin"

// entry is one file in a fabricated package tarball.
type entry struct {
	name string // relative to the vendor triple directory
	body string
	mode os.FileMode
	typ  byte
	link string
	// triple overrides the archive's vendor directory for this entry alone, so
	// a test can build the one shape a real package never has.
	triple string
}

// packageTarball builds a gzipped tar shaped like an npm Codex platform
// package, so a test can control exactly what an install is asked to expand.
func packageTarball(t *testing.T, triple string, entries ...entry) []byte {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		dir := triple
		if e.triple != "" {
			dir = e.triple
		}
		header := &tar.Header{
			Name:     vendorPrefix + dir + "/" + e.name,
			Mode:     int64(mode),
			Size:     int64(len(e.body)),
			Typeflag: typ,
			Linkname: e.link,
		}
		if typ != tar.TypeReg {
			header.Size = 0
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatalf("write header for %s: %v", e.name, err)
		}
		if typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatalf("write %s: %v", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

// wellFormed is the smallest archive an install is allowed to accept.
func wellFormed(t *testing.T) []byte {
	t.Helper()

	return packageTarball(t, testTriple,
		entry{name: "bin/" + BinaryName, body: "#!/bin/sh\necho codex\n", mode: 0o755},
		entry{name: "codex-path/rg", body: "ripgrep", mode: 0o755},
		entry{name: "codex-package.json", body: `{"version":"test"}`},
	)
}

func integrity(body []byte) string {
	sum := sha512.Sum512(body)
	return "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
}

// pin registers a digest for a test version and removes it afterwards, so the
// committed table is exactly what production installs judge downloads against.
func pin(t *testing.T, version, digest string) {
	t.Helper()

	platform, err := PlatformKey(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skipf("this platform vendors no Codex build: %v", err)
	}
	pinnedDigests[version] = map[string]string{platform: digest}
	t.Cleanup(func() { delete(pinnedDigests, version) })
}

// registry serves one version's tarball and counts what was asked of it.
type registry struct {
	url      string
	requests *atomic.Int64
}

func serve(t *testing.T, version string, body []byte) registry {
	t.Helper()

	platform, err := PlatformKey(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skipf("this platform vendors no Codex build: %v", err)
	}

	var requests atomic.Int64
	want := "/" + npmPackage + "/-/codex-" + version + "-" + platform + ".tgz"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != want {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)

	return registry{url: server.URL, requests: &requests}
}

func installer(t *testing.T, reg registry) *Installer {
	t.Helper()

	inst, err := NewInstaller(t.TempDir(), WithBaseURL(reg.url))
	if err != nil {
		t.Skipf("this platform vendors no Codex build: %v", err)
	}
	return inst
}

// The whole tree is installed, not the binary alone: codex runs the helpers
// beside it, and a version holding only `codex` fails at its first search.
func TestAVerifiedPackageInstallsTheWholePlatformTree(t *testing.T) {
	body := wellFormed(t)
	pin(t, "9.9.9", integrity(body))
	inst := installer(t, serve(t, "9.9.9", body))

	result, err := inst.Install(t.Context(), "9.9.9")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if result.Path != inst.Path("9.9.9") {
		t.Errorf("Install reported %s, want %s", result.Path, inst.Path("9.9.9"))
	}
	if !usable(result.Path) {
		t.Errorf("%s is not runnable after a successful install", result.Path)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(filepath.Dir(result.Path)), "codex-path", "rg")); err != nil {
		t.Errorf("the helper beside the binary was not installed: %v", err)
	}

	installed, err := inst.Installed(t.Context())
	if err != nil {
		t.Fatalf("Installed: %v", err)
	}
	if len(installed) != 1 || installed[0] != "9.9.9" {
		t.Errorf("Installed() = %v, want [9.9.9]", installed)
	}
}

// A digest that does not match is the pin doing its job, and nothing may be
// left behind: a version directory published from unverified bytes would be
// reported as installed forever after.
func TestBytesThatAreNotThePinnedOnesInstallNothing(t *testing.T) {
	pin(t, "9.9.9", integrity([]byte("some other build entirely")))
	inst := installer(t, serve(t, "9.9.9", wellFormed(t)))

	_, err := inst.Install(t.Context(), "9.9.9")
	if !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("Install error = %v, want ErrDigestMismatch", err)
	}

	installed, err := inst.Installed(t.Context())
	if err != nil {
		t.Fatalf("Installed: %v", err)
	}
	if len(installed) != 0 {
		t.Errorf("Installed() = %v after a refused download, want none", installed)
	}
}

// A version with no committed digest has nothing to be judged against, so the
// refusal must not depend on reaching the network — it says the same thing
// offline, and it says it about this repository rather than about the registry.
func TestAnUnpinnedVersionIsRefusedWithoutAskingTheRegistry(t *testing.T) {
	reg := serve(t, "9.9.9", wellFormed(t))
	inst := installer(t, reg)

	_, err := inst.Install(t.Context(), "9.9.8")
	if !errors.Is(err, ErrUnpinnedVersion) {
		t.Fatalf("Install error = %v, want ErrUnpinnedVersion", err)
	}
	if got := reg.requests.Load(); got != 0 {
		t.Errorf("the registry was asked %d times for an unpinned version, want 0", got)
	}
}

// A version string reaches both a filesystem path and a URL. One that escapes
// either would let Install's fast path adopt a binary from outside the
// providers root and report it as verified.
func TestAVersionThatEscapesThePathIsRefused(t *testing.T) {
	inst := installer(t, serve(t, "9.9.9", nil))

	for _, version := range []string{"", "../../usr/local/bin", "latest", "9.9.9/../..", `..\..\evil`} {
		if _, err := inst.Install(t.Context(), version); err == nil {
			t.Errorf("Install(%q) was accepted", version)
		}
	}
}

// A tar is a list of paths a stranger chose. The digest says the archive is the
// pinned one; it says nothing about the archive being sane, and every one of
// these is a way for an entry to reach outside the tree being staged.
func TestAnArchiveThatIsNotAPlatformPackageIsRefused(t *testing.T) {
	cases := map[string][]byte{
		"a symlink out of the tree": packageTarball(t, testTriple,
			entry{name: "bin/" + BinaryName, body: "x", mode: 0o755},
			entry{name: "bin/escape", typ: tar.TypeSymlink, link: "/etc/passwd"}),
		"a name walking upwards": packageTarball(t, testTriple,
			entry{name: "bin/" + BinaryName, body: "x", mode: 0o755},
			entry{name: "../../../../etc/cron.d/evil", body: "x"}),
		"two platform trees at once": packageTarball(t, testTriple,
			entry{name: "bin/" + BinaryName, body: "x", mode: 0o755},
			entry{name: "bin/" + BinaryName, body: "x", mode: 0o755, triple: "x86_64-unknown-linux-musl"}),
		"no binary at all": packageTarball(t, testTriple,
			entry{name: "codex-package.json", body: "{}"}),
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			version := "9.9.9"
			pin(t, version, integrity(body))
			inst := installer(t, serve(t, version, body))

			_, err := inst.Install(t.Context(), version)
			if !errors.Is(err, ErrMalformedArchive) {
				t.Fatalf("Install error = %v, want ErrMalformedArchive", err)
			}
			if installed, _ := inst.Installed(t.Context()); len(installed) != 0 {
				t.Errorf("Installed() = %v after a refused archive, want none", installed)
			}
		})
	}
}

// The pinned version is what the driver is about to execute. A retention policy
// that could delete it would trade a full disk for a broken install, and the
// protection belongs to the provider because the provider is what knows the
// number.
func TestPruneNeverRemovesThePinnedVersion(t *testing.T) {
	inst := installer(t, serve(t, "9.9.9", nil))
	for _, v := range []string{"0.1.0", "0.2.0", "0.3.0", "0.4.0"} {
		stage(t, inst, v)
	}

	if err := inst.Prune(t.Context(), 2, "0.1.0"); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	installed, err := inst.Installed(t.Context())
	if err != nil {
		t.Fatalf("Installed: %v", err)
	}
	// The newest two, plus the protected one that is neither of them. A
	// combined budget would have kept a fourth.
	want := []string{"0.4.0", "0.3.0", "0.1.0"}
	if strings.Join(installed, ",") != strings.Join(want, ",") {
		t.Errorf("Installed() = %v, want %v", installed, want)
	}
}

// A protected version that is ALREADY among the newest must not buy a slot it
// never uses: keep=2 has to mean two versions on disk, not three.
func TestPruneDoesNotSpendTheProtectedSlotTwice(t *testing.T) {
	inst := installer(t, serve(t, "9.9.9", nil))
	for _, v := range []string{"0.1.0", "0.2.0", "0.3.0"} {
		stage(t, inst, v)
	}

	if err := inst.Prune(t.Context(), 2, "0.3.0"); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	installed, _ := inst.Installed(t.Context())
	if len(installed) != 2 {
		t.Errorf("Installed() = %v, want two versions", installed)
	}
}

// Callers arriving together want one download, not one each. Install is
// synchronous over ~110 MB, and impatient retries are the normal response to a
// call that appears to hang.
func TestConcurrentInstallsOfOneVersionDownloadItOnce(t *testing.T) {
	body := wellFormed(t)
	pin(t, "9.9.9", integrity(body))
	reg := serve(t, "9.9.9", body)
	inst := installer(t, reg)

	const callers = 8
	errs := make(chan error, callers)
	for range callers {
		go func() {
			_, err := inst.Install(context.Background(), "9.9.9")
			errs <- err
		}()
	}
	for range callers {
		if err := <-errs; err != nil {
			t.Fatalf("Install: %v", err)
		}
	}

	// Later arrivals see a version already present and never reach the
	// registry, so this is an upper bound rather than exactly one.
	if got := reg.requests.Load(); got > 1 {
		t.Errorf("the registry was asked %d times, want at most 1", got)
	}
}

// A driver built on the vendored provider names the pinned binary before it
// exists, because Install is how it gets there — and reports the difference as
// a state rather than as a fork failure mid-request.
func TestAVendoredDriverIsConfigurableBeforeItIsRunnable(t *testing.T) {
	p := vendored(t)

	d, err := agentic.New(p)
	if err != nil {
		t.Fatalf("agentic.New: %v", err)
	}
	if d.Binary() != p.BinaryPath() {
		t.Errorf("driver runs %s, want the pinned %s", d.Binary(), p.BinaryPath())
	}
	if err := d.Ready(); err == nil {
		t.Fatal("Ready() succeeded with nothing installed")
	} else if !strings.Contains(err.Error(), "call Install") {
		t.Errorf("Ready() = %v, want it to name the way out", err)
	}
}

// stage publishes a version directory the way a completed install would, so
// retention can be exercised without downloading anything.
func stage(t *testing.T, inst *Installer, version string) {
	t.Helper()

	dir := filepath.Join(inst.root, version, "bin")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("stage %s: %v", version, err)
	}
	if err := os.WriteFile(filepath.Join(dir, BinaryName), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatalf("stage %s: %v", version, err)
	}
}
