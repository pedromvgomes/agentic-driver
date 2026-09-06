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

// callerID names the user in a directory name, so two accounts on one host
// never contend for the same path.
func callerID() int { return os.Getuid() }
