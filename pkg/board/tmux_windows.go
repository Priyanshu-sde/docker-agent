//go:build windows

package board

import "os"

// checkOwner is a no-op on Windows: the board requires tmux and cannot run
// there, but the package must still compile.
func checkOwner(string, os.FileInfo) error { return nil }
