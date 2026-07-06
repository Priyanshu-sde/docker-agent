//go:build !windows

package board

import (
	"fmt"
	"os"
	"syscall"
)

// checkOwner verifies that the tmux socket directory belongs to the current
// user, so the board never binds its private tmux server inside a directory
// another local user pre-created.
func checkOwner(dir string, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("tmux socket dir %s is not owned by the current user", dir)
	}
	return nil
}
