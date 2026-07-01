package leantui

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

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

// buildLines produces the entire frame: the conversation, the slash-command
// popup, the input box (or a confirmation prompt) and the status footer. It
// returns the lines plus the hardware cursor position (a line index and column).
func (m *model) buildLines() (lines []string, cursorLine, cursorCol int) {
	width := m.width

	lines = m.conversationLines(width)

	lines = append(lines, m.ac.render(width)...)

	inputStart := len(lines)
	if m.confirm != nil {
		confirmLines := m.confirm.render(width)
		lines = append(lines, confirmLines...)
		cursorLine = inputStart + max(len(confirmLines)-1, 0)
		if len(confirmLines) > 0 {
			cursorCol = min(displayWidth(confirmLines[len(confirmLines)-1]), max(width-1, 0))
		}
	} else {
		editorLines, row, col := m.editor.layout(width)
		lines = append(lines, editorLines...)
		cursorLine = inputStart + row
		cursorCol = col
	}

	lines = append(lines, "")
	lines = append(lines, renderStatus(m.status, width)...)

	return lines, cursorLine, cursorCol
}

// conversationLines renders everything that scrolls: finalized blocks, the
// in-progress streamed block, and any running tool calls. A blank line
// separates each entry.
func (m *model) conversationLines(width int) []string {
	var lines []string
	for _, b := range m.blocks {
		lines = append(lines, b.lines(width)...)
		lines = append(lines, "")
	}
	if m.pending != nil {
		lines = append(lines, m.pendingLines(width)...)
		lines = append(lines, "")
	}
	for _, id := range m.toolOrder {
		if tv := m.tools[id]; tv != nil {
			lines = append(lines, renderToolWithState(tv, width, m.spinnerFrame, m.sessionState)...)
			lines = append(lines, "")
		}
	}
	if m.busy && m.pending == nil && len(m.toolOrder) == 0 {
		lines = append(lines, m.spinnerLine(), "")
	}
	return lines
}

// pendingLines renders the message currently being streamed. Assistant text is
// rendered as markdown live (the same renderer used once it is finalized), so
// formatting appears as it streams.
func (m *model) pendingLines(width int) []string {
	text := m.pending.text.String()
	switch m.pending.kind {
	case blockReasoning:
		return renderReasoningLines(text, width)
	case blockAssistant:
		return renderAssistantLines(text, width)
	default:
		return nil
	}
}

func (m *model) spinnerLine() string {
	frame := spinnerFrames[m.spinnerFrame%len(spinnerFrames)]
	return stAccent().Render(frame) + " " + stMuted().Render("Working…")
}

// confirmState holds a pending tool-approval prompt.
type confirmState struct {
	tool     string // raw tool name, used to scope "always allow"
	toolView toolView
}

func (c *confirmState) render(width int) []string {
	lines := []string{truncate(stWarning().Render("● Approve tool call"), width)}
	lines = append(lines, renderTool(c.toolView, width)...)
	lines = append(lines, truncate(stMuted().Render("[y] yes   [a] always this tool   [s] whole session   [n] no"), width))
	return lines
}
