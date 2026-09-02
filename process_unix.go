//go:build unix

package agentic

import (
	"os/exec"
	"syscall"
)

// killWholeGroup puts the child in its own process group and makes cancellation
// signal the whole group, so a CLI's own children die with it.
func killWholeGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// The negative PID is what reaches every process in the group.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
