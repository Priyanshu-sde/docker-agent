//go:build !windows

package board

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockFile keeps the flock'd file referenced for the process lifetime; if
// it were garbage collected, its finalizer would close the descriptor and
// drop the lock.
var lockFile *os.File

// acquireLock takes an exclusive, non-blocking lock on path so only one
// board owns the cards and their sessions: a second instance would run its
// own watchers and race this one relaunching agents. The lock is held for
// the process lifetime and released by the OS on exit, so a crash never
// leaves a stale lock behind.
func acquireLock(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open board lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return errors.New("another docker agent board is already running")
	}
	lockFile = f
	return nil
}
