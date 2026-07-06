package board

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/paths"
)

func TestNormalizeProjectPath(t *testing.T) {
	abs, err := normalizeProjectPath("/some/repo")
	require.NoError(t, err)
	assert.Equal(t, "/some/repo", abs)

	// Empty and blank paths are rejected: they would silently validate
	// against the board's working directory.
	_, err = normalizeProjectPath("")
	require.Error(t, err)
	_, err = normalizeProjectPath("   ")
	require.Error(t, err)

	// A leading ~ expands to the home directory.
	abs, err = normalizeProjectPath("~/src/repo")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(paths.GetHomeDir(), "src", "repo"), abs)

	// Relative paths are anchored to the current directory.
	abs, err = normalizeProjectPath("repo")
	require.NoError(t, err)
	assert.True(t, filepath.IsAbs(abs))
}
