//go:build unix

package persona

import (
	"fmt"
	"os"
	"syscall"
)

// lockFile takes an exclusive advisory lock on path (creating it if
// missing), blocking until it's free, and returns a func to release it —
// see SetEnabled's own doc comment for what this protects against. Held
// only by cooperating BARON processes; it has no effect on anything else
// touching the file.
func lockFile(path string) (unlock func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
