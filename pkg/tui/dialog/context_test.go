package dialog

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/runtime"
	"github.com/docker/docker-agent/pkg/tui/messages"
)

func testBreakdown() *runtime.ContextBreakdown {
	return &runtime.ContextBreakdown{
		SystemPrompt:      runtime.ContextCategory{Tokens: 1200, Items: 3},
		ToolDefinitions:   runtime.ContextCategory{Tokens: 8400, Items: 23},
		PromptFiles:       runtime.ContextCategory{Tokens: 600, Items: 1},
		Messages:          runtime.ContextCategory{Tokens: 6100, Items: 12},
		ToolResults:       runtime.ContextCategory{Tokens: 8200, Items: 18},
		CompactionSummary: runtime.ContextCategory{Tokens: 900, Items: 1},
		ContextLimit:      128_000,
		Model:             "openai/gpt-4o",
	}
}

func testBreakdownWithFiles() *runtime.ContextBreakdown {
	b := testBreakdown()
	b.AttachedFiles = []runtime.ContextFile{
		{Path: "/proj/main.go", Tokens: 2100},
		{Path: "/proj/docs/readme.md", Tokens: 300},
		{Path: "/tmp/deleted.txt", Missing: true},
	}
	b.PromptFileItems = []runtime.ContextFile{
		{Path: "/proj/AGENTS.md", Tokens: 600},
	}
	return b
}

func TestNewContextDialog(t *testing.T) {
	t.Parallel()

	dialog := NewContextDialog(testBreakdown())
	require.NotNil(t, dialog)
}

func TestContextDialogView(t *testing.T) {
	t.Parallel()

	dialog := NewContextDialog(testBreakdown())
	dialog.SetSize(100, 50)
	view := dialog.View()

	assert.Contains(t, view, "Context Window")
	assert.Contains(t, view, "openai/gpt-4o")
	assert.Contains(t, view, "limit: 128.0K tokens")

	// Every category is listed, even implicitly small ones.
	assert.Contains(t, view, "System prompt")
	assert.Contains(t, view, "Tool definitions")
	assert.Contains(t, view, "Prompt files")
	assert.Contains(t, view, "Messages")
	assert.Contains(t, view, "Tool results")
	assert.Contains(t, view, "Compaction summary")
	assert.Contains(t, view, "Free space")

	// Item counts and the estimate disclaimer.
	assert.Contains(t, view, "(23 tools)")
	assert.Contains(t, view, "(12 messages)")
	assert.Contains(t, view, "(1 file)")
	assert.Contains(t, view, "estimates")

	// Usage summary: 25.4K used of 128K.
	assert.Contains(t, view, "~25.4K of 128.0K tokens")
}

func TestContextDialogViewUnknownLimit(t *testing.T) {
	t.Parallel()

	b := testBreakdown()
	b.ContextLimit = 0
	b.Model = "external-harness"

	dialog := NewContextDialog(b)
	dialog.SetSize(100, 50)
	view := dialog.View()

	assert.Contains(t, view, "context limit unknown")
	assert.Contains(t, view, "tokens estimated")
	assert.NotContains(t, view, "Free space")
}

func TestContextDialogEmptyBreakdown(t *testing.T) {
	t.Parallel()

	dialog := NewContextDialog(&runtime.ContextBreakdown{Model: "openai/gpt-4o"})
	dialog.SetSize(100, 50)
	view := dialog.View()

	assert.Contains(t, view, "Context Window")
	assert.Contains(t, view, "System prompt")
}

func TestContextRowsFreeSpace(t *testing.T) {
	t.Parallel()

	rows := contextRows(testBreakdown())
	require.Len(t, rows, 7)
	last := rows[len(rows)-1]
	assert.True(t, last.free)
	// 128000 - 25400 estimated tokens used.
	assert.Equal(t, int64(102_600), last.tokens)

	// An over-budget estimate must not produce a negative free-space row.
	over := testBreakdown()
	over.Messages.Tokens = 500_000
	rows = contextRows(over)
	require.Len(t, rows, 6)
	for _, row := range rows {
		assert.False(t, row.free)
	}
}

func TestContextPercentLabel(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "-", percentLabel(0, 1000))
	assert.Equal(t, "-", percentLabel(10, 0))
	assert.Equal(t, "<1%", percentLabel(1, 1000))
	assert.Equal(t, "5%", percentLabel(50, 1000))
	assert.Equal(t, "100%", percentLabel(1000, 1000))
}

func TestContextScaleTokens(t *testing.T) {
	t.Parallel()

	b := testBreakdown()
	assert.Equal(t, int64(128_000), scaleTokens(b))

	// Estimate exceeding the limit scales against the estimate so the bar
	// and percentages stay consistent (never above 100%).
	b.Messages.Tokens = 500_000
	assert.Equal(t, b.TotalTokens(), scaleTokens(b))

	// Unknown limit scales against the estimated total.
	b = testBreakdown()
	b.ContextLimit = 0
	assert.Equal(t, b.TotalTokens(), scaleTokens(b))
}

func TestRenderContextBarWidth(t *testing.T) {
	t.Parallel()

	b := testBreakdown()
	rows := contextRows(b)

	for _, width := range []int{1, 10, 40, 80, 200} {
		bar := renderContextBar(rows, scaleTokens(b), width)
		assert.Equal(t, width, lipgloss.Width(bar), "bar must span exactly %d cells", width)
	}

	assert.Empty(t, renderContextBar(rows, 0, 40), "zero scale renders no bar")
	assert.Empty(t, renderContextBar(rows, scaleTokens(b), 0), "zero width renders no bar")
}

func TestContextDialogPlainText(t *testing.T) {
	t.Parallel()

	d := &contextDialog{breakdown: testBreakdown()}
	text := d.renderPlainText()

	assert.Contains(t, text, "Context Window")
	assert.Contains(t, text, "openai/gpt-4o")
	assert.Contains(t, text, "Tool definitions")
	assert.Contains(t, text, "8.4K")
	assert.Contains(t, text, "(23 tools)")
	assert.Contains(t, text, "Free space")
	assert.Contains(t, text, "estimates")
	assert.NotContains(t, text, "\x1b[", "plain text must carry no ANSI escapes")
}

// ---------------------------------------------------------------------------
// File inventory (attached files, prompt files)
// ---------------------------------------------------------------------------

func TestContextDialogViewInventory(t *testing.T) {
	t.Parallel()

	dialog := NewContextDialog(testBreakdownWithFiles())
	dialog.SetSize(120, 50)
	view := dialog.View()

	assert.Contains(t, view, "Attached files")
	assert.Contains(t, view, "main.go")
	assert.Contains(t, view, "readme.md")
	assert.Contains(t, view, "deleted.txt")
	assert.Contains(t, view, "(missing)")
	assert.Contains(t, view, "Prompt files")
	assert.Contains(t, view, "AGENTS.md")
	assert.Contains(t, view, "drop", "help must advertise the drop key")
}

func TestContextDialogViewNoInventory(t *testing.T) {
	t.Parallel()

	dialog := NewContextDialog(testBreakdown())
	dialog.SetSize(100, 50)
	view := dialog.View()

	assert.NotContains(t, view, "Attached files")
	assert.NotContains(t, view, "drop")
}

func keyPress(key rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: key, Text: string(key)}
}

func TestContextDialogSelectionNavigation(t *testing.T) {
	t.Parallel()

	d := NewContextDialog(testBreakdownWithFiles()).(*contextDialog)
	d.SetSize(100, 50)
	d.View()

	require.Equal(t, 0, d.selected, "selection starts on the first attached file")

	d.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	assert.Equal(t, 1, d.selected)

	d.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	d.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // clamped at the last row
	assert.Equal(t, 2, d.selected)

	d.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	d.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	d.Update(tea.KeyPressMsg{Code: tea.KeyUp}) // clamped at the first row
	assert.Equal(t, 0, d.selected)
}

func TestContextDialogDropSelected(t *testing.T) {
	t.Parallel()

	d := NewContextDialog(testBreakdownWithFiles()).(*contextDialog)
	d.SetSize(100, 50)
	d.View()

	d.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	_, cmd := d.Update(keyPress('d'))
	require.NotNil(t, cmd)

	msg := cmd()
	dropMsg, ok := msg.(messages.DropAttachedFileMsg)
	require.True(t, ok, "expected DropAttachedFileMsg, got %T", msg)
	assert.Equal(t, "/proj/docs/readme.md", dropMsg.Path)

	// The row is removed optimistically and the selection stays in bounds.
	require.Len(t, d.breakdown.AttachedFiles, 2)
	assert.Equal(t, "/proj/main.go", d.breakdown.AttachedFiles[0].Path)
	assert.Equal(t, "/tmp/deleted.txt", d.breakdown.AttachedFiles[1].Path)
	assert.Equal(t, 1, d.selected)
}

func TestContextDialogDropLastAttachment(t *testing.T) {
	t.Parallel()

	b := testBreakdown()
	b.AttachedFiles = []runtime.ContextFile{{Path: "/proj/only.go", Tokens: 10}}
	d := NewContextDialog(b).(*contextDialog)
	d.SetSize(100, 50)
	d.View()

	_, cmd := d.Update(keyPress('x'))
	require.NotNil(t, cmd)
	assert.Empty(t, d.breakdown.AttachedFiles)
	assert.Equal(t, -1, d.selected)

	// Further drop/navigation keys are inert and must not panic; up/down
	// fall back to scrolling.
	_, cmd = d.Update(keyPress('d'))
	assert.Nil(t, cmd)
	d.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	assert.Equal(t, -1, d.selected)

	view := d.View()
	assert.NotContains(t, view, "Attached files")
}

func TestContextDialogPlainTextInventory(t *testing.T) {
	t.Parallel()

	d := &contextDialog{breakdown: testBreakdownWithFiles()}
	text := d.renderPlainText()

	assert.Contains(t, text, "Attached files")
	assert.Contains(t, text, "/proj/main.go")
	assert.Contains(t, text, "2.1K")
	assert.Contains(t, text, "/tmp/deleted.txt (missing)")
	assert.Contains(t, text, "Prompt files")
	assert.Contains(t, text, "/proj/AGENTS.md")
	assert.NotContains(t, text, "\x1b[", "plain text must carry no ANSI escapes")
}

func TestTruncateName(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "short.go", truncateName("short.go", 24))
	assert.Equal(t, "a_very_long_file_name_w…", truncateName("a_very_long_file_name_with_suffix.go", 24))
	assert.LessOrEqual(t, lipgloss.Width(truncateName("日本語の長いファイル名.md", 10)), 10)
}

func TestFileLabelWidthClamped(t *testing.T) {
	t.Parallel()

	b := &runtime.ContextBreakdown{
		AttachedFiles: []runtime.ContextFile{{Path: "/p/a_very_long_file_name_that_exceeds_the_column_cap.go"}},
	}
	assert.Equal(t, 24, fileLabelWidth(b))

	b = &runtime.ContextBreakdown{AttachedFiles: []runtime.ContextFile{{Path: "/p/a.go"}}}
	assert.Equal(t, 4, fileLabelWidth(b))
}
