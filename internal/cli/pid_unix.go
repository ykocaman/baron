//go:build unix

package cli

import (
	"errors"
	"os"
	"syscall"
)

// pidAlive reports whether a process id is still running. signal 0 performs
// only the permission-and-existence check the kernel does for a real signal,
// without delivering one; ESRCH is the "no such process" answer this needs,
// while EPERM means it exists but belongs to someone else — still alive.
func pidAlive(pid int) bool {
	proc, err := os.FindProcess(pid) // never fails on unix
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}
