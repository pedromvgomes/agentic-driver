//go:build unix

package codex

import (
	"io/fs"
	"os"
	"syscall"
)

// ownedByCaller reports whether a directory belongs to the user running this
// process.
//
// The schema directory is trusted to hold only what this process put there, and
// on a shared machine that trust has to be earned rather than assumed: MkdirAll
// succeeds on a directory that already exists whoever owns it, so without this
// a directory another account created first would be adopted silently.
func ownedByCaller(info fs.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return int(stat.Uid) == os.Getuid()
}

// privateToCaller reports whether a directory admits nobody but its owner.
//
// Ownership alone is not enough: a directory this user owns and every user can
// write is a directory anything on the machine can put a schema into.
//
// It lives beside ownedByCaller because both read a permission model only some
// platforms have. Elsewhere fs.FileInfo carries a mode synthesised from file
// attributes rather than an access-control list — every directory reports 0777
// — and a check written against that would refuse the directory it had just
// created.
func privateToCaller(info fs.FileInfo) bool {
	return info.Mode().Perm()&0o077 == 0
}

// callerID names the user in a directory name, so two accounts on one host
// never contend for the same path.
func callerID() int { return os.Getuid() }

// schemaRoot is the directory the schema directory is created under.
//
// The system temporary directory is shared between accounts here, which is why
// the directory beneath it carries a uid and is checked before it is used.
func schemaRoot() (string, error) { return os.TempDir(), nil }
