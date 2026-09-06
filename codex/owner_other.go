//go:build !unix

package codex

import "io/fs"

// ownedByCaller cannot read a directory's owner on this platform, so the schema
// directory is taken on trust.
//
// The platform this serves is Windows, where the trust is placed in the
// operating system: each account's temporary directory is its own, so there is
// no shared location for another user to squat in. A platform that instead
// points every account at one temporary directory gets no protection from this
// package, only from the filesystem's own permissions.
func ownedByCaller(fs.FileInfo) bool { return true }

// privateToCaller cannot read a directory's permissions on this platform.
//
// fs.FileInfo here carries a mode synthesised from file attributes rather than
// an access-control list, and every directory reports 0777 through it, so a
// permission test would refuse the directory this package had just created and
// make a schema-constrained run impossible.
func privateToCaller(fs.FileInfo) bool { return true }

// callerID has no uid to report here. The constant keeps the directory name
// well formed; on Windows the platform's own per-user temporary directory does
// the separating.
func callerID() int { return 0 }
