package builtins_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/hooks"
	"github.com/docker/docker-agent/pkg/hooks/builtins"
)

// TestRegisterInstallsAllBuiltins pins the public contract of [Register]:
// every name documented in the package constants must be resolvable on
// the registry after registration. If a future change adds or renames a
// builtin without updating Register, this test fails.
func TestRegisterInstallsAllBuiltins(t *testing.T) {
	t.Parallel()

	r := hooks.NewRegistry()
	require.NoError(t, builtins.Register(r))

	for _, name := range []string{
		builtins.AddDate,
		builtins.AddEnvironmentInfo,
		builtins.AddPromptFiles,
		builtins.AddGitStatus,
		builtins.AddGitDiff,
		builtins.AddDirectoryListing,
		builtins.AddUserInfo,
		builtins.AddRecentCommits,
		builtins.MaxIterations,
		builtins.RedactSecrets,
		builtins.LimitLargeToolResults,
		builtins.HTTPPost,
		builtins.Unload,
	} {
		fn, ok := r.LookupBuiltin(name)
		assert.True(t, ok, "builtin %q must be registered", name)
		assert.NotNil(t, fn, "builtin %q must have a non-nil function", name)
	}
}

// TestAddDateReturnsTodaysDate verifies the date builtin emits a
// turn_start AdditionalContext containing today's ISO date. It does NOT
// verify the exact "Today's date: " prefix — that's a UX detail, but we
// keep the assertion loose-but-meaningful by anchoring on the date.
func TestAddDateReturnsTodaysDate(t *testing.T) {
	t.Parallel()

	fn := lookup(t, builtins.AddDate)

	out, err := fn(t.Context(), &hooks.Input{SessionID: "s"}, nil)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.NotNil(t, out.HookSpecificOutput)
	assert.Equal(t, hooks.EventTurnStart, out.HookSpecificOutput.HookEventName,
		"add_date must target turn_start, not session_start")
	assert.Contains(t, out.HookSpecificOutput.AdditionalContext, time.Now().Format("2006-01-02"))
}

// TestAddEnvironmentInfoUsesInputCwd verifies that the env-info builtin
// reads its working directory from the Input (not from os.Getwd) and
// emits a session_start AdditionalContext that reflects that path. We
// assert on the Cwd appearing verbatim rather than the full env block
// format, to stay stable across cosmetic tweaks to GetEnvironmentInfo.
func TestAddEnvironmentInfoUsesInputCwd(t *testing.T) {
	t.Parallel()

	fn := lookup(t, builtins.AddEnvironmentInfo)

	cwd := t.TempDir()
	out, err := fn(t.Context(), &hooks.Input{SessionID: "s", Cwd: cwd}, nil)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.NotNil(t, out.HookSpecificOutput)
	assert.Equal(t, hooks.EventSessionStart, out.HookSpecificOutput.HookEventName,
		"add_environment_info must target session_start, not turn_start")
	assert.Contains(t, out.HookSpecificOutput.AdditionalContext, cwd,
		"env info must reflect the Input's Cwd, not os.Getwd")
}

// TestAddEnvironmentInfoNoCwdIsNoop documents the safety behavior: with
// an empty Cwd the builtin contributes nothing rather than fabricating
// info from os.Getwd or "<unknown>". Returning a nil Output is a valid
// successful no-op per the BuiltinFunc contract.
func TestAddEnvironmentInfoNoCwdIsNoop(t *testing.T) {
	t.Parallel()

	fn := lookup(t, builtins.AddEnvironmentInfo)

	out, err := fn(t.Context(), &hooks.Input{SessionID: "s"}, nil)
	require.NoError(t, err)
	assert.Nil(t, out)

	out, err = fn(t.Context(), nil, nil)
	require.NoError(t, err)
	assert.Nil(t, out)
}

// TestAddPromptFilesReadsFromCwd verifies that add_prompt_files reads
// each file named in args (relative to Input.Cwd) and joins their
// contents into the turn_start AdditionalContext.
func TestAddPromptFilesReadsFromCwd(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	const promptBody = "Project guidelines: prefer Go."
	require.NoError(t, os.WriteFile(filepath.Join(dir, "PROMPT.md"), []byte(promptBody), 0o600))

	fn := lookup(t, builtins.AddPromptFiles)

	out, err := fn(t.Context(), &hooks.Input{SessionID: "s", Cwd: dir}, []string{"PROMPT.md"})
	require.NoError(t, err)
	require.NotNil(t, out)
	require.NotNil(t, out.HookSpecificOutput)
	assert.Equal(t, hooks.EventTurnStart, out.HookSpecificOutput.HookEventName,
		"add_prompt_files must target turn_start, not session_start")
	assert.Contains(t, out.HookSpecificOutput.AdditionalContext, promptBody)
}

// TestAddPromptFilesMissingFileIsTolerated documents that a missing
// prompt file is logged-and-skipped, not an error: surviving files
// still contribute, and an args list with only missing files yields a
// nil Output rather than a hard failure. This matches the original
// inline loop's silent-skip behavior.
func TestAddPromptFilesMissingFileIsTolerated(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	const promptBody = "still here"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "OK.md"), []byte(promptBody), 0o600))

	fn := lookup(t, builtins.AddPromptFiles)

	// One missing + one good: the good one survives.
	out, err := fn(t.Context(), &hooks.Input{SessionID: "s", Cwd: dir}, []string{"MISSING.md", "OK.md"})
	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Contains(t, out.HookSpecificOutput.AdditionalContext, promptBody)
}

// TestAddPromptFilesNoArgsIsNoop pins the early-return behavior: with
// no args (or empty Cwd, or nil Input) the builtin does nothing rather
// than returning an empty AdditionalContext that would still register
// as a contribution.
func TestAddPromptFilesNoArgsIsNoop(t *testing.T) {
	t.Parallel()

	fn := lookup(t, builtins.AddPromptFiles)

	cases := []struct {
		name string
		in   *hooks.Input
		args []string
	}{
		{"nil input", nil, []string{"PROMPT.md"}},
		{"empty cwd", &hooks.Input{SessionID: "s"}, []string{"PROMPT.md"}},
		{"empty args", &hooks.Input{SessionID: "s", Cwd: t.TempDir()}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, err := fn(t.Context(), tc.in, tc.args)
			require.NoError(t, err)
			assert.Nil(t, out)
		})
	}
}

// lookup registers the builtins on a fresh Registry and returns the
// named BuiltinFunc, failing the test if it isn't present. Centralising
// the boilerplate keeps the per-builtin tests focused on behavior.
func lookup(t *testing.T, name string) hooks.BuiltinFunc {
	t.Helper()
	r := hooks.NewRegistry()
	require.NoError(t, builtins.Register(r))
	fn, ok := r.LookupBuiltin(name)
	require.True(t, ok, "builtin %q must be registered", name)
	require.NotNil(t, fn)
	return fn
}

// TestApplyAgentDefaultsAlwaysInjectsLargeResultLimiter pins that the runtime
// always mounts the large-result limiter as a tool_response_transform hook.
func TestApplyAgentDefaultsAlwaysInjectsLargeResultLimiter(t *testing.T) {
	t.Parallel()

	cfg := builtins.ApplyAgentDefaults(nil, builtins.AgentDefaults{})
	require.NotNil(t, cfg)
	require.Len(t, cfg.ToolResponseTransform, 1)
	assert.Equal(t, "*", cfg.ToolResponseTransform[0].Matcher)
	require.Len(t, cfg.ToolResponseTransform[0].Hooks, 1)
	assert.Equal(t, builtins.LimitLargeToolResults, cfg.ToolResponseTransform[0].Hooks[0].Command)
	assert.Equal(t, hooks.HookTypeBuiltin, cfg.ToolResponseTransform[0].Hooks[0].Type)
	require.Len(t, cfg.SessionEnd, 1)
	assert.Equal(t, builtins.LimitLargeToolResults, cfg.SessionEnd[0].Command)
}

// TestApplyAgentDefaultsInjectsExpectedEvents verifies which event each
// flag targets — turn_start for date / prompt files (recompute every
// turn), session_start for environment info (cwd / OS / arch are
// session-stable). Regressing this would silently change when users
// see today's date.
func TestApplyAgentDefaultsInjectsExpectedEvents(t *testing.T) {
	t.Parallel()

	cfg := builtins.ApplyAgentDefaults(nil, builtins.AgentDefaults{
		AddDate:            true,
		AddEnvironmentInfo: true,
		AddPromptFiles:     []string{"PROMPT.md"},
	})
	require.NotNil(t, cfg)

	require.Len(t, cfg.TurnStart, 2, "add_date and add_prompt_files must inject turn_start hooks")
	assert.Equal(t, builtins.AddDate, cfg.TurnStart[0].Command)
	assert.Equal(t, hooks.HookTypeBuiltin, cfg.TurnStart[0].Type)
	assert.Equal(t, builtins.AddPromptFiles, cfg.TurnStart[1].Command)
	assert.Equal(t, []string{"PROMPT.md"}, cfg.TurnStart[1].Args)

	require.Len(t, cfg.SessionStart, 1, "add_environment_info must inject a session_start hook")
	assert.Equal(t, builtins.AddEnvironmentInfo, cfg.SessionStart[0].Command)

	require.Len(t, cfg.ToolResponseTransform, 1, "large-result limiter must always be injected")
	require.Len(t, cfg.ToolResponseTransform[0].Hooks, 1)
	assert.Equal(t, builtins.LimitLargeToolResults, cfg.ToolResponseTransform[0].Hooks[0].Command)
	require.Len(t, cfg.SessionEnd, 1, "large-result cleanup must always be injected")
	assert.Equal(t, builtins.LimitLargeToolResults, cfg.SessionEnd[0].Command)
}

func TestLimitLargeToolResultsStoresFullOutputAndReturnsTailNotice(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())

	var b strings.Builder
	for i := range 3000 {
		b.WriteString(strings.Repeat("x", 600))
		b.WriteString(" line ")
		b.WriteString(strconv.Itoa(i))
		b.WriteByte('\n')
	}
	original := b.String()

	fn := lookup(t, builtins.LimitLargeToolResults)
	out, err := fn(t.Context(), &hooks.Input{
		HookEventName: hooks.EventToolResponseTransform,
		ToolCategory:  "filesystem",
		ToolResponse:  original,
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.NotNil(t, out.HookSpecificOutput)
	require.NotNil(t, out.HookSpecificOutput.UpdatedToolResponse)

	updated := *out.HookSpecificOutput.UpdatedToolResponse
	assert.Contains(t, updated, "Tool call result was too large")
	assert.Contains(t, updated, "The full result is available in a file:")
	assert.Contains(t, updated, "Showing the last")
	assert.NotContains(t, updated, strings.Repeat("x", 600)+" line 0\n")
	assert.Contains(t, updated, " line 2999\n")

	stored, err := os.ReadFile(extractLargeResultPath(t, updated))
	require.NoError(t, err)
	assert.Equal(t, original, string(stored))
}

func TestLimitLargeToolResultsPreservesUTF8Tail(t *testing.T) {
	payload := strings.Repeat("世", maxToolCallResultBytesForTest/len("世")+largeToolCallResultTailBytesForTest/len("世")+10)

	fn := lookup(t, builtins.LimitLargeToolResults)
	out, err := fn(t.Context(), &hooks.Input{
		SessionID:     "utf8-session",
		HookEventName: hooks.EventToolResponseTransform,
		ToolCategory:  "shell",
		ToolResponse:  payload,
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.NotNil(t, out.HookSpecificOutput.UpdatedToolResponse)
	updated := *out.HookSpecificOutput.UpdatedToolResponse
	assert.Contains(t, updated, "世")
	assert.Equal(t, updated, strings.ToValidUTF8(updated, ""))
}

func TestLimitLargeToolResultsCleansSessionTempDir(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())

	fn := lookup(t, builtins.LimitLargeToolResults)
	out, err := fn(t.Context(), &hooks.Input{
		SessionID:     "cleanup/session",
		HookEventName: hooks.EventToolResponseTransform,
		ToolCategory:  "filesystem",
		ToolResponse:  strings.Repeat("x", maxToolCallResultBytesForTest+1),
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, out)
	path := extractLargeResultPath(t, *out.HookSpecificOutput.UpdatedToolResponse)
	_, err = os.Stat(path)
	require.NoError(t, err)

	_, err = fn(t.Context(), &hooks.Input{
		SessionID:     "cleanup/session",
		HookEventName: hooks.EventSessionEnd,
	}, nil)
	require.NoError(t, err)
	_, err = os.Stat(path)
	assert.True(t, os.IsNotExist(err), "session_end must clean up large-result temp files")
}

const (
	maxToolCallResultBytesForTest       = 50 * 1024
	largeToolCallResultTailBytesForTest = 50 * 1024
)

func TestLimitLargeToolResultsNoopsForInternalCategory(t *testing.T) {
	t.Parallel()

	fn := lookup(t, builtins.LimitLargeToolResults)
	out, err := fn(t.Context(), &hooks.Input{
		HookEventName: hooks.EventToolResponseTransform,
		ToolCategory:  "memory",
		ToolResponse:  strings.Repeat("x", maxToolCallResultBytesForTest+1),
	}, nil)
	require.NoError(t, err)
	assert.Nil(t, out)
}

func TestLimitLargeToolResultsCapsExternalToolCategories(t *testing.T) {
	for _, category := range []string{"mcp", "a2a"} {
		t.Run(category, func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())

			fn := lookup(t, builtins.LimitLargeToolResults)
			out, err := fn(t.Context(), &hooks.Input{
				SessionID:     category + "-session",
				HookEventName: hooks.EventToolResponseTransform,
				ToolCategory:  category,
				ToolResponse:  strings.Repeat("x", maxToolCallResultBytesForTest+1),
			}, nil)
			require.NoError(t, err)
			require.NotNil(t, out)
			require.NotNil(t, out.HookSpecificOutput)
			require.NotNil(t, out.HookSpecificOutput.UpdatedToolResponse)
			assert.Contains(t, *out.HookSpecificOutput.UpdatedToolResponse, "Tool call result was too large")
		})
	}
}

func TestLimitLargeToolResultsTriggersOnLineCount(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())

	payload := strings.Repeat("x\n", 2001)
	require.LessOrEqual(t, len(payload), maxToolCallResultBytesForTest)

	fn := lookup(t, builtins.LimitLargeToolResults)
	out, err := fn(t.Context(), &hooks.Input{
		SessionID:     "line-count-session",
		HookEventName: hooks.EventToolResponseTransform,
		ToolCategory:  "shell",
		ToolResponse:  payload,
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.NotNil(t, out.HookSpecificOutput.UpdatedToolResponse)
	updated := *out.HookSpecificOutput.UpdatedToolResponse
	assert.Contains(t, updated, "Tool call result was too large")
	assert.NotContains(t, updated, strings.Repeat("x\n", 2001))
}

func TestLimitLargeToolResultsNoopsForSmallOutput(t *testing.T) {
	t.Parallel()

	fn := lookup(t, builtins.LimitLargeToolResults)
	out, err := fn(t.Context(), &hooks.Input{
		HookEventName: hooks.EventToolResponseTransform,
		ToolCategory:  "shell",
		ToolResponse:  "small output",
	}, nil)
	require.NoError(t, err)
	assert.Nil(t, out)
}

func extractLargeResultPath(t *testing.T, response string) string {
	t.Helper()
	const marker = "The full result is available in a file: "
	idx := strings.Index(response, marker)
	require.NotEqual(t, -1, idx)
	pathStart := idx + len(marker)
	pathEnd := strings.Index(response[pathStart:], "\n")
	require.NotEqual(t, -1, pathEnd)
	return response[pathStart : pathStart+pathEnd]
}

// TestRegisterSnapshotInstallsBuiltin verifies that the dedicated
// snapshot entry point installs the snapshot builtin and returns a
// controller wired up to the registered hook.
func TestRegisterSnapshotInstallsBuiltin(t *testing.T) {
	t.Parallel()

	r := hooks.NewRegistry()
	ctrl, err := builtins.RegisterSnapshot(r, true)
	require.NoError(t, err)
	require.NotNil(t, ctrl)
	assert.True(t, ctrl.Enabled())

	fn, ok := r.LookupBuiltin(builtins.Snapshot)
	assert.True(t, ok, "snapshot must be registered by RegisterSnapshot")
	assert.NotNil(t, fn)
}

// TestRegisterSnapshotDisabledStillExposesController verifies that an
// embedder can install the snapshot builtin without auto-injection, in
// which case the controller still exists (so /undo etc. work for hooks
// the user wired manually) but Enabled() reports false.
func TestRegisterSnapshotDisabledStillExposesController(t *testing.T) {
	t.Parallel()

	r := hooks.NewRegistry()
	ctrl, err := builtins.RegisterSnapshot(r, false)
	require.NoError(t, err)
	require.NotNil(t, ctrl)
	assert.False(t, ctrl.Enabled())

	_, ok := r.LookupBuiltin(builtins.Snapshot)
	assert.True(t, ok)
}

// TestSnapshotControllerAutoInjectWiresFourEvents verifies that the
// controller's AutoInject mounts the snapshot hook on session_start,
// turn_start, turn_end, and session_end — the four boundaries needed
// to bracket every session and every turn. Per-tool capture stays
// opt-in via YAML.
func TestSnapshotControllerAutoInjectWiresFourEvents(t *testing.T) {
	t.Parallel()

	r := hooks.NewRegistry()
	ctrl, err := builtins.RegisterSnapshot(r, true)
	require.NoError(t, err)

	inj, ok := ctrl.(builtins.AutoInjector)
	require.True(t, ok, "controller must satisfy AutoInjector")

	cfg := &hooks.Config{}
	inj.AutoInject(cfg)
	require.Len(t, cfg.SessionStart, 1)
	require.Len(t, cfg.TurnStart, 1)
	require.Len(t, cfg.TurnEnd, 1)
	require.Len(t, cfg.SessionEnd, 1)
	assert.Equal(t, builtins.Snapshot, cfg.SessionStart[0].Command)
	assert.Equal(t, builtins.Snapshot, cfg.TurnStart[0].Command)
	assert.Equal(t, builtins.Snapshot, cfg.TurnEnd[0].Command)
	assert.Equal(t, builtins.Snapshot, cfg.SessionEnd[0].Command)
}

// TestSnapshotControllerAutoInjectDisabledIsNoop verifies that a
// controller constructed with enabled=false makes no changes to cfg,
// so an embedder can pass it unconditionally to the runtime as an
// AutoInjector and rely on the bool to gate auto-injection.
func TestSnapshotControllerAutoInjectDisabledIsNoop(t *testing.T) {
	t.Parallel()

	r := hooks.NewRegistry()
	ctrl, err := builtins.RegisterSnapshot(r, false)
	require.NoError(t, err)

	inj, ok := ctrl.(builtins.AutoInjector)
	require.True(t, ok)

	cfg := &hooks.Config{}
	inj.AutoInject(cfg)
	assert.True(t, cfg.IsEmpty(), "disabled controller must not inject any hooks")
}

func TestApplyAgentDefaultsAppendsToUserHooks(t *testing.T) {
	t.Parallel()

	user := hooks.Hook{Type: hooks.HookTypeCommand, Command: "echo hi"}
	cfg := &hooks.Config{TurnStart: []hooks.Hook{user}}

	got := builtins.ApplyAgentDefaults(cfg, builtins.AgentDefaults{AddDate: true})
	require.NotNil(t, got)
	require.Len(t, got.TurnStart, 2)
	assert.Equal(t, user, got.TurnStart[0])
	assert.Equal(t, builtins.AddDate, got.TurnStart[1].Command)
}
