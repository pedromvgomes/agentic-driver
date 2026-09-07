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
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	// outside places the entry beside the vendor tree rather than inside it,
	// where the npm wrapper keeps its own files.
	outside bool
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
		name := vendorPrefix + dir + "/" + e.name
		if e.outside {
			name = "package/" + e.name
		}
		header := &tar.Header{
			Name:     name,
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

// patience bounds every wait in these tests.
//
// Long enough that a loaded machine does not fail a healthy run, short enough
// that a regression fails the suite instead of hanging until the harness kills
// it — a hang reports as a timeout naming the whole package rather than the
// property that broke.
const patience = 10 * time.Second

// publishVersion makes a version present the way a completed install would,
// reporting rather than failing, so it can be called from a goroutine that is
// not the test's.
func publishVersion(inst *Installer, version string) error {
	dir := filepath.Join(inst.root, version, "bin")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, BinaryName), []byte("#!/bin/sh\n"), 0o700)
}

// serveHandler stands up a registry with handler-level control, for the
// failures a well-behaved server never produces.
func serveHandler(t *testing.T, handler http.HandlerFunc) registry {
	t.Helper()

	if _, err := PlatformKey(runtime.GOOS, runtime.GOARCH); err != nil {
		t.Skipf("this platform vendors no Codex build: %v", err)
	}

	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		handler(w, r)
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

	if err := publishVersion(inst, version); err != nil {
		t.Fatalf("stage %s: %v", version, err)
	}
}

// Installed() answers "what can I run", so a name that is not a version, a
// directory the package hides from enumeration, and a version whose binary
// cannot be executed are all absent from it. Reporting any of them hands the
// driver a path it would fail to exec at the start of a run.
func TestInstalledReportsOnlyRunnableVersions(t *testing.T) {
	inst := installer(t, serve(t, "9.9.9", nil))
	stage(t, inst, "0.2.0")

	// A file where only directories are versions.
	if err := os.WriteFile(filepath.Join(inst.root, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("prepare the stray file: %v", err)
	}

	// A dot-prefixed directory, which is never a version; a name that is not a
	// version; and a version directory whose binary never became executable.
	// None of the three is something a driver could exec.
	for _, dir := range []string{".9.9.9-staging", "latest", "0.3.0/bin"} {
		if err := os.MkdirAll(filepath.Join(inst.root, dir), 0o700); err != nil {
			t.Fatalf("prepare %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(inst.root, "0.3.0", "bin", BinaryName), []byte("x"), 0o600); err != nil {
		t.Fatalf("prepare the unusable binary: %v", err)
	}

	installed, err := inst.Installed(t.Context())
	if err != nil {
		t.Fatalf("Installed: %v", err)
	}
	if len(installed) != 1 || installed[0] != "0.2.0" {
		t.Errorf("Installed() = %v, want [0.2.0]", installed)
	}
}

// Prune reclaims disk, so it sweeps every version-shaped directory rather than
// the runnable ones Installed() reports. A directory whose binary is missing is
// invisible to Installed(), so pruning that list would leave it on the disk
// forever — and leave the rename that would republish that version failing.
func TestPruneRemovesDebrisInstalledNeverLists(t *testing.T) {
	inst := installer(t, serve(t, "9.9.9", nil))
	stage(t, inst, "0.4.0")

	debris := filepath.Join(inst.root, "0.1.0", "bin")
	if err := os.MkdirAll(debris, 0o700); err != nil {
		t.Fatalf("prepare the debris: %v", err)
	}

	if installed, _ := inst.Installed(t.Context()); len(installed) != 1 {
		t.Fatalf("Installed() = %v, want only the runnable version", installed)
	}
	if err := inst.Prune(t.Context(), 1, "0.4.0"); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if _, err := os.Stat(filepath.Join(inst.root, "0.1.0")); !os.IsNotExist(err) {
		t.Errorf("the debris directory survived Prune: %v", err)
	}
}

// Another process publishing the same version first is a race the install
// joins rather than fails: its copy was checked against the same digest, so the
// version is present and correct however it got there.
func TestAVersionPublishedByAnotherProcessMidInstallIsAccepted(t *testing.T) {
	body := wellFormed(t)
	pin(t, "9.9.9", integrity(body))

	var inst *Installer
	// The handler runs on the server's goroutine, where t.Fatalf is not
	// allowed: it would unwind that goroutine instead of failing the test,
	// truncating the response so the failure arrives as a digest mismatch
	// naming nothing. The error travels back to the test goroutine instead.
	staged := make(chan error, 1)
	reg := serveHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		// Published while this download is in flight, so the fast path at the
		// top of installOnce cannot have seen it and the rename is what
		// collides.
		staged <- publishVersion(inst, "9.9.9")
		_, _ = w.Write(body)
	})
	inst = installer(t, reg)

	result, err := inst.Install(t.Context(), "9.9.9")
	select {
	case stageErr := <-staged:
		if stageErr != nil {
			t.Fatalf("publishing the racing copy: %v", stageErr)
		}
	default:
		t.Fatal("the registry was never asked for the tarball")
	}
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !result.AlreadyPresent {
		t.Errorf("Install reported %+v, want the winner's copy accepted as already present", result)
	}
}

// A torn directory holding no runnable binary cannot be published over, and
// saying so names the one thing that fixes it. Reporting success would point
// Path() at a file that does not exist, and every later attempt would repeat
// it — a version permanently "present" and permanently unrunnable.
func TestATornDirectoryBlocksTheInstallAndSaysSo(t *testing.T) {
	body := wellFormed(t)
	pin(t, "9.9.9", integrity(body))
	inst := installer(t, serve(t, "9.9.9", body))

	torn := filepath.Join(inst.root, "9.9.9", "codex-path")
	if err := os.MkdirAll(torn, 0o700); err != nil {
		t.Fatalf("prepare the torn directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(torn, "rg"), []byte("x"), 0o600); err != nil {
		t.Fatalf("prepare the torn directory: %v", err)
	}

	_, err := inst.Install(t.Context(), "9.9.9")
	if err == nil {
		t.Fatal("Install published over a directory holding no usable binary")
	}
	if !strings.Contains(err.Error(), "remove it and retry") {
		t.Errorf("Install error = %v, want it to name the way out", err)
	}
}

// A caller that gives up is not a caller that cancels everyone else's install.
// The download belongs to the set of callers waiting on it, so it survives one
// of them leaving and stops only when the last one does.
func TestACallerGivingUpLeavesTheOthersInstalling(t *testing.T) {
	body := wellFormed(t)
	pin(t, "9.9.9", integrity(body))

	release := make(chan struct{})
	arrived := make(chan struct{})
	var once sync.Once
	reg := serveHandler(t, func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(arrived) })
		select {
		case <-release:
			_, _ = w.Write(body)
		case <-r.Context().Done():
		}
	})
	inst := installer(t, reg)

	stayed := make(chan error, 1)
	go func() {
		_, err := inst.Install(context.Background(), "9.9.9")
		stayed <- err
	}()
	<-arrived

	// The second caller joins the download already in flight, then gives up.
	left := make(chan error, 1)
	giveUp, cancel := context.WithCancel(context.Background())
	go func() {
		_, err := inst.Install(giveUp, "9.9.9")
		left <- err
	}()
	cancel()

	select {
	case err := <-left:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("the departing caller got %v, want context.Canceled", err)
		}
	case <-time.After(patience):
		t.Fatal("the departing caller never returned")
	}

	close(release)
	select {
	case err := <-stayed:
		if err != nil {
			t.Fatalf("the caller that stayed got %v, want the install to complete", err)
		}
	case <-time.After(patience):
		t.Fatal("the caller that stayed never completed")
	}
	if !usable(inst.Path("9.9.9")) {
		t.Error("the install did not publish a runnable binary")
	}
}

// The last caller leaving stops the download rather than leaving it running for
// nobody, which is what keeps a detached context bounded.
func TestTheLastCallerLeavingStopsTheDownload(t *testing.T) {
	body := wellFormed(t)
	pin(t, "9.9.9", integrity(body))

	arrived := make(chan struct{})
	stopped := make(chan struct{})
	var once sync.Once
	reg := serveHandler(t, func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(arrived) })
		<-r.Context().Done()
		close(stopped)
	})
	inst := installer(t, reg)

	giveUp, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := inst.Install(giveUp, "9.9.9")
		done <- err
	}()
	<-arrived
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Install error = %v, want context.Canceled", err)
		}
	case <-time.After(patience):
		t.Fatal("the cancelled caller never returned")
	}
	select {
	case <-stopped:
	case <-time.After(patience):
		t.Error("the download outlived the last caller waiting on it")
	}
}

// A registry that answers with anything but the tarball has made no statement
// about the pinned bytes, so the status reaches the caller rather than being
// folded into a digest mismatch.
func TestARegistryErrorIsReportedAsItself(t *testing.T) {
	pin(t, "9.9.9", integrity(wellFormed(t)))
	inst := installer(t, serveHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream is unwell", http.StatusInternalServerError)
	}))

	_, err := inst.Install(t.Context(), "9.9.9")
	if err == nil {
		t.Fatal("Install accepted a 500 from the registry")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("Install error = %v, want it to name the status", err)
	}
	if installed, _ := inst.Installed(t.Context()); len(installed) != 0 {
		t.Errorf("Installed() = %v after a failed fetch, want none", installed)
	}
}

// A real package carries explicit directory entries, a bare entry for the
// triple itself, and the npm wrapper's own files beside the vendor tree. All
// three are ordinary parts of the archive: refusing any would make every
// genuine install fail, and installing the wrapper's files would put documents
// describing the packaging where the CLI is meant to be.
func TestExplicitDirectoryEntriesAreInstalled(t *testing.T) {
	body := packageTarball(t, testTriple,
		entry{name: "", typ: tar.TypeDir},
		entry{name: "bin", typ: tar.TypeDir},
		entry{name: "bin/" + BinaryName, body: "#!/bin/sh\n", mode: 0o755},
		entry{name: "codex-resources/zsh/bin", typ: tar.TypeDir},
		entry{name: "codex-resources/zsh/bin/zsh", body: "shell", mode: 0o755},
		// Siblings of the vendor tree rather than entries within it.
		entry{name: "package.json", body: `{"name":"@openai/codex"}`, outside: true},
		entry{name: "README.md", body: "# codex", outside: true},
	)
	pin(t, "9.9.9", integrity(body))
	inst := installer(t, serve(t, "9.9.9", body))

	result, err := inst.Install(t.Context(), "9.9.9")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !usable(result.Path) {
		t.Errorf("%s is not runnable after a successful install", result.Path)
	}
	nested := filepath.Join(inst.root, "9.9.9", "codex-resources", "zsh", "bin", "zsh")
	if _, err := os.Stat(nested); err != nil {
		t.Errorf("the nested helper was not installed: %v", err)
	}
	for _, wrapper := range []string{"package.json", "README.md"} {
		if _, err := os.Stat(filepath.Join(inst.root, "9.9.9", wrapper)); !os.IsNotExist(err) {
			t.Errorf("the npm wrapper's %s was installed into the version tree", wrapper)
		}
	}
}

// The sweep reclaims what a dead process abandoned and nothing else.
//
// Age is the only test available: installs of different versions run
// concurrently, in this process and in any other sharing the providers root,
// each holding its own staging directory. "Everything but mine" would delete a
// tree another install is extracting into at that moment.
func TestTheSweepReclaimsOnlyStagingTreesOldEnoughToBeAbandoned(t *testing.T) {
	body := wellFormed(t)
	pin(t, "9.9.9", integrity(body))
	inst := installer(t, serve(t, "9.9.9", body))

	if err := os.MkdirAll(inst.staging, 0o700); err != nil {
		t.Fatalf("prepare the staging root: %v", err)
	}
	abandoned := filepath.Join(inst.staging, "9.9.9-abandoned")
	live := filepath.Join(inst.staging, "9.9.9-live")
	for _, dir := range []string{abandoned, live} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatalf("prepare %s: %v", dir, err)
		}
	}

	// Older than any install could still be working on.
	stale := time.Now().Add(-2 * stagingLifetime)
	if err := os.Chtimes(abandoned, stale, stale); err != nil {
		t.Fatalf("backdate %s: %v", abandoned, err)
	}

	inst.sweepStaging()

	if _, err := os.Stat(abandoned); !os.IsNotExist(err) {
		t.Errorf("the abandoned staging tree survived the sweep: %v", err)
	}
	if _, err := os.Stat(live); err != nil {
		t.Errorf("the sweep removed a staging tree a live install could own: %v", err)
	}
}

// A clock corrected backwards leaves a stamp in the future, and a future stamp
// is not an age. Read as a magnitude it would make the install that just
// stamped itself look like the oldest thing on the disk.
func TestTheSweepLeavesAStagingTreeStampedInTheFuture(t *testing.T) {
	inst := installer(t, serve(t, "9.9.9", nil))

	if err := os.MkdirAll(inst.staging, 0o700); err != nil {
		t.Fatalf("prepare the staging root: %v", err)
	}
	ahead := filepath.Join(inst.staging, "9.9.9-ahead")
	if err := os.Mkdir(ahead, 0o700); err != nil {
		t.Fatalf("prepare %s: %v", ahead, err)
	}
	future := time.Now().Add(2 * stagingLifetime)
	if err := os.Chtimes(ahead, future, future); err != nil {
		t.Fatalf("stamp %s in the future: %v", ahead, err)
	}

	inst.sweepStaging()

	if _, err := os.Stat(ahead); err != nil {
		t.Errorf("the sweep removed a staging tree stamped in the future: %v", err)
	}
}

// A clean install leaves the staging root empty, so the sweep has nothing to
// reclaim in the ordinary case and disk is not held between installs.
func TestACleanInstallLeavesNoStagingTreeBehind(t *testing.T) {
	body := wellFormed(t)
	pin(t, "9.9.9", integrity(body))
	inst := installer(t, serve(t, "9.9.9", body))

	if _, err := inst.Install(t.Context(), "9.9.9"); err != nil {
		t.Fatalf("Install: %v", err)
	}

	entries, err := os.ReadDir(inst.staging)
	if err != nil {
		t.Fatalf("read the staging root: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("the staging root holds %d leftovers after a clean install, want none", len(entries))
	}
}

// The staging directory's mtime tracks the start of extraction rather than the
// start of the call, because its last direct-child change is the tree directory
// the download's completion precedes. A sweep threshold read against the call's
// age would shrink by however long the download took.
func TestTheStagingStampFollowsTheDownload(t *testing.T) {
	body := wellFormed(t)
	pin(t, "9.9.9", integrity(body))

	held := make(chan struct{})
	reg := serveHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		<-held
		_, _ = w.Write(body)
	})
	inst := installer(t, reg)

	var stampedAt time.Time
	inst.syncStaged = func(root string) error {
		if info, err := os.Stat(filepath.Dir(root)); err == nil {
			stampedAt = info.ModTime()
		}
		return syncTree(root)
	}

	started := time.Now()
	go func() {
		time.Sleep(50 * time.Millisecond)
		close(held)
	}()
	if _, err := inst.Install(t.Context(), "9.9.9"); err != nil {
		t.Fatalf("Install: %v", err)
	}

	if stampedAt.IsZero() {
		t.Fatal("the staging directory was never observed")
	}
	if !stampedAt.After(started) {
		t.Errorf("the staging stamp is %v, from before the download finished at the earliest %v",
			stampedAt, started)
	}
}
