package leantui

import (
	"strings"

	"github.com/docker/docker-agent/pkg/runtime"
	"github.com/docker/docker-agent/pkg/tools"
	"github.com/docker/docker-agent/pkg/tui/service"
	tuitypes "github.com/docker/docker-agent/pkg/tui/types"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type blockKind int

const (
	blockReasoning blockKind = iota
	blockAssistant
)

// pendingBlock accumulates the text of the block currently being streamed.
type pendingBlock struct {
	kind blockKind
	text strings.Builder
}

// block is a finalized piece of the conversation. Its lines are rendered lazily
// and cached per width, so finalized content is not re-rendered every frame and
// only reflows when the terminal is resized.
type block struct {
	render func(width int) []string
	cacheW int
	cache  []string
	cached bool
}

func (b *block) lines(width int) []string {
	if !b.cached || b.cacheW != width {
		b.cache = b.render(width)
		b.cacheW = width
		b.cached = true
	}
	return b.cache
}

// transcript owns everything that scrolls: the finalized conversation blocks,
// the in-progress streamed block, and the in-flight tool calls. Committed
// blocks are immutable scrollback; the pending block and tool calls are the
// live region that changes each frame until they finalize into blocks.
type transcript struct {
	blocks  []*block
	pending *pendingBlock
	toolz   *toolTracker
}

func newTranscript() *transcript {
	return &transcript{toolz: newToolTracker()}
}

// clearActive drops the live region (the streamed block and any in-flight tool
// calls) while keeping the committed scrollback intact. Used when starting a
// new session.
func (t *transcript) clearActive() {
	t.pending = nil
	t.toolz.reset()
}

// addBlock appends a finalized, lazily-rendered block to the conversation.
func (t *transcript) addBlock(render func(width int) []string) {
	t.blocks = append(t.blocks, &block{render: render})
}

func (t *transcript) appendPending(kind blockKind, content string) {
	if content == "" {
		return
	}
	if t.pending == nil || t.pending.kind != kind {
		t.flushPending()
		t.pending = &pendingBlock{kind: kind}
	}
	t.pending.text.WriteString(content)
}

// flushPending finalizes the in-progress streamed block into the conversation.
func (t *transcript) flushPending() {
	if t.pending == nil {
		return
	}
	text := t.pending.text.String()
	kind := t.pending.kind
	t.pending = nil

	switch kind {
	case blockReasoning:
		t.addBlock(func(w int) []string { return renderReasoningLines(text, w) })
	case blockAssistant:
		t.addBlock(func(w int) []string { return renderAssistantLines(text, w) })
	}
}

func (t *transcript) upsertTool(agentName string, toolCall tools.ToolCall, toolDef tools.Tool, status tuitypes.ToolStatus) {
	t.toolz.upsert(agentName, toolCall, toolDef, status)
}

func (t *transcript) tool(id string) *toolView { return t.toolz.get(id) }

func (t *transcript) removeTool(id string) { t.toolz.remove(id) }

// finishTool commits a completed tool call as an immutable block.
func (t *transcript) finishTool(e *runtime.ToolCallResponseEvent, sessionState service.SessionStateReader) {
	view := t.toolz.finish(e)
	if view == nil {
		return
	}
	t.addBlock(func(w int) []string { return renderToolWithState(view, w, 0, sessionState) })
}

// lines renders everything that scrolls: finalized blocks, the in-progress
// streamed block, and any running tool calls. A blank line separates each
// entry. The spinner is shown only while busy with nothing yet streaming.
func (t *transcript) lines(width, spinnerFrame int, busy bool, sessionState service.SessionStateReader) []string {
	var lines []string
	for _, b := range t.blocks {
		lines = append(lines, b.lines(width)...)
		lines = append(lines, "")
	}
	if t.pending != nil {
		lines = append(lines, t.pendingLines(width)...)
		lines = append(lines, "")
	}
	t.toolz.forEach(func(tv *toolView) {
		lines = append(lines, renderToolWithState(tv, width, spinnerFrame, sessionState)...)
		lines = append(lines, "")
	})
	if busy && t.pending == nil && t.toolz.empty() {
		lines = append(lines, spinnerLine(spinnerFrame), "")
	}
	return lines
}

// pendingLines renders the message currently being streamed. Assistant text is
// rendered as markdown live (the same renderer used once it is finalized), so
// formatting appears as it streams.
func (t *transcript) pendingLines(width int) []string {
	text := t.pending.text.String()
	switch t.pending.kind {
	case blockReasoning:
		return renderReasoningLines(text, width)
	case blockAssistant:
		return renderAssistantLines(text, width)
	default:
		return nil
	}
}

func spinnerLine(frame int) string {
	f := spinnerFrames[frame%len(spinnerFrames)]
	return stAccent().Render(f) + " " + stMuted().Render("Working…")
}
