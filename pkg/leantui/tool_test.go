package leantui

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/tools"
	"github.com/docker/docker-agent/pkg/tools/builtin/filesystem"
	builtinshell "github.com/docker/docker-agent/pkg/tools/builtin/shell"
	"github.com/docker/docker-agent/pkg/tui/animation"
	tuitypes "github.com/docker/docker-agent/pkg/tui/types"
)

func TestRenderToolOutputTruncatesOutput(t *testing.T) {
	t.Parallel()
	output := strings.Repeat("line\n", 50)
	lines := renderToolOutput(output, 80)

	assert.LessOrEqual(t, len(lines), maxToolOutputLines+1)
	assert.Contains(t, strings.Join(lines, "\n"), "earlier lines")
}

func TestRenderToolUsesFullTUIRenderer(t *testing.T) {
	t.Parallel()
	tv := shellToolView(tuitypes.ToolStatusCompleted)
	tv.message.Content = "hi\n"

	joined := strings.Join(renderTool(*tv, 80), "\n")
	assert.Contains(t, joined, builtinshell.ToolNameShell)
	assert.Contains(t, joined, "echo hi")
	assert.Contains(t, joined, "hi")
	assert.NotContains(t, joined, "Took")
}

func TestRenderToolDoesNotLeakAnimationSubscription(t *testing.T) {
	assert.False(t, animation.HasActive())
	renderToolWithState(shellToolView(tuitypes.ToolStatusRunning), 80, 3, nil)
	assert.False(t, animation.HasActive())
}

func TestRenderToolKeepsLastLinesWhenArgumentsTemporarilyInvalid(t *testing.T) {
	tv := newToolView("root", tools.ToolCall{
		ID: "call-1",
		Function: tools.FunctionCall{
			Name:      "Write",
			Arguments: `{"path": "/tmp/file", "content": "hello"`,
		},
	}, tools.Tool{Name: "Write"}, tuitypes.ToolStatusPending)

	first := renderToolWithState(tv, 80, 0, nil)
	require.Contains(t, strings.Join(first, "\n"), "hello")

	tv.message.ToolCall.Function.Arguments += ","
	second := renderToolWithState(tv, 80, 1, nil)
	assert.Contains(t, strings.Join(second, "\n"), "hello")
}

func TestRenderWriteFileKeepsPathWhenArgumentsTemporarilyInvalid(t *testing.T) {
	tv := newToolView("root", tools.ToolCall{
		ID: "call-1",
		Function: tools.FunctionCall{
			Name:      filesystem.ToolNameWriteFile,
			Arguments: `{"path": "/tmp/file"`,
		},
	}, tools.Tool{Name: filesystem.ToolNameWriteFile}, tuitypes.ToolStatusPending)

	first := renderToolWithState(tv, 80, 0, nil)
	require.Contains(t, strings.Join(first, "\n"), "/tmp/file")

	tv.message.ToolCall.Function.Arguments += ","
	second := renderToolWithState(tv, 80, 1, nil)
	assert.Contains(t, strings.Join(second, "\n"), "/tmp/file")
}

func shellToolView(status tuitypes.ToolStatus) *toolView {
	return newToolView("root", tools.ToolCall{
		ID: "call-1",
		Function: tools.FunctionCall{
			Name:      builtinshell.ToolNameShell,
			Arguments: `{"cmd":"echo hi"}`,
		},
	}, tools.Tool{Name: builtinshell.ToolNameShell}, status)
}
