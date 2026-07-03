//go:build unix

package database

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// lockFileExclusive attempts to acquire an exclusive advisory lock without
// blocking. The retry loop in FileLock.Lock handles waiting and cancellation.
func lockFileExclusive(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

func unlockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}

func isLockUnavailable(err error) bool {
	return errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN)
}
