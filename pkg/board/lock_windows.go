//go:build windows

package board

// acquireLock is a no-op on Windows: the board requires tmux and cannot run
// there, but the package must still compile.
func acquireLock(string) error { return nil }
