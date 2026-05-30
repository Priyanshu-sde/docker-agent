package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/paths"
)

func TestCreateWorktree(t *testing.T) {
	dir := bootstrapRepo(t)
	dataDir := t.TempDir()
	paths.SetDataDir(dataDir)
	t.Cleanup(func() { paths.SetDataDir("") })

	wt, err := Create(t.Context(), dir, "")
	require.NoError(t, err)
	require.NotNil(t, wt)

	assert.DirExists(t, wt.Dir)
	// A random name looks like "focused_turing" (adjective_surname).
	assert.Regexp(t, `^[a-z]+_[a-z]+$`, wt.Name)
	assert.Equal(t, "worktree-"+wt.Name, wt.Branch)
	assert.Equal(t, filepath.Join(dataDir, "worktrees", wt.Name), wt.Dir)

	// The worktree shares the repository's history: the initial commit's
	// files must be present in the new working directory.
	assert.FileExists(t, filepath.Join(wt.Dir, "a.txt"))

	// The checked-out branch must match the one reported by the worktree.
	out := gitOutput(t, wt.Dir, "rev-parse", "--abbrev-ref", "HEAD")
	assert.Equal(t, wt.Branch, out)
}

func TestCreateWithName(t *testing.T) {
	dir := bootstrapRepo(t)
	dataDir := t.TempDir()
	paths.SetDataDir(dataDir)
	t.Cleanup(func() { paths.SetDataDir("") })

	wt, err := Create(t.Context(), dir, "my-feature")
	require.NoError(t, err)

	assert.Equal(t, "my-feature", wt.Name)
	assert.Equal(t, "worktree-my-feature", wt.Branch)
	assert.Equal(t, filepath.Join(dataDir, "worktrees", "my-feature"), wt.Dir)
	assert.FileExists(t, filepath.Join(wt.Dir, "a.txt"))
}

func TestCreateFromSubfolder(t *testing.T) {
	root := bootstrapRepo(t)
	sub := filepath.Join(root, "nested")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	paths.SetDataDir(t.TempDir())
	t.Cleanup(func() { paths.SetDataDir("") })

	wt, err := Create(t.Context(), sub, "")
	require.NoError(t, err)
	assert.DirExists(t, wt.Dir)
	assert.FileExists(t, filepath.Join(wt.Dir, "a.txt"))
}

func TestCreateOutsideGitRepo(t *testing.T) {
	_, err := Create(t.Context(), t.TempDir(), "")
	assert.ErrorIs(t, err, ErrNotGitRepository)
}

func TestCreateRejectsUnsafeNames(t *testing.T) {
	dir := bootstrapRepo(t)
	paths.SetDataDir(t.TempDir())
	t.Cleanup(func() { paths.SetDataDir("") })

	for _, name := range []string{
		"../escape",
		"../../etc/evil",
		"foo/bar",
		`foo\bar`,
		".",
		"..",
		" leading",
		"trailing ",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Create(t.Context(), dir, name)
			assert.ErrorIs(t, err, ErrInvalidName)
		})
	}
}

func TestCreateRejectsDuplicateName(t *testing.T) {
	dir := bootstrapRepo(t)
	paths.SetDataDir(t.TempDir())
	t.Cleanup(func() { paths.SetDataDir("") })

	_, err := Create(t.Context(), dir, "dup")
	require.NoError(t, err)

	_, err = Create(t.Context(), dir, "dup")
	assert.ErrorIs(t, err, ErrInvalidName)
}

func bootstrapRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test User")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("A"), 0o644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "init")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	require.NoError(t, err)
	return string(trimNL(out))
}

func trimNL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}
