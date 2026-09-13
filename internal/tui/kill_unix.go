//go:build unix

package tui

import (
	"os/exec"
	"syscall"
)

// killProcessGroup SIGTERMs cmd's whole process group — see kill's own doc
// comment for why the group, not just the process. Falling back to the bare
// process covers the case where it never became a group leader.
func killProcessGroup(cmd *exec.Cmd) {
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
}
