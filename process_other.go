//go:build !unix

package agentic

import "os/exec"

// killWholeGroup leaves cancellation to exec.CommandContext, which signals only
// the process the driver started. A CLI that spawns children of its own can
// therefore outlive a cancelled run on this platform.
func killWholeGroup(*exec.Cmd) {}
