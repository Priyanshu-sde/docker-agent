package builtins

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/hooks"
)

func TestIsGitRepo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(t *testing.T) string
		want  bool
	}{
		{
			name: "directory containing .git/",
			setup: func(t *testing.T) string {
				t.Helper()
				dir := t.TempDir()
				require.NoError(t, os.Mkdir(filepath.Join(dir, ".git"), 0o755))
				return dir
			},
			want: true,
		},
		{
			name: "subdirectory of a git repo",
			setup: func(t *testing.T) string {
				t.Helper()
				parent := t.TempDir()
				require.NoError(t, os.Mkdir(filepath.Join(parent, ".git"), 0o755))
				child := filepath.Join(parent, "child")
				require.NoError(t, os.Mkdir(child, 0o755))
				return child
			},
			want: true,
		},
		{
			name: ".git is a regular file, not a directory",
			setup: func(t *testing.T) string {
				t.Helper()
				dir := t.TempDir()
				require.NoError(t, os.WriteFile(filepath.Join(dir, ".git"), nil, 0o644))
				return dir
			},
			want: false,
		},
		{
			name:  "directory with no .git anywhere up the tree",
			setup: func(t *testing.T) string { t.Helper(); return t.TempDir() },
			want:  false,
		},
		{
			name:  "nonexistent directory",
			setup: func(*testing.T) string { return "/path/that/does/not/exist" },
			want:  false,
		},
		{
			name:  "empty path",
			setup: func(*testing.T) string { return "" },
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, isGitRepo(tt.setup(t)))
		})
	}
}

// TestGitOutputUsesHookEnv proves the per-hook env reaches the git
// subprocess gitOutput spawns, end to end through the executor. A
// builtin probe reads a config key that exists only when git is handed
// the GIT_CONFIG_* env injection carried by the hook's env override;
// without it the key is absent and the command errors.
func TestGitOutputUsesHookEnv(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed; skipping git-backed builtin test")
	}

	dir := t.TempDir()
	init := exec.CommandContext(t.Context(), "git", "-C", dir, "init", "--quiet", "-b", "main")
	out, err := init.CombinedOutput()
	require.NoErrorf(t, err, "git init failed: %s", out)

	registry := hooks.NewRegistry()
	var got string
	require.NoError(t, registry.RegisterBuiltin("git-config-probe", func(ctx context.Context, in *hooks.Input, _ []string) (*hooks.Output, error) {
		v, gerr := gitOutput(ctx, in.Cwd, "config", "--get", "hook.value")
		if gerr != nil {
			return nil, gerr
		}
		got = v
		return nil, nil
	}))

	// Injects `hook.value=from-env` into git purely through the
	// environment, so a non-empty read proves the env was applied.
	hookEnv := map[string]string{
		"GIT_CONFIG_COUNT":   "1",
		"GIT_CONFIG_KEY_0":   "hook.value",
		"GIT_CONFIG_VALUE_0": "from-env",
	}
	executor := hooks.NewExecutorWithRegistry(&hooks.Config{SessionStart: []hooks.Hook{{
		Type:    hooks.HookTypeBuiltin,
		Command: "git-config-probe",
		Env:     hookEnv,
	}}}, dir, os.Environ(), registry)

	_, err = executor.Dispatch(t.Context(), hooks.EventSessionStart, &hooks.Input{SessionID: "s", Cwd: dir})
	require.NoError(t, err)
	assert.Equal(t, "from-env", got)
}
