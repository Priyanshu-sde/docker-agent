//go:build !windows

package board

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAcquireLockIsExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "board.lock")

	require.NoError(t, acquireLock(path))
	held := lockFile
	t.Cleanup(func() { _ = held.Close(); lockFile = nil })

	// A second acquisition (a second board) is rejected while the first
	// lock is held.
	err := acquireLock(path)
	require.ErrorContains(t, err, "already running")

	// Releasing the first lock frees the state for the next instance.
	require.NoError(t, held.Close())
	require.NoError(t, acquireLock(path))
	_ = lockFile.Close()
	lockFile = nil
}
