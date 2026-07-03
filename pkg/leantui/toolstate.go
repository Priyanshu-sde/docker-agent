package leantui

import (
	"slices"
	"strings"
	"time"

	"github.com/docker/docker-agent/pkg/runtime"
	"github.com/docker/docker-agent/pkg/tools"
	tuitypes "github.com/docker/docker-agent/pkg/tui/types"
)

// toolTracker holds the render state of in-flight tool calls, keyed by id and
// kept in call order so the conversation shows them as they arrive.
type toolTracker struct {
	byID  map[string]*toolView
	order []string
}

func newToolTracker() *toolTracker {
	return &toolTracker{byID: map[string]*toolView{}}
}

func (t *toolTracker) reset() {
	t.byID = map[string]*toolView{}
	t.order = nil
}

func (t *toolTracker) empty() bool { return len(t.order) == 0 }

func (t *toolTracker) get(id string) *toolView { return t.byID[id] }

// forEach visits the tracked tools in call order, skipping nil entries.
func (t *toolTracker) forEach(fn func(*toolView)) {
	for _, id := range t.order {
		if tv := t.byID[id]; tv != nil {
			fn(tv)
		}
	}
}

func (t *toolTracker) remove(id string) {
	if id == "" {
		return
	}
	delete(t.byID, id)
	t.order = slices.DeleteFunc(t.order, func(s string) bool { return s == id })
}

// upsert creates or updates the tracked view for a tool call. Argument
// fragments streamed while the call is still pending are concatenated.
func (t *toolTracker) upsert(agentName string, toolCall tools.ToolCall, toolDef tools.Tool, status tuitypes.ToolStatus) {
	id := toolViewID(toolCall)
	tv := t.byID[id]
	if tv == nil {
		tv = newToolView(agentName, toolCall, toolDef, status)
		t.byID[id] = tv
		t.order = append(t.order, id)
		return
	}

	msg := tv.message
	if msg == nil {
		msg = newToolView(agentName, toolCall, toolDef, status).message
		tv.message = msg
		return
	}

	if agentName != "" {
		msg.Sender = agentName
	}
	if toolDef.Name != "" || toolCall.Function.Name != "" {
		msg.ToolDefinition = ensureToolDefinition(toolCall, toolDef)
	}
	msg.ToolStatus = status
	if status == tuitypes.ToolStatusRunning && msg.StartedAt == nil {
		now := time.Now()
		msg.StartedAt = &now
	}
	if toolCall.ID != "" {
		msg.ToolCall.ID = toolCall.ID
	}
	if toolCall.Type != "" {
		msg.ToolCall.Type = toolCall.Type
	}
	if toolCall.Function.Name != "" {
		msg.ToolCall.Function.Name = toolCall.Function.Name
	}
	if toolCall.Function.Arguments != "" {
		if status == tuitypes.ToolStatusPending {
			msg.ToolCall.Function.Arguments += toolCall.Function.Arguments
		} else {
			msg.ToolCall.Function.Arguments = toolCall.Function.Arguments
		}
	}
}

// finish marks a tool call complete, drops it from the tracker, and returns an
// immutable snapshot to commit into the conversation. It returns nil when there
// is nothing to render.
func (t *toolTracker) finish(e *runtime.ToolCallResponseEvent) *toolView {
	id := e.ToolCallID
	tv := t.byID[id]
	if tv == nil {
		toolCall := tools.ToolCall{ID: id, Function: tools.FunctionCall{Name: e.ToolDefinition.Name}}
		tv = newToolView(e.GetAgentName(), toolCall, e.ToolDefinition, tuitypes.ToolStatusCompleted)
	}
	if tv.message == nil {
		return nil
	}

	status := tuitypes.ToolStatusCompleted
	if e.Result != nil && e.Result.IsError {
		status = tuitypes.ToolStatusError
	}
	tv.message.ToolStatus = status
	tv.message.ToolDefinition = ensureToolDefinition(tv.message.ToolCall, e.ToolDefinition)
	tv.message.Content = strings.ReplaceAll(e.Response, "\t", "    ")
	tv.message.ToolResult = e.Result.WithoutPayload()
	tv.images = inlineImagesFromToolResult(e.Result)

	msg := *tv.message
	snapshot := &toolView{message: &msg, images: tv.images}
	t.remove(id)
	return snapshot
}

func toolViewID(toolCall tools.ToolCall) string {
	if toolCall.ID != "" {
		return toolCall.ID
	}
	return toolCall.Function.Name
}
