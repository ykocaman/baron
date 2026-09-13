//go:build windows

package cli

import "golang.org/x/sys/windows"

// pidAlive reports whether a process id is still running. Windows has no
// signal-0 equivalent, so this opens a query-only handle and checks whether
// the process has an exit code yet — STILL_ACTIVE means it hasn't.
func pidAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)

	var exitCode uint32
	if err := windows.GetExitCodeProcess(h, &exitCode); err != nil {
		return false
	}
	return exitCode == uint32(windows.STATUS_PENDING)
}
