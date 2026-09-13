//go:build windows

package tui

import "os/exec"

// killProcessGroup terminates cmd's process. Windows has no POSIX process
// group to signal — this path is currently unreachable in practice anyway,
// since startPtyEmulator's pty.StartWithSize has no real Windows backend and
// fails before a terminal (and its cmd) ever exists.
func killProcessGroup(cmd *exec.Cmd) {
	_ = cmd.Process.Kill()
}
