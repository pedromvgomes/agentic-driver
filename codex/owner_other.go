//go:build !unix

package codex

import (
	"fmt"
	"io/fs"
	"os"
)

// ownedByCaller cannot read a directory's owner on this platform, so the schema
// directory is taken on trust, and schemaRoot is what makes that trust safe: it
// names a location the operating system already separates per account.
//
// Every platform outside the unix build tag lands in this file; Windows is the
// one that runs a codex binary. Trusting the SYSTEM TEMPORARY directory here
// would be wrong even there — a service or scheduled task runs without an
// interactive profile, where TMP resolves to a machine-wide directory any
// account may create subdirectories in — so the cache directory is used
// instead.
func ownedByCaller(fs.FileInfo) bool { return true }

// privateToCaller cannot read a directory's permissions on this platform.
//
// fs.FileInfo here carries a mode synthesised from file attributes rather than
// an access-control list, and every directory reports 0777 through it, so a
// permission test would refuse the directory this package had just created and
// make a schema-constrained run impossible.
func privateToCaller(fs.FileInfo) bool { return true }

// callerID has no uid to report here. The constant keeps the directory name
// well formed; schemaRoot is what separates one account from another.
func callerID() int { return 0 }

// schemaRoot is the per-account cache directory.
//
// It is the platform's own answer to "where does this user keep its files",
// which is what the checks in the unix build establish by hand. A caller whose
// environment names no such directory is told so rather than quietly falling
// back to somewhere shared.
func schemaRoot() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locating a private directory for the schema: %w", err)
	}
	return dir, nil
}
