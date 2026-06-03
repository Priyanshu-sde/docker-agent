//go:build windows

package selfupdate

import (
	"fmt"
	"os"
	"os/exec"
)

// swapBinary replaces dst with src on Windows.
//
// Windows locks a running executable's file, so we cannot overwrite dst
// directly. Instead we rename the running binary out of the way (to a sidecar
// ".old" path, which Windows allows even while the file is mapped) and move the
// new binary into its place. The stale ".old" file is best-effort cleaned up;
// if it is still locked it lingers harmlessly until the next run.
func swapBinary(dst, src string) error {
	old := dst + ".old"
	_ = os.Remove(old)

	if err := os.Rename(dst, old); err != nil {
		return fmt.Errorf("moving current binary aside: %w", err)
	}

	if err := os.Rename(src, dst); err != nil {
		if cpErr := atomicWriteFromFile(dst, src); cpErr != nil {
			// Roll back so we never leave the install without a binary.
			if rbErr := os.Rename(old, dst); rbErr != nil {
				return fmt.Errorf("installing new binary: %w (copy fallback failed: %v; rollback also failed: %v)", err, cpErr, rbErr)
			}
			return fmt.Errorf("installing new binary: %w (copy fallback failed: %v)", err, cpErr)
		}
		_ = os.Remove(src)
	}

	_ = os.Remove(old)
	return nil
}

// reExecProcess relaunches path as a child process inheriting our stdio, waits
// for it, and exits with its status. Windows has no execve, so the parent
// process must stay alive until the child finishes to preserve exit-code
// semantics for the caller (e.g. a shell or the docker CLI).
func reExecProcess(path string, args, env []string) error {
	var childArgs []string
	if len(args) > 1 {
		childArgs = args[1:]
	}

	cmd := exec.Command(path, childArgs...) //nolint:gosec // path is our own freshly installed binary
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if ok := asExitError(err, &exitErr); ok {
			os.Exit(exitErr.ExitCode())
		}
		return fmt.Errorf("running updated binary: %w", err)
	}

	os.Exit(0)
	return nil
}

// asExitError is a tiny helper kept separate so exec_unix.go does not need to
// import errors solely for this Windows branch.
func asExitError(err error, target **exec.ExitError) bool {
	if e, ok := err.(*exec.ExitError); ok { //nolint:errorlint // direct type assertion is intentional here
		*target = e
		return true
	}
	return false
}
