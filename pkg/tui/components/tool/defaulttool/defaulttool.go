package defaulttool

import (
	"github.com/docker/docker-agent/pkg/tui/components/spinner"
	"github.com/docker/docker-agent/pkg/tui/components/toolcommon"
	"github.com/docker/docker-agent/pkg/tui/core/layout"
	"github.com/docker/docker-agent/pkg/tui/service"
	"github.com/docker/docker-agent/pkg/tui/types"
)

// New creates a new default tool component.
// It provides a standard visualization with tool name, arguments, and results.
func New(msg *types.Message, sessionState service.SessionStateReader) layout.Model {
	var lastArgs string
	return toolcommon.NewBase(msg, sessionState, func(msg *types.Message, s spinner.Spinner, sessionState service.SessionStateReader, width, height int) string {
		return render(msg, s, sessionState, width, height, &lastArgs)
	})
}

func render(msg *types.Message, s spinner.Spinner, sessionState service.SessionStateReader, width, _ int, lastArgs *string) string {
	var argsContent string
	if msg.ToolCall.Function.Arguments != "" {
		argsContent = renderToolArgs(msg.ToolCall, width-4-len(msg.ToolDefinition.DisplayName()), width-3)
		if argsContent != "" {
			*lastArgs = argsContent
		} else if msg.ToolStatus == types.ToolStatusPending {
			argsContent = *lastArgs
		}
	}

	if argsContent == "" {
		return toolcommon.RenderTool(msg, s, "", "", width, sessionState.HideToolResults())
	}

	var resultContent string
	if (msg.ToolStatus == types.ToolStatusCompleted || msg.ToolStatus == types.ToolStatusError) && msg.Content != "" {
		resultContent = toolcommon.FormatToolResult(msg.Content, width)
	}

	return toolcommon.RenderTool(msg, s, argsContent, resultContent, width, sessionState.HideToolResults())
}
