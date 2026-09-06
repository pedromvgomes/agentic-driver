//go:build !unix

package codex

import "io/fs"

// ownedByCaller cannot read a directory's owner on this platform, so the
// schema directory is taken on trust.
//
// The exposure it would otherwise close is a shared world-writable temporary
// directory, which is a Unix arrangement; a platform that gives each user their
// own is not vulnerable to the squatting this guards against.
func ownedByCaller(fs.FileInfo) bool { return true }

// callerID has no uid to report here. The constant keeps the directory name
// well formed; the platform's own per-user temporary directory does the
// separating.
func callerID() int { return 0 }
