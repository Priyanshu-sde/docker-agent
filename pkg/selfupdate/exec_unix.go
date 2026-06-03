//go:build !windows

package selfupdate

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// swapBinary replaces dst with src on Unix. A running executable's inode can be
// replaced while it is in use, so a rename within the same filesystem is both
// safe and atomic. If src and dst live on different filesystems (the temp-dir
// fallback), os.Rename fails with EXDEV and we copy-then-replace instead.
func swapBinary(dst, src string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	} else if !errors.Is(err, syscall.EXDEV) {
		return err
	}
	// Cross-filesystem fallback: copy the contents atomically.
	if err := atomicWriteFromFile(dst, src); err != nil {
		return err
	}
	_ = os.Remove(src)
	return nil
}

// reExecProcess replaces the current process image with path using execve.
// On success it never returns: the new binary inherits our PID, file
// descriptors and terminal, so the user sees a seamless restart.
func reExecProcess(path string, args, env []string) error {
	if len(args) == 0 {
		args = []string{path}
	} else {
		// argv[0] should be the new binary's path.
		args = append([]string{path}, args[1:]...)
	}
	if err := syscall.Exec(path, args, env); err != nil {
		return fmt.Errorf("exec %s: %w", path, err)
	}
	return nil // unreachable on success
}
