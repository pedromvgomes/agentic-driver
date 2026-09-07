package codex

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/mod/semver"

	agentic "github.com/pedromvgomes/agentic-driver"
)

// DefaultRetention is how many installed versions to keep.
//
// Two, because a platform tree is ~300 MB unpacked and disk is the constraint
// that decides this number. Two rather than one so an operator can roll the pin
// back to another version in pinnedDigests without a download.
const DefaultRetention = 2

// Installer downloads and manages vendored Codex builds.
//
// Layout is one directory per version under the providers root, holding the
// platform tree the npm package ships:
//
//	<providers>/codex/<version>/bin/codex
//	<providers>/codex/<version>/codex-path/...
//	<providers>/codex/<version>/codex-resources/...
//
// The whole tree is installed rather than the binary alone, because codex
// executes the helpers beside it and a directory holding only `codex` is a
// version that runs and then fails at its first search.
//
// A version is either completely present or absent — never half-written — which
// is what lets Installed() simply list directories.
type Installer struct {
	root string
	// staging is a sibling of root, deliberately outside the directory
	// Installed() enumerates, and on the same filesystem so the publishing
	// rename cannot cross a boundary.
	staging string
	release *releaseClient

	// mu guards inflight, which maps a version to the install currently
	// downloading it.
	mu       sync.Mutex
	inflight map[string]*install
}

// install is one in-flight download that later callers wait on rather than
// repeat.
//
// It outlives whichever caller happened to arrive first. The download runs on a
// context detached from every caller's own, cancelled by the waiter count
// reaching zero — so an install belongs to the set of callers that want it, not
// to the one that started it.
type install struct {
	done   chan struct{}
	result agentic.InstallResult
	err    error

	// cancel stops the download. Called when the last waiter leaves, and again
	// when the download finishes, to release the context.
	cancel context.CancelFunc
	// waiters counts the callers still interested. Guarded by Installer.mu.
	waiters int
}

// InstallerOption configures the installer.
type InstallerOption func(*installerConfig)

type installerConfig struct {
	baseURL string
	client  *http.Client
}

// WithBaseURL points the installer at a different registry, for tests.
func WithBaseURL(url string) InstallerOption {
	return func(c *installerConfig) { c.baseURL = url }
}

// WithHTTPClient replaces the HTTP client.
func WithHTTPClient(client *http.Client) InstallerOption {
	return func(c *installerConfig) { c.client = client }
}

// NewInstaller builds an installer rooted at the providers directory.
//
// The root must be absolute. Path() joins the version onto it and the result is
// executed as-is, so a relative root would produce a relative path — resolved
// against whatever working directory the child happens to inherit, which is the
// PATH-style ambiguity executing by absolute path exists to remove.
func NewInstaller(providersRoot string, opts ...InstallerOption) (*Installer, error) {
	if !filepath.IsAbs(providersRoot) {
		return nil, fmt.Errorf("codex: providers root %q is not an absolute path", providersRoot)
	}

	cfg := installerConfig{baseURL: DefaultRegistryURL}
	for _, opt := range opts {
		opt(&cfg)
	}

	release, err := newReleaseClient(cfg.baseURL, cfg.client)
	if err != nil {
		return nil, err
	}

	return &Installer{
		root:     filepath.Join(providersRoot, ID),
		staging:  filepath.Join(providersRoot, "."+ID+"-staging"),
		release:  release,
		inflight: map[string]*install{},
	}, nil
}

// ErrInvalidVersion means a version string is not a bare version token.
var ErrInvalidVersion = errors.New("invalid version")

// validateVersion refuses anything that is not a bare version token.
//
// Path() joins this into a filesystem path and the release client interpolates
// it into a URL, so an unchecked value escapes both. "../../../../usr/local/bin"
// cleans to a path outside the providers root, where Install's fast path would
// find an existing `codex`, never download anything, and report it as an
// installed version — handing the caller an unverified binary to execute.
func validateVersion(version string) error {
	if version == "" {
		return fmt.Errorf("%w: no version specified", ErrInvalidVersion)
	}
	// Separators first. semver.IsValid would reject these too, but only as a
	// side effect of the grammar — and a check that can never fire is a check
	// nobody can test. This order means the traversal refusal is exercised by
	// the traversal cases, and says so in the error the operator reads.
	if strings.ContainsAny(version, `/\`) || version != filepath.Base(version) {
		return fmt.Errorf("%w: %q contains a path separator", ErrInvalidVersion, version)
	}
	if !semver.IsValid("v" + version) {
		return fmt.Errorf("%w: %q is not a version", ErrInvalidVersion, version)
	}
	return nil
}

// usable reports whether path is a binary that could actually be executed.
//
// Mere existence is not enough: a directory named `codex`, a zero-byte file, or
// one with the execute bit stripped all satisfy os.Stat and none can be run. An
// Install that accepted any of them would be a permanent no-op reporting
// success.
//
// The same predicate answers Driver.Ready, so "installed" and "runnable" cannot
// disagree. Two definitions differing by a permission bit would let Ready call a
// version runnable while Install treats it as absent — re-downloading it and
// then failing to publish over the directory that is already there, on every
// retry, forever.
func usable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && agentic.Executable(info)
}

// Path is where a version's binary lives once installed.
//
// Absolute, and executed as such: there is deliberately no launcher symlink to
// repoint and no PATH lookup to win, so the binary that runs is the one whose
// tarball matched the committed digest.
func (i *Installer) Path(version string) string {
	return filepath.Join(i.root, version, "bin", BinaryName)
}

// Installed lists the versions present, newest first.
func (i *Installer) Installed(context.Context) ([]string, error) {
	entries, err := os.ReadDir(i.root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list installed versions: %w", err)
	}

	var versions []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		// Dot-prefixed directories are never versions. Staging lives elsewhere,
		// but a leftover from an interrupted run must not be reported as an
		// installed version — its tree exists before the digest that justifies
		// it has been checked.
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if validateVersion(entry.Name()) != nil {
			continue
		}
		// A directory without a runnable binary is debris from an interrupted
		// install, not a version anyone can use.
		if !usable(i.Path(entry.Name())) {
			continue
		}
		versions = append(versions, entry.Name())
	}

	sortVersionsNewestFirst(versions)
	return versions, nil
}

// Install downloads and verifies a version, unless it is already present.
//
// The order is the property worth having: download the WHOLE tarball, check it
// against the digest this repository commits, and only then expand it. A
// version whose digest is not committed is refused before any request is made,
// because there would be nothing to judge the answer against.
//
// Concurrent requests for the same version JOIN rather than duplicate. Install
// is reachable over the network, it is synchronous, and it downloads ~110 MB.
// Without this, a handful of impatient retries — the entirely normal response to
// a call that appears to hang — each open their own staging directory and pull
// the whole payload.
func (i *Installer) Install(ctx context.Context, version string) (agentic.InstallResult, error) {
	if err := validateVersion(version); err != nil {
		return agentic.InstallResult{}, err
	}
	// Refused here rather than after the download, so an unpinned version costs
	// nothing and says the same thing whether or not the network is reachable.
	if _, err := PinnedDigest(version, i.release.platform); err != nil {
		return agentic.InstallResult{}, err
	}

	i.mu.Lock()
	inst, joined := i.inflight[version]
	if !joined {
		// Detached from this caller's context, so the download is not hostage
		// to whoever happened to arrive first. It is still bounded: the waiter
		// count reaching zero cancels it, so a caller giving up when nobody
		// else is waiting stops the work exactly as before.
		runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		inst = &install{done: make(chan struct{}), cancel: cancel}
		i.inflight[version] = inst
		go i.run(runCtx, version, inst)
	}
	inst.waiters++
	i.mu.Unlock()

	// Every caller waits the same way, first or not.
	//
	// Any asymmetry here hands the first caller the power to cancel everyone
	// else's install: it gives up, the download aborts, and every other caller
	// receives a cancellation for work it asked for and is still waiting on.
	select {
	case <-inst.done:
		// Whatever the download got, including its failure: a second attempt
		// started now would fail the same way.
		return inst.result, inst.err
	case <-ctx.Done():
		i.leave(inst, version)
		return agentic.InstallResult{}, ctx.Err()
	}
}

// run performs the download and publishes its outcome to everyone waiting.
func (i *Installer) run(ctx context.Context, version string, inst *install) {
	result, err := i.installOnce(ctx, version)

	// Removed from the map before the result is published, so a caller arriving
	// afterwards starts a fresh attempt rather than joining one that has
	// already finished. That attempt costs nothing when the install succeeded:
	// installOnce reports an already-present version without downloading.
	i.forget(version, inst)

	inst.result, inst.err = result, err
	close(inst.done)
	// Releases the context. The work is over, so this cancels nothing.
	inst.cancel()
}

// leave records that a caller has stopped waiting, and stops the download if it
// was the last one.
//
// The map entry goes FIRST, while the lock is held. A download being cancelled
// is not something a later caller can join: it is unwinding, and it will
// publish the cancellation that stopped it. A caller arriving in that window
// with a perfectly healthy context would be handed a context.Canceled for an
// install nobody asked it to stop.
func (i *Installer) leave(inst *install, version string) {
	i.mu.Lock()
	inst.waiters--
	last := inst.waiters == 0
	if last && i.inflight[version] == inst {
		delete(i.inflight, version)
	}
	i.mu.Unlock()

	if last {
		inst.cancel()
	}
}

// forget removes an install from the map, but only if it is still the current
// one for that version.
//
// The identity check matters because leave may already have removed it and a
// later caller may have started a fresh download under the same version. An
// unconditional delete would evict that newcomer from the map while its
// goroutine ran, so the next caller would start a THIRD download of a version
// already being fetched — the duplication single-flighting exists to prevent.
func (i *Installer) forget(version string, inst *install) {
	i.mu.Lock()
	if i.inflight[version] == inst {
		delete(i.inflight, version)
	}
	i.mu.Unlock()
}

func (i *Installer) installOnce(ctx context.Context, version string) (agentic.InstallResult, error) {
	target := i.Path(version)
	if usable(target) {
		return agentic.InstallResult{Version: version, Path: target, AlreadyPresent: true}, nil
	}

	// Staged OUTSIDE the directory Installed() enumerates, and beside it so the
	// publishing rename stays on one filesystem.
	//
	// A staging directory inside the enumerated one holds a `bin/codex` created
	// before the archive is known to be the pinned one: Installed() would list
	// it as a version and Path() would hand out a runnable path to it. Prune
	// walks the same list, so it would also delete the staging directory of an
	// install still in flight.
	if err := i.prepare(i.root); err != nil {
		return agentic.InstallResult{}, err
	}
	if err := i.prepare(i.staging); err != nil {
		return agentic.InstallResult{}, err
	}
	staging, err := os.MkdirTemp(i.staging, version+"-")
	if err != nil {
		return agentic.InstallResult{}, fmt.Errorf("create a staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(staging) }()

	// The archive lands on disk before anything is expanded from it. It is
	// removed with the staging directory whatever happens, so an unverified
	// tarball is never left where a later install could mistake it for work
	// already done.
	archive, err := os.CreateTemp(staging, "package-*.tgz")
	if err != nil {
		return agentic.InstallResult{}, fmt.Errorf("create a staging file: %w", err)
	}
	defer func() { _ = archive.Close() }()

	if err := i.release.Fetch(ctx, version, archive); err != nil {
		return agentic.InstallResult{}, err
	}
	if _, err := archive.Seek(0, 0); err != nil {
		return agentic.InstallResult{}, fmt.Errorf("rewind the downloaded package: %w", err)
	}

	tree := filepath.Join(staging, version)
	if err := os.Mkdir(tree, 0o700); err != nil {
		return agentic.InstallResult{}, fmt.Errorf("create %s: %w", tree, err)
	}
	if err := extract(archive, tree); err != nil {
		return agentic.InstallResult{}, err
	}
	if err := syncDir(tree); err != nil {
		return agentic.InstallResult{}, err
	}

	// Published by renaming the whole tree: a reader either sees no version or
	// sees a complete, verified one.
	if err := os.Rename(tree, filepath.Join(i.root, version)); err != nil {
		if !errors.Is(err, os.ErrExist) && !errors.Is(err, syscall.ENOTEMPTY) {
			return agentic.InstallResult{}, fmt.Errorf("install %s: %w", version, err)
		}

		// Something already occupies the destination. If it is a usable binary,
		// another process won the race and its copy was checked against the
		// same digest.
		//
		// If it is NOT — an empty directory left by an interrupted install, say
		// — then reporting success would be a lie: the verified tree is about to
		// be discarded by the deferred cleanup, and Path() would point at a file
		// that does not exist. Every later attempt would repeat it, so the
		// caller would be permanently stuck with a "present" version it cannot
		// execute.
		if usable(target) {
			return agentic.InstallResult{Version: version, Path: target, AlreadyPresent: true}, nil
		}
		return agentic.InstallResult{}, fmt.Errorf(
			"install %s: %s already exists but holds no usable binary; remove it and retry: %w",
			version, filepath.Join(i.root, version), err)
	}

	if err := syncDir(i.root); err != nil {
		return agentic.InstallResult{}, err
	}

	return agentic.InstallResult{Version: version, Path: target}, nil
}

// prepare makes a directory this process can trust to hold only what it put
// there.
//
// MkdirAll succeeds on a directory that already exists, whoever owns it and
// whatever its mode, so creating one proves nothing about it. The checks after
// it are what make the trust real, and they matter more here than anywhere else
// in this package: what lands under these directories is executed. A providers
// root another account owns, or one open to another account, is somewhere a
// binary can be substituted between the rename that published it and the fork
// that runs it — and the digest checked on the way in says nothing about a file
// replaced afterwards.
func (i *Installer) prepare(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}

	// Lstat, not Stat: a symlink standing where the directory should be is the
	// thing being refused, and Stat would report whatever it points at.
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", dir, err)
	}
	switch {
	case !info.IsDir():
		return fmt.Errorf("%s is not a directory", dir)
	case !ownedByCaller(info):
		return fmt.Errorf("%s belongs to another user", dir)
	case !privateToCaller(info):
		return fmt.Errorf("%s is open to other users (mode %#o)", dir, info.Mode().Perm())
	}
	return nil
}

// syncDir flushes a directory entry, so a rename survives a power cut.
func syncDir(path string) error {
	dir, err := os.Open(path) // #nosec G304 -- a directory this package created
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = dir.Close() }()

	if err := dir.Sync(); err != nil {
		return fmt.Errorf("flush %s: %w", path, err)
	}
	return nil
}

// Prune removes all but the newest keep versions, never removing protected ones.
//
// protected is the pinned version and anything else the caller must not lose.
// Retention is about disk, not about tidiness: ~300 MB per version is the
// constraint, and deleting the version the caller is configured to run would
// trade a full disk for a broken install.
func (i *Installer) Prune(ctx context.Context, keep int, protected ...string) error {
	if keep < 1 {
		keep = 1
	}

	installed, err := i.Installed(ctx)
	if err != nil {
		return err
	}

	// Keep the newest `keep`, PLUS any protected version not already among them.
	//
	// Expressed in that order deliberately. A combined budget of
	// keep+len(protected) lets a protected version that is ALREADY among the
	// newest buy a slot it never uses, so keep=2 retains three — nearly a
	// gigabyte of the disk the policy exists to protect — and duplicates in
	// protected multiply it.
	keepSet := make(map[string]struct{}, keep+len(protected))
	for _, v := range installed {
		if len(keepSet) >= keep {
			break
		}
		keepSet[v] = struct{}{}
	}

	// Protected versions survive whether or not they are recent. Only ones that
	// are actually installed matter; the rest are not ours to keep.
	installedSet := make(map[string]struct{}, len(installed))
	for _, v := range installed {
		installedSet[v] = struct{}{}
	}
	for _, v := range protected {
		if _, ok := installedSet[v]; ok {
			keepSet[v] = struct{}{}
		}
	}

	// Swept over every version-shaped directory, not just the runnable ones.
	//
	// Installed() reports what can be RUN, which is what its callers need and
	// deliberately excludes a directory whose binary is missing or not
	// executable. Pruning the same list would leave that directory forever: it
	// is never listed, so it is never removed, and it is never removed, so the
	// rename that publishes a fresh copy of that version keeps failing. A
	// version could be permanently unfixable through this API while occupying
	// most of a disk the retention policy exists to protect.
	present, err := i.versionDirs()
	if err != nil {
		return err
	}
	for _, v := range present {
		if _, ok := keepSet[v]; ok {
			continue
		}
		if slices.Contains(protected, v) {
			// Never the version about to be executed, even as debris: an
			// install may be publishing it right now, and a half-finished
			// directory is repaired by installing over it.
			continue
		}
		if err := os.RemoveAll(filepath.Join(i.root, v)); err != nil {
			return fmt.Errorf("remove %s: %w", v, err)
		}
	}
	return nil
}

// versionDirs lists every version-shaped directory under the root, whether or
// not it holds a runnable binary.
//
// The counterpart to Installed(): that one answers "what can I run", this one
// answers "what is taking up space".
func (i *Installer) versionDirs() ([]string, error) {
	entries, err := os.ReadDir(i.root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list version directories: %w", err)
	}

	var versions []string
	for _, entry := range entries {
		// Dot-prefixed names are never versions, and validateVersion refuses
		// anything that is not a bare version token — so nothing outside this
		// package's own layout is ever a candidate for removal.
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if validateVersion(entry.Name()) != nil {
			continue
		}
		versions = append(versions, entry.Name())
	}
	return versions, nil
}

// sortVersionsNewestFirst orders semver-ish versions, newest first.
//
// Versions here are bare ("0.153.4"); semver.Compare wants a leading "v".
// Anything unparseable sorts last rather than being dropped.
func sortVersionsNewestFirst(versions []string) {
	sort.SliceStable(versions, func(a, b int) bool {
		va, vb := "v"+versions[a], "v"+versions[b]
		switch {
		case semver.IsValid(va) && !semver.IsValid(vb):
			return true
		case !semver.IsValid(va) && semver.IsValid(vb):
			return false
		case !semver.IsValid(va) && !semver.IsValid(vb):
			return versions[a] > versions[b]
		default:
			return semver.Compare(va, vb) > 0
		}
	})
}
