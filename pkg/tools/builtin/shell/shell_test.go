package shell

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/config"
	"github.com/docker/docker-agent/pkg/tools"
)

func TestNew(t *testing.T) {
	t.Setenv("SHELL", "/bin/bash")
	tool := New(nil, &config.RuntimeConfig{Config: config.Config{WorkingDir: t.TempDir()}})

	assert.NotNil(t, tool)
	assert.NotNil(t, tool.handler)
	assert.Equal(t, "/bin/bash", tool.handler.shell)

	t.Setenv("SHELL", "")
	tool = New(nil, &config.RuntimeConfig{Config: config.Config{WorkingDir: t.TempDir()}})

	assert.NotNil(t, tool)
	assert.NotNil(t, tool.handler)
	assert.Equal(t, "/bin/sh", tool.handler.shell, "Should default to /bin/sh when SHELL is not set")
}

func TestShellTool_HandlerEcho(t *testing.T) {
	t.Parallel()
	tool := New(nil, &config.RuntimeConfig{Config: config.Config{WorkingDir: t.TempDir()}})

	result, err := tool.handler.RunShell(t.Context(), RunShellArgs{
		Cmd: "echo 'hello world'",
		Cwd: "",
	}, tools.NopRuntime{})
	require.NoError(t, err)
	assert.Contains(t, result.Output, "hello world")
}

func TestShellTool_HandlerWithCwd(t *testing.T) {
	t.Parallel()
	tool := New(nil, &config.RuntimeConfig{Config: config.Config{WorkingDir: t.TempDir()}})
	tmpDir := t.TempDir()

	result, err := tool.handler.RunShell(t.Context(), RunShellArgs{
		Cmd: "pwd",
		Cwd: tmpDir,
	}, tools.NopRuntime{})
	require.NoError(t, err)
	// The output might contain extra newlines or other characters,
	// so we just check if it contains the temp dir path
	assert.Contains(t, result.Output, tmpDir)
}

func TestRunShellArgs_UnmarshalJSON_AcceptsCmdAndCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantCmd string
		wantCwd string
		wantTO  int
	}{
		{
			name:    "canonical cmd",
			input:   `{"cmd":"ls -la","cwd":"/tmp","timeout":10}`,
			wantCmd: "ls -la",
			wantCwd: "/tmp",
			wantTO:  10,
		},
		{
			name:    "alias command",
			input:   `{"command":"ls -la","cwd":"/tmp","timeout":10}`,
			wantCmd: "ls -la",
			wantCwd: "/tmp",
			wantTO:  10,
		},
		{
			name:    "both present cmd wins",
			input:   `{"cmd":"from-cmd","command":"from-command"}`,
			wantCmd: "from-cmd",
		},
		{
			name:    "blank cmd falls back to command alias",
			input:   `{"cmd":"   ","command":"from-command"}`,
			wantCmd: "from-command",
		},
		{
			name:    "empty cmd falls back to command alias",
			input:   `{"cmd":"","command":"from-command"}`,
			wantCmd: "from-command",
		},
		{
			name:    "empty object leaves cmd empty",
			input:   `{}`,
			wantCmd: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var got RunShellArgs
			require.NoError(t, json.Unmarshal([]byte(tt.input), &got))
			assert.Equal(t, tt.wantCmd, got.Cmd)
			assert.Equal(t, tt.wantCwd, got.Cwd)
			assert.Equal(t, tt.wantTO, got.Timeout)
		})
	}
}

// Exercises the end-to-end dispatch path: a tool-call whose raw arguments
// use "command" instead of "cmd" must execute normally rather than return
// the missing-parameter error.
func TestShellTool_HandlerAcceptsCommandAlias(t *testing.T) {
	t.Parallel()
	tool := New(nil, &config.RuntimeConfig{Config: config.Config{WorkingDir: t.TempDir()}})

	var params RunShellArgs
	require.NoError(t, json.Unmarshal([]byte(`{"command":"echo hello-from-alias"}`), &params))

	result, err := tool.handler.RunShell(t.Context(), params, tools.NopRuntime{})
	require.NoError(t, err)
	assert.Contains(t, result.Output, "hello-from-alias")
}

func TestShellTool_HandlerMissingCmdReturnsActionableError(t *testing.T) {
	t.Parallel()
	tool := New(nil, &config.RuntimeConfig{Config: config.Config{WorkingDir: t.TempDir()}})

	result, err := tool.handler.RunShell(t.Context(), RunShellArgs{}, tools.NopRuntime{})
	require.NoError(t, err)
	assert.Contains(t, result.Output, `"cmd"`,
		"error must name the expected parameter so the model can self-correct")
}

func TestShellTool_HandlerError(t *testing.T) {
	t.Parallel()
	tool := New(nil, &config.RuntimeConfig{Config: config.Config{WorkingDir: t.TempDir()}})

	result, err := tool.handler.RunShell(t.Context(), RunShellArgs{
		Cmd: "command_that_does_not_exist",
		Cwd: "",
	}, tools.NopRuntime{})
	require.NoError(t, err, "Handler should not return an error")
	assert.Contains(t, result.Output, "Error executing command")
}

func TestShellTool_OutputSchema(t *testing.T) {
	t.Parallel()
	tool := New(nil, &config.RuntimeConfig{Config: config.Config{WorkingDir: t.TempDir()}})

	allTools, err := tool.Tools(t.Context())
	require.NoError(t, err)
	require.NotEmpty(t, allTools)

	for _, tool := range allTools {
		assert.NotNil(t, tool.OutputSchema)
	}
}

func TestShellTool_ParametersAreObjects(t *testing.T) {
	t.Parallel()
	tool := New(nil, &config.RuntimeConfig{Config: config.Config{WorkingDir: t.TempDir()}})

	allTools, err := tool.Tools(t.Context())
	require.NoError(t, err)
	require.NotEmpty(t, allTools)

	for _, tool := range allTools {
		m, err := tools.SchemaToMap(tool.Parameters)
		require.NoError(t, err)
		assert.Equal(t, "object", m["type"])
	}
}

func TestShellTool_OnlyExposesShell(t *testing.T) {
	t.Parallel()
	tool := New(nil, &config.RuntimeConfig{Config: config.Config{WorkingDir: t.TempDir()}})

	allTools, err := tool.Tools(t.Context())
	require.NoError(t, err)
	require.Len(t, allTools, 1)
	assert.Equal(t, ToolNameShell, allTools[0].Name)
}

func TestShellTool_Instructions(t *testing.T) {
	t.Parallel()

	tool := New(nil, &config.RuntimeConfig{Config: config.Config{WorkingDir: t.TempDir()}})

	instructions := tool.Instructions()

	assert.Contains(t, instructions, "Shell Tools")
	assert.NotContains(t, instructions, "run_background_job")
}

func TestResolveWorkDir(t *testing.T) {
	t.Parallel()

	workingDir := "/configured/project"
	h := &shellHandler{workingDir: workingDir}

	tests := []struct {
		name     string
		cwd      string
		expected string
	}{
		{name: "empty defaults to workingDir", cwd: "", expected: workingDir},
		{name: "dot defaults to workingDir", cwd: ".", expected: workingDir},
		{name: "absolute path unchanged", cwd: "/tmp/other", expected: "/tmp/other"},
		{name: "relative path joined with workingDir", cwd: "src/pkg", expected: "/configured/project/src/pkg"},
		{name: "relative with dot prefix", cwd: "./subdir", expected: "/configured/project/subdir"},
		{name: "relative with parent traversal", cwd: "../sibling", expected: "/configured/sibling"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, h.resolveWorkDir(tt.cwd))
		})
	}
}

func TestShellTool_RelativeCwdResolvesAgainstWorkingDir(t *testing.T) {
	t.Parallel()
	// Create a directory structure: workingDir/subdir/
	workingDir := t.TempDir()
	subdir := workingDir + "/subdir"
	require.NoError(t, os.Mkdir(subdir, 0o755))

	tool := New(nil, &config.RuntimeConfig{Config: config.Config{WorkingDir: workingDir}})

	result, err := tool.handler.RunShell(t.Context(), RunShellArgs{
		Cmd: "pwd",
		Cwd: "subdir",
	}, tools.NopRuntime{})
	require.NoError(t, err)
	assert.Contains(t, result.Output, subdir,
		"relative cwd must resolve against the configured workingDir, not the process cwd")
}

// Regression test for a shell-tool hang caused by backgrounded grandchildren.
//
// A command like `sleep 10 &` makes the shell exit immediately, but the
// backgrounded sleep inherits stdout/stderr. Without cmd.WaitDelay, Go's
// exec.Cmd.Wait() blocks reading the pipe until the configured timeout,
// which makes the tool call hang (observed in eval runs where the agent
// launched a server with `docker run ... &`).
//
// With the WaitDelay safeguard the tool must return within a small fraction
// of the configured timeout.
func TestShellTool_BackgroundedChildDoesNotBlockReturn(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell backgrounding semantics; skipped on Windows")
	}

	tool := New(nil, &config.RuntimeConfig{Config: config.Config{WorkingDir: t.TempDir()}})

	start := time.Now()
	result, err := tool.handler.RunShell(t.Context(), RunShellArgs{
		// sleep inherits stdout/stderr from the shell and holds the pipe
		// open for 30s. The tool must return as soon as the shell exits.
		Cmd:     "sleep 30 &",
		Timeout: 20,
	}, tools.NopRuntime{})
	elapsed := time.Since(start)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Less(t, elapsed, 5*time.Second,
		"shell tool must return promptly when the command backgrounds a child "+
			"that inherits stdout/stderr; elapsed=%s", elapsed)
}

// Even when the backgrounded child detaches into its own session (so the
// shell tool's process-group kill cannot reach it on timeout), cmd.WaitDelay
// must still allow the tool call to return.
func TestShellTool_DetachedBackgroundedChildDoesNotBlockReturn(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell backgrounding semantics; skipped on Windows")
	}
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid not available")
	}

	tool := New(nil, &config.RuntimeConfig{Config: config.Config{WorkingDir: t.TempDir()}})

	done := make(chan struct{})
	var result *tools.ToolCallResult
	var err error
	go func() {
		defer close(done)
		result, err = tool.handler.RunShell(t.Context(), RunShellArgs{
			// setsid places sleep in its own session/process group, so the
			// process-group kill fallback in the timeout path cannot reach
			// it. Only cmd.WaitDelay can unblock Wait() here.
			Cmd:     "setsid sleep 30 &",
			Timeout: 20,
		}, tools.NopRuntime{})
	}()

	select {
	case <-done:
		require.NoError(t, err)
		require.NotNil(t, result)
	case <-time.After(10 * time.Second):
		t.Fatal("shell tool hung when command backgrounded a detached child")
	}
}

// TestReapSpawnedChild verifies that reapSpawnedChild both terminates a
// running child and waits on it so no zombie is left behind. This exercises
// the error path we take when cmd.Start() succeeded but a follow-up call
// (e.g. createProcessGroup) failed.
func TestReapSpawnedChild(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-specific: relies on /bin/sh and ProcessState.Exited()")
	}

	cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", "sleep 60")
	cmd.SysProcAttr = platformSpecificSysProcAttr()
	require.NoError(t, cmd.Start())

	start := time.Now()
	reapSpawnedChild(cmd, nil)
	elapsed := time.Since(start)

	assert.Less(t, elapsed, 3*time.Second, "reapSpawnedChild should return promptly after kill")
	require.NotNil(t, cmd.ProcessState, "ProcessState must be populated - Wait() was not called")
	// After Wait(), ProcessState.Exited() returns false for signaled
	// processes but the important property is that the child was reaped,
	// which is exactly what ProcessState != nil guarantees.
}

// TestReapSpawnedChild_HandlesAlreadyExited verifies that reaping a process
// that has already exited is a no-op (does not block, does not panic).
func TestReapSpawnedChild_HandlesAlreadyExited(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-specific")
	}

	cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", "exit 0")
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	// EOF on stdout means the child has exited (its fds are closed on exit).
	_, err = io.ReadAll(stdout)
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		reapSpawnedChild(cmd, nil)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("reapSpawnedChild hung on an already-exited process")
	}
	require.NotNil(t, cmd.ProcessState, "process must have been reaped")
}
