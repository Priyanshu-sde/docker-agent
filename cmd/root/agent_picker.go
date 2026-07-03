package root

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"

	"github.com/docker/docker-agent/pkg/config"
	"github.com/docker/docker-agent/pkg/environment"
	"github.com/docker/docker-agent/pkg/path"
	"github.com/docker/docker-agent/pkg/tui/components/scrollbar"
	"github.com/docker/docker-agent/pkg/tui/components/toolcommon"
	"github.com/docker/docker-agent/pkg/tui/styles"
)

// defaultAgentPickerRefs returns the agent refs offered by the picker when
// the user doesn't pass --agent-picker with an explicit list: the built-in
// agents plus any agent config files found in ~/.agents.
func defaultAgentPickerRefs() []string {
	refs := []string{"default", "coder"}
	home, err := os.UserHomeDir()
	if err != nil {
		return refs
	}
	return append(refs, agentRefsInDir(filepath.Join(home, ".agents"))...)
}

// agentRefsInDir returns the agent config files directly inside dir, sorted
// by name. A missing or unreadable directory yields nothing.
func agentRefsInDir(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var refs []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(entry.Name())) {
		case ".yaml", ".yml", ".hcl":
			refs = append(refs, filepath.Join(dir, entry.Name()))
		}
	}
	return refs
}

// errAgentPickerCancelled is returned when the user aborts the picker
// (Esc / Ctrl-C) without choosing an agent.
var errAgentPickerCancelled = errors.New("agent selection cancelled")

// agentChoice is a single entry in the agent picker.
type agentChoice struct {
	ref         string   // agent reference as passed on the command line
	description string   // one-line description loaded from the agent config
	tags        []string // metadata tags shown as coloured chips
	yaml        string   // raw config YAML, shown in the details dialog
	err         error    // non-nil when the config could not be loaded
}

// loadAgentChoices resolves and loads metadata for each ref so the picker can
// show a name and description. A ref that fails to load is still listed (with
// the error surfaced) so the user can see what went wrong instead of it
// silently disappearing.
func loadAgentChoices(ctx context.Context, refs []string, env environment.Provider) []agentChoice {
	choices := make([]agentChoice, 0, len(refs))
	for _, ref := range refs {
		choice := agentChoice{ref: ref}

		source, err := config.Resolve(ref, env)
		if err != nil {
			choice.err = err
			choices = append(choices, choice)
			continue
		}

		if raw, err := source.Read(ctx); err == nil {
			choice.yaml = string(raw)
		}

		cfg, err := config.Load(ctx, source)
		if err != nil {
			choice.err = err
			choices = append(choices, choice)
			continue
		}

		if len(cfg.Agents) > 0 {
			root := cfg.Agents.First()
			choice.description = root.Description
		}
		if cfg.Metadata.Description != "" {
			choice.description = cfg.Metadata.Description
		}
		choice.tags = cfg.Metadata.Tags
		choices = append(choices, choice)
	}
	return choices
}

// selectAgentRef shows a full-screen picker and returns the chosen agent ref
// along with whether the user wants lean mode. The "Lean Mode" checkbox is
// seeded with initialLean (the effective lean state from flags/user config)
// so what the user sees always matches what will run; the returned value is
// authoritative. When only a single ref is supplied there is nothing to
// choose, so it is returned directly without showing any UI.
func selectAgentRef(ctx context.Context, refs []string, env environment.Provider, initialLean bool) (ref string, lean bool, err error) {
	if len(refs) == 0 {
		return "", false, errors.New("no agent refs to choose from")
	}
	if len(refs) == 1 {
		return refs[0], initialLean, nil
	}

	choices := loadAgentChoices(ctx, refs, env)
	m := newAgentPickerModel(choices)
	m.leanMode = initialLean

	p := tea.NewProgram(m, tea.WithContext(ctx))
	final, err := p.Run()
	if err != nil {
		return "", false, err
	}

	result, ok := final.(*agentPickerModel)
	if !ok || result.cancelled {
		return "", false, errAgentPickerCancelled
	}
	return result.choices[result.cursor].ref, result.leanMode, nil
}

// agentPickerKeyMap holds the key bindings for the agent picker.
type agentPickerKeyMap struct {
	Up      key.Binding
	Down    key.Binding
	Choose  key.Binding
	Details key.Binding
	Lean    key.Binding
	Quit    key.Binding
}

var agentPickerKeys = agentPickerKeyMap{
	Up: key.NewBinding(
		key.WithKeys("up", "k"),
		key.WithHelp("↑/k", "up"),
	),
	Down: key.NewBinding(
		key.WithKeys("down", "j"),
		key.WithHelp("↓/j", "down"),
	),
	Choose: key.NewBinding(
		key.WithKeys("enter", " "),
		key.WithHelp("enter", "select"),
	),
	Details: key.NewBinding(
		key.WithKeys("?"),
		key.WithHelp("?", "view yaml"),
	),
	Lean: key.NewBinding(
		key.WithKeys("l"),
		key.WithHelp("l", "toggle lean mode"),
	),
	Quit: key.NewBinding(
		key.WithKeys("esc", "ctrl+c", "q"),
		key.WithHelp("esc", "cancel"),
	),
}

// agentPickerModel is the bubbletea model backing the full-screen picker.
type agentPickerModel struct {
	choices   []agentChoice
	cursor    int
	width     int
	height    int
	cancelled bool

	// leanMode mirrors the "Lean Mode" checkbox: when ticked the chosen
	// agent runs in the lean TUI instead of the full one. Seeded by the
	// caller with the effective lean state (off by default).
	leanMode bool

	// showDetails toggles the scrollable YAML dialog overlay for the
	// currently selected agent.
	showDetails bool
	details     viewport.Model
	detailsBar  *scrollbar.Model

	// lastClickIndex and lastClickTime back double-click detection on the
	// agent cards: a second left-click on the same card within the threshold
	// selects it.
	lastClickIndex int
	lastClickTime  time.Time
}

func newAgentPickerModel(choices []agentChoice) *agentPickerModel {
	vp := viewport.New()
	vp.FillHeight = true
	// Truncate long lines instead of soft-wrapping them: the config's long
	// instruction blocks would otherwise wrap across dozens of rows and bloat
	// the viewer. Horizontal scrolling remains available.
	vp.SoftWrap = false
	return &agentPickerModel{
		choices:        choices,
		details:        vp,
		detailsBar:     scrollbar.New(),
		lastClickIndex: -1,
	}
}

func (m *agentPickerModel) Init() tea.Cmd { return nil }

func (m *agentPickerModel) moveUp() {
	if m.cursor > 0 {
		m.cursor--
	}
}

func (m *agentPickerModel) moveDown() {
	if m.cursor < len(m.choices)-1 {
		m.cursor++
	}
}

func (m *agentPickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resizeDetails()
		return m, nil
	case tea.KeyPressMsg:
		// While the YAML dialog is open it captures all keys: scrolling is
		// delegated to the viewport, and any close key dismisses it.
		if m.showDetails {
			switch {
			case key.Matches(msg, agentPickerKeys.Quit), key.Matches(msg, agentPickerKeys.Details):
				m.showDetails = false
				m.resetClickTracking()
				return m, nil
			}
			var cmd tea.Cmd
			m.details, cmd = m.details.Update(msg)
			m.syncDetailsBar()
			return m, cmd
		}

		switch {
		case key.Matches(msg, agentPickerKeys.Quit):
			m.cancelled = true
			return m, tea.Quit
		case key.Matches(msg, agentPickerKeys.Up):
			m.moveUp()
			return m, nil
		case key.Matches(msg, agentPickerKeys.Down):
			m.moveDown()
			return m, nil
		case key.Matches(msg, agentPickerKeys.Details):
			m.openDetails()
			return m, nil
		case key.Matches(msg, agentPickerKeys.Lean):
			m.leanMode = !m.leanMode
			return m, nil
		case key.Matches(msg, agentPickerKeys.Choose):
			return m, tea.Quit
		}
	case tea.MouseWheelMsg:
		if m.showDetails {
			var cmd tea.Cmd
			m.details, cmd = m.details.Update(msg)
			m.syncDetailsBar()
			return m, cmd
		}
		return m, nil
	case tea.MouseMotionMsg:
		if !m.showDetails {
			if i, ok := m.cardAt(msg.X, msg.Y); ok {
				m.cursor = i
			}
		}
		return m, nil
	case tea.MouseClickMsg:
		return m.handleMouseClick(msg)
	}
	return m, nil
}

// handleMouseClick moves the cursor to the clicked card and treats a second
// left-click on the same card (within the double-click threshold) as a
// selection. Clicks are ignored while the YAML dialog is open.
func (m *agentPickerModel) handleMouseClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	if m.showDetails || msg.Button != tea.MouseLeft {
		return m, nil
	}
	if m.leanCheckboxAt(msg.X, msg.Y) {
		m.leanMode = !m.leanMode
		m.resetClickTracking()
		return m, nil
	}
	i, ok := m.cardAt(msg.X, msg.Y)
	if !ok {
		m.lastClickIndex = -1
		return m, nil
	}
	m.cursor = i

	now := time.Now()
	if m.lastClickIndex == i && now.Sub(m.lastClickTime) < styles.DoubleClickThreshold {
		m.lastClickIndex = -1
		return m, tea.Quit
	}
	m.lastClickIndex = i
	m.lastClickTime = now
	return m, nil
}

// Fixed YAML dialog dimensions. Keeping them constant means the dialog never
// moves or resizes while scrolling. They shrink only when the terminal is too
// small to hold the preferred size.
const (
	detailsDialogWidth  = 110
	detailsDialogHeight = 36

	// detailsChromeRows is the number of rows used by the dialog around the
	// scrollable content: border (2) + padding (2) + title (1) + blank (1) +
	// help (1).
	detailsChromeRows = 7
	// detailsChromeCols is the number of columns used by the dialog around
	// the content: border (2) + padding (4) + scrollbar (1).
	detailsChromeCols = 2 + 4 + scrollbar.Width
)

// detailsDialogSize returns the outer width and height of the YAML dialog,
// clamped so it always fits on screen with a small margin.
func (m *agentPickerModel) detailsDialogSize() (w, h int) {
	w = min(detailsDialogWidth, max(m.width-4, 20))
	h = min(detailsDialogHeight, max(m.height-2, detailsChromeRows+1))
	return w, h
}

// viewportSize returns the inner content dimensions of the YAML viewport.
func (m *agentPickerModel) viewportSize() (w, h int) {
	dw, dh := m.detailsDialogSize()
	return max(dw-detailsChromeCols, 1), max(dh-detailsChromeRows, 1)
}

// resizeDetails keeps the viewport and its scrollbar sized to the current
// dialog dimensions.
func (m *agentPickerModel) resizeDetails() {
	w, h := m.viewportSize()
	m.details.SetWidth(w)
	m.details.SetHeight(h)
	m.syncDetailsBar()
}

// syncDetailsBar mirrors the viewport's scroll state into the scrollbar.
func (m *agentPickerModel) syncDetailsBar() {
	m.detailsBar.SetDimensions(m.details.Height(), m.details.TotalLineCount())
	m.detailsBar.SetScrollOffset(m.details.YOffset())
}

// openDetails loads the selected agent's YAML into the viewport and shows the
// dialog.
func (m *agentPickerModel) openDetails() {
	if m.cursor < 0 || m.cursor >= len(m.choices) {
		return
	}
	m.resetClickTracking()
	m.resizeDetails()
	m.details.SetContent(m.detailsContent(m.choices[m.cursor]))
	m.details.GotoTop()
	m.syncDetailsBar()
	m.showDetails = true
}

// resetClickTracking clears double-click state so an unrelated later click
// can't be paired with a stale earlier one (e.g. across opening/closing the
// details dialog).
func (m *agentPickerModel) resetClickTracking() {
	m.lastClickIndex = -1
	m.lastClickTime = time.Time{}
}

// detailsContent returns the text shown in the YAML dialog for a choice.
func (m *agentPickerModel) detailsContent(choice agentChoice) string {
	switch {
	case choice.yaml != "":
		return highlightYAML(strings.TrimRight(choice.yaml, "\n"))
	case choice.err != nil:
		return "Failed to load agent:\n\n" + sanitizeYAML(choice.err.Error())
	default:
		return "No configuration available."
	}
}

// highlightYAML syntax-colorizes YAML using chroma with the active TUI theme.
// On any tokenisation error it returns the (sanitized) source unchanged.
func highlightYAML(src string) string {
	src = sanitizeYAML(src)
	lexer := lexers.Get("yaml")
	if lexer == nil {
		return src
	}
	iterator, err := chroma.Coalesce(lexer).Tokenise(nil, src)
	if err != nil {
		return src
	}

	style := styles.ChromaStyle()
	var b strings.Builder
	for _, token := range iterator.Tokens() {
		b.WriteString(chromaTokenStyle(token.Type, style).Render(token.Value))
	}
	return b.String()
}

// sanitizeYAML normalizes line endings, expands tabs, and strips terminal
// control characters from config content that may come from untrusted (remote)
// sources, so it cannot inject escape sequences or break the dialog layout.
func sanitizeYAML(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ReplaceAll(s, "\t", "    ")
	return stripControl(s)
}

// stripControl removes control characters (including ESC) that could inject
// terminal escape sequences or corrupt the layout. Newlines are preserved.
func stripControl(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// chromaTokenStyle maps a chroma token type to a lipgloss style using the
// given chroma style (theme).
func chromaTokenStyle(tokenType chroma.TokenType, style *chroma.Style) lipgloss.Style {
	entry := style.Get(tokenType)
	s := lipgloss.NewStyle()
	if entry.Colour.IsSet() {
		s = s.Foreground(lipgloss.Color(entry.Colour.String()))
	}
	if entry.Bold == chroma.Yes {
		s = s.Bold(true)
	}
	if entry.Italic == chroma.Yes {
		s = s.Italic(true)
	}
	return s
}

func (m *agentPickerModel) View() tea.View {
	var body string
	if m.showDetails {
		body = m.renderDetails()
	} else {
		body = m.render()
	}
	centered := lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, body)

	view := tea.NewView(centered)
	view.AltScreen = true
	view.MouseMode = tea.MouseModeAllMotion
	view.BackgroundColor = styles.Background
	view.WindowTitle = "Select an agent"
	return view
}

// agent picker card dimensions.
const (
	agentPickerCardWidth    = 70
	agentPickerMinCardWidth = 24

	// agentPickerCardHeight is the rendered height of a card: 3 content rows
	// (header + detail + tags) wrapped by one row of vertical padding and a
	// border on the top and bottom.
	agentPickerCardHeight = 7

	// agentPickerCardGap is the number of blank rows between adjacent cards.
	agentPickerCardGap = 0

	// agentPickerCardsTop is the number of rows from the panel's top edge to
	// the first card: border (1) + padding (1) + title (1) + blank (1) +
	// subtitle (1) + blank separator (1).
	agentPickerCardsTop = 6
	// agentPickerCardsLeft is the number of columns from the panel's left
	// edge to a card: border (1) + padding (4).
	agentPickerCardsLeft = 5
)

// cardWidth returns the card width to use, shrinking to fit narrow terminals.
// The card is wrapped by the outer panel border (1) + padding (4) on each
// side, so it must leave room for that chrome.
func (m *agentPickerModel) cardWidth() int {
	w := agentPickerCardWidth
	if m.width > 0 {
		if fit := m.width - 2*(1+4); fit < w {
			w = fit
		}
	}
	if w < agentPickerMinCardWidth {
		w = agentPickerMinCardWidth
	}
	return w
}

// panelOrigin returns the top-left corner of the centered picker panel.
func (m *agentPickerModel) panelOrigin() (x, y int) {
	panelWidth, panelHeight := m.panelSize()
	return max((m.width-panelWidth)/2, 0), max((m.height-panelHeight)/2, 0)
}

// cardRows returns the number of rows occupied by the stacked cards,
// including the gaps between them.
func (m *agentPickerModel) cardRows() int {
	return len(m.choices)*agentPickerCardHeight + max(len(m.choices)-1, 0)*agentPickerCardGap
}

// cardAt maps terminal coordinates to the index of the agent card under them.
// It mirrors the layout produced by render: the panel is centered, and cards
// are stacked with no gaps below the title/subtitle. The bool is false when
// the point is outside every card.
func (m *agentPickerModel) cardAt(x, y int) (int, bool) {
	originX, originY := m.panelOrigin()

	cardWidth := m.cardWidth()
	relX := x - originX - agentPickerCardsLeft
	relY := y - originY - agentPickerCardsTop
	if relX < 0 || relX >= cardWidth || relY < 0 {
		return 0, false
	}
	// Cards are stacked with a blank gap between them; a click landing in the
	// gap belongs to no card.
	stride := agentPickerCardHeight + agentPickerCardGap
	if relY%stride >= agentPickerCardHeight {
		return 0, false
	}
	i := relY / stride
	if i >= len(m.choices) {
		return 0, false
	}
	return i, true
}

// leanCheckboxAt reports whether terminal coordinates land on the "Lean
// Mode" checkbox row. It mirrors the layout produced by render: the checkbox
// sits one blank row below the last card, at the cards' left offset.
func (m *agentPickerModel) leanCheckboxAt(x, y int) bool {
	originX, originY := m.panelOrigin()

	checkboxY := originY + agentPickerCardsTop + m.cardRows() + 1
	if y != checkboxY {
		return false
	}
	relX := x - originX - agentPickerCardsLeft
	return relX >= 0 && relX < lipgloss.Width(m.leanCheckbox())
}

// leanCheckbox renders the "Lean Mode" checkbox line.
func (m *agentPickerModel) leanCheckbox() string {
	box := styles.MutedStyle.Render("[ ]")
	if m.leanMode {
		box = styles.SuccessStyle.Render("[x]")
	}
	return box + " " + styles.SecondaryStyle.Render("Lean Mode")
}

// panelSize returns the outer dimensions of the rendered picker panel without
// rendering every card. cardAt relies on it to place hit zones, and it is
// called on every mouse-motion event, so it must stay cheap: cards all share
// cardWidth, so only the (variable-width) header lines need measuring.
func (m *agentPickerModel) panelSize() (w, h int) {
	title, subtitle, help := m.headerText()
	contentWidth := max(
		m.cardWidth(),
		lipgloss.Width(title),
		lipgloss.Width(subtitle),
		lipgloss.Width(help),
	)
	// Horizontal chrome: border (1) + padding (4) on each side.
	w = contentWidth + 2*(1+4)
	// Content rows: title + blank + subtitle + blank + cards (with gaps) +
	// blank + lean checkbox + blank + help.
	rows := 4 + m.cardRows() + 4
	// Vertical chrome: border (2) + padding (2).
	h = rows + 4
	return w, h
}

// headerText returns the (styled) title, subtitle, and help lines shared by
// render and panelSize so their layout math can't drift apart.
func (m *agentPickerModel) headerText() (title, subtitle, help string) {
	title = styles.HighlightWhiteStyle.Render("Choose an agent to run")
	subtitle = styles.MutedStyle.Render("Pick the agent you want to start a session with, or double-click a card.")
	help = styles.MutedStyle.Render(
		strings.Join([]string{
			"↑↓ move",
			"double-click select",
			agentPickerKeys.Choose.Help().Key + " " + agentPickerKeys.Choose.Help().Desc,
			agentPickerKeys.Details.Help().Key + " " + agentPickerKeys.Details.Help().Desc,
			agentPickerKeys.Lean.Help().Key + " " + agentPickerKeys.Lean.Help().Desc,
			agentPickerKeys.Quit.Help().Key + " " + agentPickerKeys.Quit.Help().Desc,
		}, "   "),
	)
	return title, subtitle, help
}

func (m *agentPickerModel) render() string {
	title, subtitle, help := m.headerText()

	cardWidth := m.cardWidth()
	blocks := make([]string, 0, len(m.choices)*2)
	for i, choice := range m.choices {
		if i > 0 && agentPickerCardGap > 0 {
			blocks = append(blocks, strings.Repeat("\n", agentPickerCardGap-1))
		}
		blocks = append(blocks, m.renderCard(choice, cardWidth, i == m.cursor))
	}
	list := lipgloss.JoinVertical(lipgloss.Left, blocks...)

	content := lipgloss.JoinVertical(
		lipgloss.Left,
		title,
		"",
		subtitle,
		"",
		list,
		"",
		m.leanCheckbox(),
		"",
		help,
	)

	return styles.BaseStyle.
		Border(lipgloss.RoundedBorder()).
		BorderForeground(styles.BorderSecondary).
		Padding(1, 4).
		Render(content)
}

// renderDetails renders the scrollable YAML dialog for the selected agent.
func (m *agentPickerModel) renderDetails() string {
	dw, _ := m.detailsDialogSize()
	contentWidth := dw - detailsChromeCols + scrollbar.Width

	ref := displayRef(m.choices[m.cursor].ref)
	title := styles.DialogTitleStyle.Width(contentWidth).Render(toolcommon.TruncateText(ref, contentWidth))

	// Place the scrollbar immediately to the right of the viewport content.
	// Reserve the column even when the content fits (empty scrollbar view) so
	// the dialog width stays fixed.
	_, vh := m.viewportSize()
	bar := m.detailsBar.View()
	if bar == "" {
		bar = strings.TrimRight(strings.Repeat(" \n", vh), "\n")
	}
	body := lipgloss.JoinHorizontal(
		lipgloss.Top,
		m.details.View(),
		bar,
	)

	help := styles.MutedStyle.
		Width(contentWidth).
		Render(strings.Join([]string{
			"↑↓ scroll",
			percentLabel(m.details.ScrollPercent()),
			"esc/? close",
		}, "   "))

	content := lipgloss.JoinVertical(
		lipgloss.Left,
		title,
		body,
		"",
		help,
	)

	return styles.DialogStyle.Render(content)
}

// percentLabel formats a scroll fraction (0..1) as a percentage string.
func percentLabel(frac float64) string {
	pct := min(max(int(frac*100), 0), 100)
	return strconv.Itoa(pct) + "%"
}

// isLocalConfigRef reports whether ref points at a local agent config file
// (as opposed to a built-in name, OCI image, or URL).
func isLocalConfigRef(ref string) bool {
	if config.IsURLReference(ref) {
		return false
	}
	switch strings.ToLower(filepath.Ext(ref)) {
	case ".yaml", ".yml", ".hcl":
		return true
	default:
		return false
	}
}

// displayRef returns the ref as shown to the user: local config paths are
// shortened with "~", everything else is unchanged.
func displayRef(ref string) string {
	if isLocalConfigRef(ref) {
		return path.ShortenHome(ref)
	}
	return ref
}

func (m *agentPickerModel) renderCard(choice agentChoice, cardWidth int, selected bool) string {
	marker := "  "
	nameStyle := styles.BoldStyle
	borderColor := styles.BorderMuted
	if selected {
		marker = styles.SuccessStyle.Render("❯ ")
		nameStyle = styles.HighlightWhiteStyle
		borderColor = styles.BorderPrimary
	}

	// The marker occupies 2 columns and the card chrome (border + padding)
	// 4, so the title and detail text get cardWidth-6. Titles and details can
	// come from arbitrary (including remote) configs, so collapse them to a
	// single line and truncate to fit the card.
	detailWidth := cardWidth - 6
	title := displayRef(choice.ref)
	var detail string
	switch {
	case choice.err != nil:
		detail = styles.ErrorStyle.Render(truncateDetail("failed to load: "+choice.err.Error(), detailWidth))
	case choice.description != "" && isLocalConfigRef(choice.ref):
		// Local config files show their description as the title; the path is
		// demoted to the detail line.
		title = choice.description
		detail = styles.MutedStyle.Render(truncateDetail(displayRef(choice.ref), detailWidth))
	case choice.description != "":
		detail = styles.SecondaryStyle.Render(truncateDetail(choice.description, detailWidth))
	default:
		detail = styles.MutedStyle.Render("No description available")
	}
	header := marker + nameStyle.Render(truncateDetail(title, detailWidth))

	card := lipgloss.JoinVertical(lipgloss.Left, header, "  "+detail, "  "+renderTags(choice.tags, detailWidth))

	return styles.BaseStyle.
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Width(cardWidth).
		Padding(1, 1).
		Render(card)
}

// tagChipStyles are the rotating colour palette used to render tag chips so
// adjacent tags are visually distinct.
var tagChipStyles = []lipgloss.Style{
	styles.BaseStyle.Foreground(styles.BadgePurple).Bold(true),
	styles.BaseStyle.Foreground(styles.BadgeCyan).Bold(true),
	styles.BaseStyle.Foreground(styles.BadgeGreen).Bold(true),
	styles.BaseStyle.Foreground(styles.Info).Bold(true),
}

// renderTags renders the agent's metadata tags as coloured chips, collapsed
// onto a single line and truncated to width so they can't break the card
// layout. It returns an empty (blank) line when there are no tags, keeping the
// card height uniform for hit-testing.
func renderTags(tags []string, width int) string {
	if len(tags) == 0 || width <= 0 {
		return ""
	}
	chips := make([]string, 0, len(tags))
	used := 0
	for i, tag := range tags {
		tag = stripControl(strings.TrimSpace(tag))
		if tag == "" {
			continue
		}
		label := "#" + tag
		// Account for the single-space separator between chips.
		sep := 0
		if len(chips) > 0 {
			sep = 1
		}
		if used+sep+lipgloss.Width(label) > width {
			break
		}
		used += sep + lipgloss.Width(label)
		style := tagChipStyles[i%len(tagChipStyles)]
		chips = append(chips, style.Render(label))
	}
	return strings.Join(chips, " ")
}

// truncateDetail collapses whitespace (including newlines) into single spaces,
// strips terminal control characters, and truncates the result to width
// columns. This keeps card-detail text on a single line so untrusted or
// multi-line descriptions can't break the layout or inject escape sequences.
func truncateDetail(text string, width int) string {
	return toolcommon.TruncateText(stripControl(strings.Join(strings.Fields(text), " ")), width)
}

// prependAgentRef returns args with ref inserted as the leading positional
// argument. After an --agent-picker selection the remaining positional args
// are user messages, and the rest of the run pipeline expects args[0] to be
// the agent ref.
func prependAgentRef(ref string, args []string) []string {
	return append([]string{ref}, args...)
}

// parseAgentPickerRefs splits a comma-separated list of agent refs, trims
// whitespace, and drops empty entries. An empty or all-whitespace input
// yields the built-in defaults.
func parseAgentPickerRefs(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return defaultAgentPickerRefs()
	}
	var refs []string
	for part := range strings.SplitSeq(raw, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			refs = append(refs, trimmed)
		}
	}
	if len(refs) == 0 {
		return defaultAgentPickerRefs()
	}
	return refs
}
