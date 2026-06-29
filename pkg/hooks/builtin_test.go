package hooks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRegisterBuiltinValidation pins the input contract: empty names and
// nil functions are rejected, valid pairs round-trip through LookupBuiltin.
func TestRegisterBuiltinValidation(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()

	require.Error(t, registry.RegisterBuiltin("", func(context.Context, *Input, []string) (*Output, error) { return nil, nil }))
	require.Error(t, registry.RegisterBuiltin("nil-fn", nil))

	require.NoError(t, registry.RegisterBuiltin("echo", func(context.Context, *Input, []string) (*Output, error) {
		return &Output{}, nil
	}))

	fn, ok := registry.LookupBuiltin("echo")
	require.True(t, ok)
	require.NotNil(t, fn)

	_, ok = registry.LookupBuiltin("never-registered")
	require.False(t, ok)
}

// TestExecutorDispatchesBuiltinHook is the end-to-end happy path: a Go
// function is registered on a private Registry, referenced from a hook
// with {type: builtin, command: <name>}, and its returned Output drives
// the aggregated Result. The handler also sees the typed Input directly,
// without having to unmarshal JSON itself.
func TestExecutorDispatchesBuiltinHook(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()

	var (
		called   bool
		seenTool string
	)
	require.NoError(t, registry.RegisterBuiltin("deny", func(_ context.Context, in *Input, _ []string) (*Output, error) {
		called = true
		seenTool = in.ToolName
		return &Output{
			Decision: "block",
			Reason:   "denied by builtin hook",
		}, nil
	}))

	config := &Config{
		PreToolUse: []MatcherConfig{
			{
				Matcher: "*",
				Hooks: []Hook{
					{Type: HookTypeBuiltin, Command: "deny", Timeout: 5},
				},
			},
		},
	}

	exec := NewExecutorWithRegistry(config, t.TempDir(), nil, registry)
	result, err := exec.Dispatch(t.Context(), EventPreToolUse, &Input{
		SessionID: "test-session",
		ToolName:  "shell",
		ToolUseID: "test-id",
	})
	require.NoError(t, err)

	assert.True(t, called)
	assert.Equal(t, "shell", seenTool)
	assert.False(t, result.Allowed)
	assert.Contains(t, result.Message, "denied by builtin hook")
}

// TestBuiltinHookUnknownNameIsRejected ensures that referencing an
// unregistered builtin from a hook surfaces as a hook execution error.
// For PreToolUse this maps to fail-closed (deny), matching how the
// existing "unsupported hook type" path behaves.
func TestBuiltinHookUnknownNameIsRejected(t *testing.T) {
	t.Parallel()

	config := &Config{
		PreToolUse: []MatcherConfig{
			{
				Matcher: "*",
				Hooks: []Hook{
					{Type: HookTypeBuiltin, Command: "never-registered", Timeout: 5},
				},
			},
		},
	}

	exec := NewExecutorWithRegistry(config, t.TempDir(), nil, NewRegistry())
	result, err := exec.Dispatch(t.Context(), EventPreToolUse, &Input{
		SessionID: "test-session",
		ToolName:  "shell",
		ToolUseID: "test-id",
	})
	require.NoError(t, err)
	assert.False(t, result.Allowed)
	assert.Contains(t, result.Message, "no builtin hook registered")
}

// TestBuiltinHookEmptyNameIsRejected ensures the factory rejects a hook
// that uses HookTypeBuiltin without naming a function.
func TestBuiltinHookEmptyNameIsRejected(t *testing.T) {
	t.Parallel()

	config := &Config{
		PreToolUse: []MatcherConfig{
			{
				Matcher: "*",
				Hooks: []Hook{
					{Type: HookTypeBuiltin, Command: "", Timeout: 5},
				},
			},
		},
	}

	exec := NewExecutorWithRegistry(config, t.TempDir(), nil, NewRegistry())
	result, err := exec.Dispatch(t.Context(), EventPreToolUse, &Input{
		SessionID: "test-session",
		ToolName:  "shell",
		ToolUseID: "test-id",
	})
	require.NoError(t, err)
	assert.False(t, result.Allowed)
	assert.Contains(t, result.Message, "builtin hook requires a name")
}

// TestBuiltinHookHonorsWorkingDir pins that a builtin hook's working_dir
// override repoints Input.Cwd before the BuiltinFunc runs — the same
// resolution command hooks get. A relative override is joined onto the
// executor's working directory.
func TestBuiltinHookHonorsWorkingDir(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(baseDir, "scripts"), 0o755))

	registry := NewRegistry()
	var seenCwd string
	require.NoError(t, registry.RegisterBuiltin("capture-cwd", func(_ context.Context, in *Input, _ []string) (*Output, error) {
		seenCwd = in.Cwd
		return nil, nil
	}))

	exec := NewExecutorWithRegistry(&Config{SessionStart: []Hook{{
		Type:       HookTypeBuiltin,
		Command:    "capture-cwd",
		WorkingDir: "scripts",
	}}}, baseDir, nil, registry)

	_, err := exec.Dispatch(t.Context(), EventSessionStart, &Input{SessionID: "s"})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(baseDir, "scripts"), seenCwd)
}

// TestBuiltinHookWithoutWorkingDirKeepsInputCwd pins that, absent a
// working_dir override, a builtin sees the Input.Cwd the caller
// supplied — the executor must not clobber it with its own working
// directory. This matters for events like worktree_create whose cwd is
// set explicitly by the dispatcher.
func TestBuiltinHookWithoutWorkingDirKeepsInputCwd(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()
	var seenCwd string
	require.NoError(t, registry.RegisterBuiltin("capture-cwd", func(_ context.Context, in *Input, _ []string) (*Output, error) {
		seenCwd = in.Cwd
		return nil, nil
	}))

	exec := NewExecutorWithRegistry(&Config{SessionStart: []Hook{{
		Type:    HookTypeBuiltin,
		Command: "capture-cwd",
	}}}, t.TempDir(), nil, registry)

	_, err := exec.Dispatch(t.Context(), EventSessionStart, &Input{SessionID: "s", Cwd: "/explicit/cwd"})
	require.NoError(t, err)
	assert.Equal(t, "/explicit/cwd", seenCwd)
}

// TestBuiltinHookHonorsEnv pins that per-hook env overrides reach the
// BuiltinFunc through the context, merged onto the executor env, so
// builtins that shell out can run subprocesses with per-hook variables.
func TestBuiltinHookHonorsEnv(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()
	var seenEnv []string
	require.NoError(t, registry.RegisterBuiltin("capture-env", func(ctx context.Context, _ *Input, _ []string) (*Output, error) {
		seenEnv = EnvFromContext(ctx)
		return nil, nil
	}))

	exec := NewExecutorWithRegistry(&Config{SessionStart: []Hook{{
		Type:    HookTypeBuiltin,
		Command: "capture-env",
		Env:     map[string]string{"HOOK_VALUE": "from-hook"},
	}}}, t.TempDir(), []string{"BASE=1"}, registry)

	_, err := exec.Dispatch(t.Context(), EventSessionStart, &Input{SessionID: "s"})
	require.NoError(t, err)
	assert.Contains(t, seenEnv, "BASE=1")
	assert.Contains(t, seenEnv, "HOOK_VALUE=from-hook")
}

// TestBuiltinHookWithoutEnvInheritsProcess pins that a builtin without an
// env override sees a nil env from the context, the signal to inherit the
// process environment (exec.Cmd's default).
func TestBuiltinHookWithoutEnvInheritsProcess(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()
	sawEnv := true
	require.NoError(t, registry.RegisterBuiltin("capture-env", func(ctx context.Context, _ *Input, _ []string) (*Output, error) {
		sawEnv = EnvFromContext(ctx) != nil
		return nil, nil
	}))

	exec := NewExecutorWithRegistry(&Config{SessionStart: []Hook{{
		Type:    HookTypeBuiltin,
		Command: "capture-env",
	}}}, t.TempDir(), nil, registry)

	_, err := exec.Dispatch(t.Context(), EventSessionStart, &Input{SessionID: "s"})
	require.NoError(t, err)
	assert.False(t, sawEnv, "nil executor env + no override must yield a nil hook env")
}

// TestBuiltinHookErrorFailsClosed documents that an error returned by the
// builtin function is treated identically to a command hook spawn failure:
// for PreToolUse it denies the call.
func TestBuiltinHookErrorFailsClosed(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()
	require.NoError(t, registry.RegisterBuiltin("boom", func(context.Context, *Input, []string) (*Output, error) {
		return nil, errors.New("kaboom")
	}))

	config := &Config{
		PreToolUse: []MatcherConfig{
			{
				Matcher: "*",
				Hooks: []Hook{
					{Type: HookTypeBuiltin, Command: "boom", Timeout: 5},
				},
			},
		},
	}

	exec := NewExecutorWithRegistry(config, t.TempDir(), nil, registry)
	result, err := exec.Dispatch(t.Context(), EventPreToolUse, &Input{
		SessionID: "test-session",
		ToolName:  "shell",
		ToolUseID: "test-id",
	})
	require.NoError(t, err)
	assert.False(t, result.Allowed)
	assert.Equal(t, -1, result.ExitCode)
}
