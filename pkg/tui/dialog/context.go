package dialog

import (
	"fmt"
	"image/color"
	"path/filepath"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/atotto/clipboard"

	pathx "github.com/docker/docker-agent/pkg/path"
	"github.com/docker/docker-agent/pkg/runtime"
	"github.com/docker/docker-agent/pkg/tui/components/notification"
	"github.com/docker/docker-agent/pkg/tui/components/scrollview"
	"github.com/docker/docker-agent/pkg/tui/core"
	"github.com/docker/docker-agent/pkg/tui/core/layout"
	"github.com/docker/docker-agent/pkg/tui/messages"
	"github.com/docker/docker-agent/pkg/tui/styles"
)

// ---------------------------------------------------------------------------
// contextDialog – TUI dialog displaying the context-window composition
// ---------------------------------------------------------------------------

type contextDialog struct {
	BaseDialog

	breakdown  *runtime.ContextBreakdown
	keyMap     contextDialogKeyMap
	scrollview *scrollview.Model

	// selected indexes breakdown.AttachedFiles; -1 when the inventory has
	// nothing to select.
	selected int
	// attachedLines maps each attached file to its line index in the
	// scrollable content region. Rebuilt on every render and used to keep
	// the selection visible while navigating.
	attachedLines []int
}

type contextDialogKeyMap struct {
	Close, Copy, Up, Down, Drop key.Binding
}

// NewContextDialog creates the /context dialog showing the estimated
// context-window composition by category, plus the per-file inventory of
// attached files (droppable) and prompt files.
func NewContextDialog(breakdown *runtime.ContextBreakdown) Dialog {
	if breakdown == nil {
		breakdown = &runtime.ContextBreakdown{}
	}
	d := &contextDialog{
		breakdown: breakdown,
		selected:  -1,
		scrollview: scrollview.New(
			scrollview.WithKeyMap(scrollview.ReadOnlyScrollKeyMap()),
			scrollview.WithReserveScrollbarSpace(true),
		),
		keyMap: contextDialogKeyMap{
			Close: key.NewBinding(key.WithKeys("esc", "enter", "q"), key.WithHelp("Esc", "close")),
			Copy:  key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "copy")),
			Up:    key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑", "select")),
			Down:  key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓", "select")),
			Drop:  key.NewBinding(key.WithKeys("d", "x", "backspace", "delete"), key.WithHelp("d", "drop")),
		},
	}
	if len(breakdown.AttachedFiles) > 0 {
		d.selected = 0
	}
	return d
}

func (d *contextDialog) Init() tea.Cmd { return nil }

func (d *contextDialog) Update(msg tea.Msg) (layout.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyPressMsg); ok {
		if cmd, handled := d.handleKey(keyMsg); handled {
			return d, cmd
		}
	}
	if handled, cmd := d.scrollview.Update(msg); handled {
		return d, cmd
	}
	if msg, ok := msg.(tea.WindowSizeMsg); ok {
		cmd := d.SetSize(msg.Width, msg.Height)
		return d, cmd
	}
	return d, nil
}

// handleKey processes dialog-level keys. Up/Down are claimed for selection
// only while attached files are listed; without them the keys fall through
// to the scrollview and keep their plain scrolling behavior.
func (d *contextDialog) handleKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	hasAttached := len(d.breakdown.AttachedFiles) > 0
	switch {
	case key.Matches(msg, d.keyMap.Close):
		return core.CmdHandler(CloseDialogMsg{}), true
	case key.Matches(msg, d.keyMap.Copy):
		_ = clipboard.WriteAll(d.renderPlainText())
		return notification.SuccessCmd("Context breakdown copied to clipboard."), true
	case key.Matches(msg, d.keyMap.Up) && hasAttached:
		d.moveSelection(-1)
		return nil, true
	case key.Matches(msg, d.keyMap.Down) && hasAttached:
		d.moveSelection(1)
		return nil, true
	case key.Matches(msg, d.keyMap.Drop) && hasAttached:
		return d.dropSelected(), true
	}
	return nil, false
}

// moveSelection moves the attached-file selection by delta, clamped to the
// list bounds, and scrolls the selected row into view.
func (d *contextDialog) moveSelection(delta int) {
	n := len(d.breakdown.AttachedFiles)
	if n == 0 {
		return
	}
	d.selected = min(max(d.selected+delta, 0), n-1)
	if d.selected < len(d.attachedLines) {
		d.scrollview.EnsureLineVisible(d.attachedLines[d.selected])
	}
}

// dropSelected removes the selected attached file from the local inventory
// and asks the app to drop it from the session. The local removal is
// optimistic, mirroring the session-browser delete flow; the handler reports
// success or failure through a notification.
func (d *contextDialog) dropSelected() tea.Cmd {
	files := d.breakdown.AttachedFiles
	if d.selected < 0 || d.selected >= len(files) {
		return nil
	}
	path := files[d.selected].Path
	d.breakdown.AttachedFiles = slices.Delete(files, d.selected, d.selected+1)
	if d.selected >= len(d.breakdown.AttachedFiles) {
		d.selected = len(d.breakdown.AttachedFiles) - 1
	}
	return core.CmdHandler(messages.DropAttachedFileMsg{Path: path})
}

func (d *contextDialog) dialogSize() (dialogWidth, maxHeight, contentWidth int) {
	dialogWidth = d.ComputeDialogWidth(70, 50, 100)
	maxHeight = min(d.Height()*70/100, 30)
	contentWidth = d.ContentWidth(dialogWidth, 2) - d.scrollview.ReservedCols()
	return dialogWidth, maxHeight, contentWidth
}

func (d *contextDialog) Position() (row, col int) {
	dialogWidth, maxHeight, _ := d.dialogSize()
	return CenterPosition(d.Width(), d.Height(), dialogWidth, maxHeight)
}

func (d *contextDialog) View() string {
	dialogWidth, maxHeight, contentWidth := d.dialogSize()
	content := d.renderContent(contentWidth, maxHeight)
	return styles.DialogStyle.Padding(1, 2).Width(dialogWidth).Render(content)
}

// ---------------------------------------------------------------------------
// Row model – one renderable category
// ---------------------------------------------------------------------------

// contextRow is one line of the breakdown: a category (or the free-space
// remainder) with its estimated token count and item count.
type contextRow struct {
	label  string
	tokens int64
	items  int
	noun   string // singular item noun ("message", "tool", "file", "result")
	free   bool   // true for the free-space remainder row
}

// contextRows flattens the breakdown into display order. Categories are
// always listed (zero values included) so users see every bucket the
// runtime accounts for; the free-space row is appended only when the
// context limit is known and not already exceeded by the estimate.
func contextRows(b *runtime.ContextBreakdown) []contextRow {
	rows := []contextRow{
		{label: "System prompt", tokens: b.SystemPrompt.Tokens, items: b.SystemPrompt.Items, noun: "message"},
		{label: "Tool definitions", tokens: b.ToolDefinitions.Tokens, items: b.ToolDefinitions.Items, noun: "tool"},
		{label: "Prompt files", tokens: b.PromptFiles.Tokens, items: b.PromptFiles.Items, noun: "file"},
		{label: "Messages", tokens: b.Messages.Tokens, items: b.Messages.Items, noun: "message"},
		{label: "Tool results", tokens: b.ToolResults.Tokens, items: b.ToolResults.Items, noun: "result"},
		{label: "Compaction summary", tokens: b.CompactionSummary.Tokens, items: b.CompactionSummary.Items, noun: "summary"},
	}
	if free := b.ContextLimit - b.TotalTokens(); b.ContextLimit > 0 && free > 0 {
		rows = append(rows, contextRow{label: "Free space", tokens: free, free: true})
	}
	return rows
}

// itemsSuffix returns the parenthesized item count, e.g. "(12 messages)".
func (r *contextRow) itemsSuffix() string {
	if r.free || r.items == 0 {
		return ""
	}
	noun := r.noun
	if r.items > 1 {
		noun += "s"
	}
	return fmt.Sprintf("(%d %s)", r.items, noun)
}

// scaleTokens returns the denominator percentages and the usage bar are
// computed against: the context limit when known, otherwise the estimated
// total (the bar then shows relative composition instead of fill level).
func scaleTokens(b *runtime.ContextBreakdown) int64 {
	if b.ContextLimit > 0 {
		return max(b.ContextLimit, b.TotalTokens())
	}
	return b.TotalTokens()
}

// percentLabel formats tokens as a percentage of scale: "<1%" for tiny
// non-zero slices, "-" for empty ones.
func percentLabel(tokens, scale int64) string {
	if tokens <= 0 || scale <= 0 {
		return "-"
	}
	pct := float64(tokens) / float64(scale) * 100
	if pct < 1 {
		return "<1%"
	}
	return fmt.Sprintf("%.0f%%", pct)
}

// ---------------------------------------------------------------------------
// Styled rendering (TUI view)
// ---------------------------------------------------------------------------

// contextBarGlyphs are the block glyphs of the stacked usage bar.
const (
	contextBarFilled = "█"
	contextBarFree   = "░"
	contextRowMarker = "■"
)

// contextHeaderLines is the number of fixed (non-scrolling) lines at the top
// of the dialog: title (with its meta line) + separator + spacer.
const contextHeaderLines = 3

// contextEstimateNote labels every figure in the dialog as an estimate, as
// the counts come from a heuristic rather than the provider's tokenizer.
const contextEstimateNote = "Token counts are estimates; the provider's tokenizer may count differently."

// contextDropNote explains the scope of dropping an attachment: the file
// stops being shared forward, but past messages keep their inlined copy.
const contextDropNote = "Dropping a file removes it from the session's attachment list (sub-agents and skills stop receiving it); content already inlined in past messages stays until compaction."

// categoryColors returns the per-category accent colors, aligned with the
// order of contextRows. Hues are used categorically (Error's rose tint
// carries no alarm semantics here); each maps to a distinct color in the
// bundled themes. A function (not a var) so it picks up theme changes.
func categoryColors() []color.Color {
	return []color.Color{
		styles.MobyBlue,    // system prompt
		styles.Info,        // tool definitions
		styles.BadgePurple, // prompt files
		styles.Success,     // messages
		styles.Warning,     // tool results
		styles.Error,       // compaction summary
	}
}

func (d *contextDialog) renderContent(contentWidth, maxHeight int) string {
	b := d.breakdown
	rows := contextRows(b)
	scale := scaleTokens(b)

	header := RenderTitle("Context Window", contentWidth, styles.DialogTitleStyle)
	if meta := contextHeaderMeta(b); meta != "" {
		header += "\n" + styles.DialogOptionsStyle.Width(contentWidth).Render(meta)
	}

	lines := []string{
		header,
		RenderSeparator(contentWidth),
		"",
		renderContextBar(rows, scale, contentWidth),
		styles.MutedStyle.Render(usageSummary(b)),
		"",
	}

	labelWidth := contextLabelWidth(rows)
	colors := categoryColors()
	for i, row := range rows {
		lines = append(lines, renderContextRow(&row, scale, labelWidth, markerColor(i, row, colors)))
	}

	lines = d.appendInventory(lines, scale, contentWidth)

	lines = append(lines, "")
	lines = append(lines, wrapMutedLines(contextEstimateNote, contentWidth)...)
	if len(b.AttachedFiles) > 0 {
		lines = append(lines, wrapMutedLines(contextDropNote, contentWidth)...)
	}

	return d.applyScrolling(lines, contentWidth, maxHeight)
}

// appendInventory renders the per-file inventory under the category rows:
// the session's attached files (selectable, droppable) and the resolved
// prompt files (config-driven, read-only). Empty sections are omitted. The
// content-region line index of every attached row is recorded so navigation
// can keep the selection visible.
func (d *contextDialog) appendInventory(lines []string, scale int64, contentWidth int) []string {
	d.attachedLines = d.attachedLines[:0]
	b := d.breakdown
	labelWidth := fileLabelWidth(b)

	if len(b.AttachedFiles) > 0 {
		lines = append(lines, "", sectionStyle().Render("Attached files"))
		for i := range b.AttachedFiles {
			d.attachedLines = append(d.attachedLines, len(lines)-contextHeaderLines)
			lines = append(lines, renderContextFileRow(&b.AttachedFiles[i], scale, labelWidth, contentWidth, i == d.selected))
		}
	}
	if len(b.PromptFileItems) > 0 {
		lines = append(lines, "", sectionStyle().Render("Prompt files"))
		for i := range b.PromptFileItems {
			lines = append(lines, renderContextFileRow(&b.PromptFileItems[i], scale, labelWidth, contentWidth, false))
		}
	}
	return lines
}

// fileLabelWidth returns the display width of the inventory file-name
// column, sized to the longest base name across both sections and clamped
// so one long name cannot push the token columns off screen.
func fileLabelWidth(b *runtime.ContextBreakdown) int {
	width := 0
	for _, files := range [][]runtime.ContextFile{b.AttachedFiles, b.PromptFileItems} {
		for i := range files {
			width = max(width, lipgloss.Width(filepath.Base(files[i].Path)))
		}
	}
	return min(max(width, 4), 24)
}

// truncateName shortens name to maxWidth display cells, ellipsizing the end.
func truncateName(name string, maxWidth int) string {
	if lipgloss.Width(name) <= maxWidth {
		return name
	}
	r := []rune(name)
	for len(r) > 0 && lipgloss.Width(string(r))+1 > maxWidth {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}

// renderContextFileRow renders one inventory line:
// "▶ main.go   2.1K   2%  ~/proj/main.go".
func renderContextFileRow(file *runtime.ContextFile, scale int64, labelWidth, contentWidth int, selected bool) string {
	prefix := "  "
	nameStyle := labelStyle()
	if selected {
		prefix = accentStyle().Render("▶ ")
		nameStyle = nameStyle.Foreground(styles.Highlight)
	}
	name := truncateName(filepath.Base(file.Path), labelWidth)
	pad := strings.Repeat(" ", max(0, labelWidth-lipgloss.Width(name)))

	line := fmt.Sprintf("%s%s%s  %s  %s",
		prefix,
		nameStyle.Render(name),
		pad,
		valueStyle().Render(padRight(fileTokensLabel(file))),
		valueStyle().Render(fmt.Sprintf("%4s", percentLabel(file.Tokens, scale))))

	if suffix := filePathSuffix(file, contentWidth-lipgloss.Width(line)-2); suffix != "" {
		line += "  " + suffix
	}
	return line
}

// fileTokensLabel formats a file's token estimate, "-" when it contributes
// nothing inline (missing, binary-unsupported, or oversized files).
func fileTokensLabel(file *runtime.ContextFile) string {
	if file.Tokens <= 0 {
		return "-"
	}
	return formatTokenCount(file.Tokens)
}

// filePathSuffix renders the muted, home-shortened path (plus a missing
// marker) that trails a file row, truncated to the available width. The
// path is omitted when there is no room for a meaningful fragment.
func filePathSuffix(file *runtime.ContextFile, available int) string {
	const missingMark = "(missing)"
	var parts []string
	if file.Missing {
		available -= lipgloss.Width(missingMark) + 1
	}
	if available >= 8 {
		parts = append(parts, styles.MutedStyle.Render(truncatePath(pathx.ShortenHome(file.Path), available)))
	}
	if file.Missing {
		parts = append(parts, styles.ErrorStyle.Render(missingMark))
	}
	return strings.Join(parts, " ")
}

// wrapMutedLines wraps text to width in the muted style and returns the
// individual lines, so the scrollview's line accounting stays exact.
func wrapMutedLines(text string, width int) []string {
	return strings.Split(styles.MutedStyle.Width(width).Render(text), "\n")
}

// markerColor picks the row's accent color: its category color, or muted
// for the free-space remainder.
func markerColor(i int, row contextRow, colors []color.Color) color.Color {
	if row.free || i >= len(colors) {
		return styles.TextMutedGray
	}
	return colors[i]
}

// contextHeaderMeta returns the "model • limit" line under the title.
func contextHeaderMeta(b *runtime.ContextBreakdown) string {
	var parts []string
	if b.Model != "" {
		parts = append(parts, b.Model)
	}
	if b.ContextLimit > 0 {
		parts = append(parts, "limit: "+formatTokenCount(b.ContextLimit)+" tokens")
	} else {
		parts = append(parts, "context limit unknown")
	}
	return strings.Join(parts, "  •  ")
}

// usageSummary is the line under the bar: "~24.5K of 128.0K tokens (19%)",
// or just the estimated total when the limit is unknown.
func usageSummary(b *runtime.ContextBreakdown) string {
	total := b.TotalTokens()
	if b.ContextLimit <= 0 {
		return "~" + formatTokenCount(total) + " tokens estimated"
	}
	return fmt.Sprintf("~%s of %s tokens (%s)",
		formatTokenCount(total),
		formatTokenCount(b.ContextLimit),
		percentLabel(total, scaleTokens(b)))
}

// renderContextBar renders the stacked usage bar: one colored segment per
// category, proportional to its share of scale, with the remainder drawn
// as muted free-space cells. Cumulative rounding keeps the bar exactly
// barWidth cells wide with no drift.
func renderContextBar(rows []contextRow, scale int64, barWidth int) string {
	if barWidth < 1 || scale <= 0 {
		return ""
	}
	colors := categoryColors()
	var bar strings.Builder
	cells := 0
	var cum int64
	for i, row := range rows {
		if row.free {
			continue
		}
		cum += row.tokens
		end := int(float64(cum) / float64(scale) * float64(barWidth))
		if n := min(end, barWidth) - cells; n > 0 {
			bar.WriteString(lipgloss.NewStyle().
				Foreground(markerColor(i, row, colors)).
				Render(strings.Repeat(contextBarFilled, n)))
			cells += n
		}
	}
	if n := barWidth - cells; n > 0 {
		bar.WriteString(styles.MutedStyle.Render(strings.Repeat(contextBarFree, n)))
	}
	return bar.String()
}

// contextLabelWidth returns the widest row label, so token columns align.
func contextLabelWidth(rows []contextRow) int {
	width := 0
	for _, row := range rows {
		width = max(width, len(row.label))
	}
	return width
}

// renderContextRow renders one category line:
// "■ Tool definitions   8.4K   7%  (23 tools)".
func renderContextRow(row *contextRow, scale int64, labelWidth int, markerCol color.Color) string {
	marker := lipgloss.NewStyle().Foreground(markerCol).Render(contextRowMarker)
	label := row.label + strings.Repeat(" ", labelWidth-len(row.label))
	if row.free {
		label = styles.MutedStyle.Render(label)
	} else {
		label = labelStyle().Render(label)
	}
	line := fmt.Sprintf("%s %s  %s  %s",
		marker,
		label,
		valueStyle().Render(padRight(formatTokenCount(row.tokens))),
		valueStyle().Render(fmt.Sprintf("%4s", percentLabel(row.tokens, scale))))
	if suffix := row.itemsSuffix(); suffix != "" {
		line += "  " + styles.MutedStyle.Render(suffix)
	}
	return line
}

func (d *contextDialog) applyScrolling(allLines []string, contentWidth, maxHeight int) string {
	const footerLines = 2 // space + help

	visibleLines := max(1, maxHeight-contextHeaderLines-footerLines-4)
	contentLines := allLines[contextHeaderLines:]

	regionWidth := contentWidth + d.scrollview.ReservedCols()
	d.scrollview.SetSize(regionWidth, visibleLines)

	dialogRow, dialogCol := d.Position()
	d.scrollview.SetPosition(dialogCol+3, dialogRow+2+contextHeaderLines)
	d.scrollview.SetContent(contentLines, len(contentLines))

	parts := make([]string, 0, contextHeaderLines+3)
	parts = append(parts, allLines[:contextHeaderLines]...)
	parts = append(parts, d.scrollview.View(), "", RenderHelpKeys(regionWidth, d.helpKeys()...))
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// helpKeys returns the footer bindings; the selection/drop pair appears
// only while attached files are listed.
func (d *contextDialog) helpKeys() []string {
	if len(d.breakdown.AttachedFiles) > 0 {
		return []string{"↑↓", "select", "d", "drop", "c", "copy", "Esc", "close"}
	}
	return []string{"↑↓", "scroll", "c", "copy", "Esc", "close"}
}

// ---------------------------------------------------------------------------
// Plain-text rendering (clipboard copy)
// ---------------------------------------------------------------------------

func (d *contextDialog) renderPlainText() string {
	b := d.breakdown
	rows := contextRows(b)
	scale := scaleTokens(b)
	labelWidth := contextLabelWidth(rows)

	lines := []string{"Context Window"}
	if meta := contextHeaderMeta(b); meta != "" {
		lines = append(lines, meta)
	}
	lines = append(lines, "", usageSummary(b), "")

	for _, row := range rows {
		line := fmt.Sprintf("%s  %-8s %4s",
			row.label+strings.Repeat(" ", labelWidth-len(row.label)),
			formatTokenCount(row.tokens),
			percentLabel(row.tokens, scale))
		if suffix := row.itemsSuffix(); suffix != "" {
			line += "  " + suffix
		}
		lines = append(lines, line)
	}

	if len(b.AttachedFiles) > 0 {
		lines = append(lines, "", "Attached files")
		for i := range b.AttachedFiles {
			lines = append(lines, plainFileLine(&b.AttachedFiles[i], scale))
		}
	}
	if len(b.PromptFileItems) > 0 {
		lines = append(lines, "", "Prompt files")
		for i := range b.PromptFileItems {
			lines = append(lines, plainFileLine(&b.PromptFileItems[i], scale))
		}
	}

	lines = append(lines, "", contextEstimateNote)
	return strings.Join(lines, "\n")
}

// plainFileLine formats one inventory file for the clipboard copy.
func plainFileLine(file *runtime.ContextFile, scale int64) string {
	line := fmt.Sprintf("%-8s %4s  %s", fileTokensLabel(file), percentLabel(file.Tokens, scale), file.Path)
	if file.Missing {
		line += " (missing)"
	}
	return line
}
