package codex

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// PinnedVersion is the Codex build this package is written against.
//
// Every fixture in testdata was captured from it, and parse.go decodes the
// event stream those fixtures hold. Codex ships often, so an unpinned CLI moves
// that schema between one run and the next; pinned, a schema change is a
// failing decoder test at the bump rather than a parse failure on a user's
// machine.
const PinnedVersion = "0.153.4"

// DefaultRegistryURL is the npm registry the pinned build is fetched from.
//
// npm rather than the GitHub release, because it is the only channel that
// carries the same artifact for every platform this package vendors on. The
// GitHub release publishes sigstore bundles for its linux-musl assets alone, so
// a darwin install taken from there could be checked against nothing but an
// unsigned checksum file served from the same host.
const DefaultRegistryURL = "https://registry.npmjs.org"

// npmPackage is the package the pinned build is published under. The
// per-platform builds are prerelease-suffixed versions of this same package,
// pulled in by the wrapper as optional dependencies.
const npmPackage = "@openai/codex"

// pinnedDigests is the trust anchor: for each version this package will
// install, the SHA-512 of each platform's tarball, in npm's own Subresource
// Integrity spelling.
//
// These are the values `npm view @openai/codex@<version>-<platform> dist.integrity`
// prints, committed verbatim so an operator can compare them without decoding
// anything. Changing this table is a deliberate act visible in a diff, which is
// what makes it worth more than the registry metadata it was copied from:
// checking a download against a digest the same server just served proves the
// bytes arrived intact, not that they are the bytes anyone chose.
//
// A version absent from this table cannot be installed. Old versions stay
// listed so an operator can roll the pin back to a build whose digests were
// captured at the time, which is what the retention policy keeps on disk for.
var pinnedDigests = map[string]map[string]string{
	"0.153.4": {
		"darwin-arm64": "sha512-B1qhN3fa1ay0R0wGziXqgwSkB5icpYChNKHhtBHff/0UtSTC7z+l8aTtvMlGjH3E8HEvY3+njIJelM9CAAoVWg==",
		"darwin-x64":   "sha512-vnSbbPzfoDZmmyzsxswsDDXQ06IVFBzkQU7/hroB3ji93Ok2utcsq8Psfk2tjF5r9mEx8RWFJhzuTGHG26/NDA==",
		"linux-arm64":  "sha512-QKdjYLYV4hXIuUQDP3P6F4NXuWFoKo9WUoV4nAREIx55kiUyi8UsYdsVobkeXir5n/maEQgYMCKLHVma4rNPiw==",
		"linux-x64":    "sha512-x1EcwBlY3AObM1VTUHNM2AzAJQsyreGdagpF+qFiYi/Oa30VBktvvG0C6tLtCzqW6hjZNWkGZQWmeVk7MuJKWg==",
	},
}

// maxTarballBytes bounds the download. The largest platform tarball is ~110 MB.
const maxTarballBytes = 1 << 30 // 1 GiB

// maxExtractedBytes bounds what the archive is allowed to expand to. The
// largest platform tree is ~400 MB unpacked, and the check exists because the
// digest says the bytes are the ones that were pinned without saying what they
// decompress to.
const maxExtractedBytes = 2 << 30 // 2 GiB

// vendorPrefix is the directory inside the tarball holding the platform tree.
// Everything below it is installed; the wrapper's own package.json and README
// describe the npm packaging rather than the CLI, and nothing runs them.
const vendorPrefix = "package/vendor/"

var (
	// ErrUnpinnedVersion means a version has no committed digests, so there is
	// nothing to check a download against. Never recoverable by retrying: the
	// missing thing is in this repository, not on the network.
	ErrUnpinnedVersion = errors.New("version is not pinned in this build")

	// ErrDigestMismatch means the downloaded tarball is not the one the pin
	// names.
	ErrDigestMismatch = errors.New("downloaded tarball does not match the pinned digest")

	// ErrPlatformUnsupported means this package does not vendor a build for
	// this machine.
	ErrPlatformUnsupported = errors.New("no vendored Codex build for this platform")

	// ErrMalformedArchive means the tarball does not hold the tree a Codex
	// platform package is supposed to hold.
	ErrMalformedArchive = errors.New("codex package archive is not the expected shape")
)

// PlatformKey is how npm names this machine's build.
//
// npm uses Node's names (x64, darwin) rather than Go's (amd64), so the two are
// translated rather than assumed equal — an assumption that would fail only on
// amd64, which is exactly where nobody would notice while developing on Apple
// silicon.
//
// Windows is deliberately absent. A vendored install is only worth anything if
// the binary it publishes can be recognised as runnable, and the predicate that
// decides that reads an execute bit no Windows file carries — so a vendored
// Codex there would install correctly and then be reported as missing forever.
// NewOnPath runs the codex that is already installed, which is the working
// answer on that platform.
func PlatformKey(goos, goarch string) (string, error) {
	arch, ok := map[string]string{"amd64": "x64", "arm64": "arm64"}[goarch]
	if !ok {
		return "", fmt.Errorf("%w: architecture %s", ErrPlatformUnsupported, goarch)
	}

	switch goos {
	case "darwin", "linux":
		return goos + "-" + arch, nil
	default:
		return "", fmt.Errorf("%w: %s", ErrPlatformUnsupported, goos)
	}
}

// PinnedDigest is the committed digest for a version on a platform.
func PinnedDigest(version, platform string) (string, error) {
	byPlatform, ok := pinnedDigests[version]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrUnpinnedVersion, version)
	}
	digest, ok := byPlatform[platform]
	if !ok {
		return "", fmt.Errorf("%w: %s has no %s build pinned", ErrPlatformUnsupported, version, platform)
	}
	return digest, nil
}

// releaseClient fetches and verifies platform packages.
type releaseClient struct {
	baseURL  string
	client   *http.Client
	platform string
}

func newReleaseClient(baseURL string, httpClient *http.Client) (*releaseClient, error) {
	platform, err := PlatformKey(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return nil, err
	}

	if httpClient == nil {
		// Generous: the platform tarball is ~110 MB, and a machine on a
		// domestic connection is a supported deployment.
		httpClient = &http.Client{Timeout: 30 * time.Minute}
	}

	return &releaseClient{
		baseURL:  strings.TrimSuffix(baseURL, "/"),
		client:   httpClient,
		platform: platform,
	}, nil
}

// tarballURL is where npm serves a platform package.
func (c *releaseClient) tarballURL(version string) string {
	// The scoped name appears unescaped in the path and escaped in the
	// filename, which is npm's own layout rather than a choice available here.
	return fmt.Sprintf("%s/%s/-/codex-%s-%s.tgz", c.baseURL, npmPackage, version, c.platform)
}

// Fetch downloads a version's platform package into w and refuses any bytes but
// the ones the pin names.
//
// The digest is checked over the WHOLE stream before w is used for anything
// else, which is why this writes an archive to a file rather than extracting as
// it reads: expanding an archive is executing its table of contents, and doing
// that to unverified bytes hands the decision of what lands on disk to whoever
// served them.
func (c *releaseClient) Fetch(ctx context.Context, version string, w io.Writer) error {
	want, err := PinnedDigest(version, c.platform)
	if err != nil {
		return err
	}

	url := c.tarballURL(version)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build the download request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch %s: %s", url, resp.Status)
	}

	digest := sha512.New()
	if _, err := io.Copy(io.MultiWriter(w, digest), io.LimitReader(resp.Body, maxTarballBytes)); err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}

	got := "sha512-" + base64.StdEncoding.EncodeToString(digest.Sum(nil))
	if got != want {
		return fmt.Errorf("%w: got %s, the pin says %s", ErrDigestMismatch, got, want)
	}
	return nil
}

// extract unpacks a verified platform package into dest.
//
// The tarball holds one vendor directory named for a target triple, and the
// triple is READ rather than composed from GOOS and GOARCH: the mapping from a
// Go platform to Rust's target names is a fact about how OpenAI builds Codex,
// and one written from memory here would be a second place for it to be wrong.
// Exactly one is required, because a tarball holding two says the archive is
// not what this function was written against and there is no basis for picking
// between them.
//
// Every entry is refused unless it is a regular file or a directory under that
// one prefix. A tar is a list of paths a stranger chose: a symlink, a device
// node, or a name that walks out of dest with ".." are all things an archive can
// ask for, and the digest that let this archive through says only that it is
// the archive that was pinned.
func extract(r io.Reader, dest string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrMalformedArchive, err)
	}
	defer func() { _ = gz.Close() }()

	var (
		tr        = tar.NewReader(gz)
		triple    string
		budget    = int64(maxExtractedBytes)
		sawBinary bool
	)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: %w", ErrMalformedArchive, err)
		}

		// path.Clean, not filepath.Clean: a tar header's name is a slash
		// separated path whatever the host separator is, and cleaning it with
		// the host's rules leaves "a\\..\\b" intact on unix.
		name := path.Clean(header.Name)

		// An entry naming somewhere outside the archive is refused before the
		// prefix test rather than skipped by it.
		//
		// The prefix test cannot tell the two apart: it skips the wrapper's own
		// package.json and README, which legitimately sit outside the vendor
		// tree, and a name that cleans to "../etc/cron.d/evil" is outside it in
		// exactly the same way. Skipping such an entry is safe — nothing is
		// written — but it is safe by accident, and an archive that asked to
		// write outside its own root is not one to keep unpacking.
		if path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("%w: entry %q names a path outside the archive", ErrMalformedArchive, header.Name)
		}

		if !strings.HasPrefix(name, vendorPrefix) {
			continue
		}
		rest := strings.TrimPrefix(name, vendorPrefix)
		dir, inner, ok := strings.Cut(rest, "/")
		if !ok || dir == "" {
			// The triple directory's own entry carries no file.
			continue
		}
		switch {
		case triple == "":
			triple = dir
		case dir != triple:
			return fmt.Errorf("%w: it holds builds for both %s and %s", ErrMalformedArchive, triple, dir)
		}

		// Cleaned above, so "..", an absolute name and a doubled separator are
		// already normalised; what remains is a name that walks upwards from
		// the prefix, which cleaning preserves and this refuses.
		if inner == "" || inner == ".." || strings.HasPrefix(inner, "../") {
			return fmt.Errorf("%w: entry %q escapes the package", ErrMalformedArchive, header.Name)
		}
		target := filepath.Join(dest, filepath.FromSlash(inner))

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return fmt.Errorf("create %s: %w", target, err)
			}
		case tar.TypeReg:
			if header.Size > budget {
				return fmt.Errorf("%w: it expands to more than %d bytes", ErrMalformedArchive, maxExtractedBytes)
			}
			budget -= header.Size
			if err := writeFile(target, tr, header.Size, header.FileInfo().Mode()); err != nil {
				return err
			}
			if inner == "bin/"+BinaryName {
				sawBinary = true
			}
		default:
			// A link, a device or a fifo. None of them is part of a Codex
			// platform package, and each is a way for an archive to reach
			// something this function did not create.
			return fmt.Errorf("%w: entry %q is not a regular file or directory", ErrMalformedArchive, header.Name)
		}
	}

	if !sawBinary {
		return fmt.Errorf("%w: it holds no %s", ErrMalformedArchive, vendorPrefix+"<target>/bin/"+BinaryName)
	}
	return nil
}

// writeFile creates one extracted file, owner-only, and flushes it.
//
// The mode is narrowed to the owner rather than copied: the archive's group and
// other bits describe the machine that built it, and honouring them would open
// a tree this package has just promised is its own. The execute bit is carried
// across, because the tree holds the helper programs Codex expects to run
// beside its own binary and stripping it would install a CLI that cannot work.
func writeFile(target string, r io.Reader, size int64, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(target), err)
	}

	perm := os.FileMode(0o600)
	if mode.Perm()&0o111 != 0 {
		perm = 0o700
	}
	// G304: target is composed from a directory this package created and a name
	// checked above to stay inside it. G302: 0700 is the minimum for a file
	// that has to be executable, and it is owner-only.
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm) // #nosec G304,G302 -- path confined to our own staging tree; 0700 is minimal for an executable
	if err != nil {
		return fmt.Errorf("create %s: %w", target, err)
	}

	// Bounded by the header's own size rather than copied to exhaustion, so a
	// stream that keeps going past what the entry declared cannot fill the disk
	// under a name the budget already accounted for.
	written, err := io.Copy(file, io.LimitReader(r, size))
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("write %s: %w", target, err)
	}
	if written != size {
		_ = file.Close()
		return fmt.Errorf("%w: %s is %d bytes, its header says %d", ErrMalformedArchive, target, written, size)
	}
	// Flushed before the directory rename publishes the version. A power cut
	// can otherwise make the rename durable while the contents are not, leaving
	// a truncated binary that every later install treats as already present.
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("flush %s: %w", target, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", target, err)
	}
	return nil
}
