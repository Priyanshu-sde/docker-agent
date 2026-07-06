// Package tui implements the full-screen Kanban TUI for `docker agent board`.
package tui

import (
	"context"
	"image/color"
	"os/exec"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/atotto/clipboard"

	"github.com/docker/docker-agent/pkg/board"
	"github.com/docker/docker-agent/pkg/tui/core"
	"github.com/docker/docker-agent/pkg/tui/styles"
)

// Run starts the board TUI and blocks until the user quits.
func Run(ctx context.Context) error {
	// The engine notifies changes from watcher goroutines; a buffered
	// channel coalesces bursts and the model turns receives into refreshes.
	refresh := make(chan struct{}, 16)
	app, err := board.NewApp(ctx, func() {
		select {
		case refresh <- struct{}{}:
		default:
		}
	})
	if err != nil {
		return err
	}

	p := tea.NewProgram(newModel(app, refresh), tea.WithContext(ctx))
	_, err = p.Run()
	return err
}

// Messages.
type (
	// refreshMsg means the engine changed: re-snapshot the cards.
	refreshMsg struct{}
	// tickMsg advances the spinner animation.
	tickMsg struct{}
	// flashMsg shows a transient message in the footer.
	flashMsg struct {
		text  string
		isErr bool
	}
	// clearFlashMsg hides an expired footer message.
	clearFlashMsg struct{ id int }
	// attachReadyMsg carries the tmux attach command for a card whose agent
	// answered its readiness probe.
	attachReadyMsg struct{ cmd *exec.Cmd }
	// attachFailedMsg means the readiness probe failed; the attach guard is
	// released and the error shown.
	attachFailedMsg struct{ err error }
	// attachDoneMsg means the user detached from a card's tmux session.
	attachDoneMsg struct{ err error }
	// diffLoadedMsg carries a card's worktree diff.
	diffLoadedMsg struct {
		cardID string
		title  string
		diff   string
		offset int
	}
	// reloadDiffMsg re-reads an open diff dialog's worktree diff, keeping
	// the scroll position.
	reloadDiffMsg struct {
		cardID string
		title  string
		offset int
	}
	// cardCreatedMsg means a new card landed in the first column.
	cardCreatedMsg struct{}
	// cardMovedMsg means the selected card landed in another column.
	cardMovedMsg struct{ colIdx int }

	// closeDialogMsg closes the active dialog.
	closeDialogMsg struct{}
	// submitNewCardMsg creates a card from the new-card dialog.
	submitNewCardMsg struct {
		project board.Project
		prompt  string
	}
	// submitProjectMsg adds a project from the projects dialog.
	submitProjectMsg struct{ project board.Project }
	// deleteProjectMsg removes a project from the projects dialog.
	deleteProjectMsg struct{ name string }
	// submitPromptMsg saves a column prompt from the prompt editor.
	submitPromptMsg struct {
		colID  string
		prompt string
	}
	// confirmDeleteMsg deletes a card after confirmation.
	confirmDeleteMsg struct{ cardID string }
)

// dialog is a modal overlay. Dialogs emit model-level messages (via
// tea.Cmd) to request actions; the model owns all engine calls.
type dialog interface {
	Init() tea.Cmd
	Update(msg tea.Msg) (dialog, tea.Cmd)
	View(width, height int) string
}

// keyMap holds the board's key bindings.
type keyMap struct {
	Left     key.Binding
	Right    key.Binding
	Up       key.Binding
	Down     key.Binding
	First    key.Binding
	Last     key.Binding
	New      key.Binding
	Attach   key.Binding
	Diff     key.Binding
	MoveFwd  key.Binding
	MoveBack key.Binding
	Delete   key.Binding
	Projects key.Binding
	Prompt   key.Binding
	Editor   key.Binding
	Help     key.Binding
	Quit     key.Binding
}

var keys = keyMap{
	Left:     key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("←/h", "previous column")),
	Right:    key.NewBinding(key.WithKeys("right", "l"), key.WithHelp("→/l", "next column")),
	Up:       key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "previous card")),
	Down:     key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "next card")),
	First:    key.NewBinding(key.WithKeys("g", "home"), key.WithHelp("g", "first card")),
	Last:     key.NewBinding(key.WithKeys("G", "end"), key.WithHelp("G", "last card")),
	New:      key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new card")),
	Attach:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "attach to agent (ctrl+q detaches)")),
	Diff:     key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "view diff")),
	MoveFwd:  key.NewBinding(key.WithKeys("]", "shift+right", "L"), key.WithHelp("]", "move card forward")),
	MoveBack: key.NewBinding(key.WithKeys("[", "shift+left", "H"), key.WithHelp("[", "move card back")),
	Delete:   key.NewBinding(key.WithKeys("x", "backspace", "delete"), key.WithHelp("x", "delete card")),
	Projects: key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "manage projects")),
	Prompt:   key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit column prompt")),
	Editor:   key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "open worktree in editor")),
	Help:     key.NewBinding(key.WithKeys("?", "f1", "ctrl+h"), key.WithHelp("?", "help")),
	Quit:     key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
}

// resolveKeys merges the user's remapped global bindings (from the config
// file, resolved by the main TUI's keymap) into the board's defaults, so a
// remapped quit works here too. Called at model construction, after the
// config directory override has been applied.
func resolveKeys() {
	globalQuit := core.GetKeys().Quit
	keys.Quit = key.NewBinding(
		key.WithKeys(append([]string{"q"}, globalQuit.Keys()...)...),
		key.WithHelp("q", "quit"),
	)
}

// model is the top-level bubbletea model of the board.
type model struct {
	app     *board.App
	refresh chan struct{}

	width, height int

	columns []board.Column
	// cards holds each column's cards in board order, keyed by column ID.
	cards map[string][]*board.Card
	// projects is a snapshot of the configured projects, cached so View
	// never takes the engine's config lock.
	projects []board.Project
	// projectColors is each configured project's accent color, keyed by name.
	projectColors map[string]color.Color

	// selCol/selRow is the cursor; selRow is clamped per column.
	selCol, selRow int
	// scroll is each column's first visible card index.
	scroll map[string]int
	// colScroll is the first visible column when the terminal is too narrow
	// to fit the whole pipeline (see columnWindow).
	colScroll int

	// projStartX/projEndX is the clickable project-count hitbox in the top
	// header, recorded by renderHeader. wtStartX/wtEndX is the clickable
	// card-details hitbox in the footer, recorded by renderFooter.
	projStartX, projEndX int
	wtStartX, wtEndX     int

	frame  int  // spinner animation frame
	ticker bool // whether a tick is scheduled

	flash   string
	flashID int
	isErr   bool

	dialog dialog

	// attaching guards against queueing a second attach while one is being
	// probed or is on screen: each queued tea.ExecProcess would otherwise
	// replay after the previous detach.
	attaching bool

	// lastProject is the project of the most recently created card; the
	// new-card dialog starts there.
	lastProject string

	// lastClick* back double-click-to-attach on cards.
	lastClickCard string
	lastClickTime time.Time
}

func newModel(app *board.App, refresh chan struct{}) *model {
	resolveKeys()
	m := &model{
		app:     app,
		refresh: refresh,
		scroll:  make(map[string]int),
	}
	m.reload()
	return m
}

// openDialog installs a dialog and runs its init command.
func (m *model) openDialog(d dialog) tea.Cmd {
	m.dialog = d
	return d.Init()
}

// reload re-snapshots columns and cards from the engine and clamps the
// selection.
func (m *model) reload() {
	m.columns = m.app.Columns()
	m.cards = groupCards(m.columns, m.app.Cards())
	m.projects = m.app.Projects()
	m.projectColors = make(map[string]color.Color)
	for i, p := range m.projects {
		m.projectColors[p.Name] = projectColorAt(i)
	}
	m.clampSelection()
}

// groupCards buckets cards by column, in board order. A card whose column
// is no longer configured lands in the first column instead of silently
// disappearing from the board.
func groupCards(columns []board.Column, cards []*board.Card) map[string][]*board.Card {
	known := make(map[string]bool, len(columns))
	for _, c := range columns {
		known[c.ID] = true
	}
	grouped := make(map[string][]*board.Card, len(columns))
	for _, card := range cards {
		col := card.Column
		if !known[col] {
			col = columns[0].ID
		}
		grouped[col] = append(grouped[col], card)
	}
	return grouped
}

func (m *model) clampSelection() {
	if len(m.columns) == 0 {
		return
	}
	m.selCol = clamp(m.selCol, 0, len(m.columns)-1)
	m.selRow = clamp(m.selRow, 0, max(len(m.selectedColumnCards())-1, 0))
}

func (m *model) selectedColumnCards() []*board.Card {
	if len(m.columns) == 0 {
		return nil
	}
	return m.cards[m.columns[m.selCol].ID]
}

func (m *model) selectedCard() *board.Card {
	cards := m.selectedColumnCards()
	if len(cards) == 0 {
		return nil
	}
	return cards[m.selRow]
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(m.waitRefresh(), m.scheduleTick())
}

// waitRefresh turns engine change notifications into refresh messages.
func (m *model) waitRefresh() tea.Cmd {
	return func() tea.Msg {
		<-m.refresh
		return refreshMsg{}
	}
}

// anyBusy reports whether any card is animating (starting or running).
func (m *model) anyBusy() bool {
	for _, cards := range m.cards {
		for _, c := range cards {
			if c.Status.Busy() {
				return true
			}
		}
	}
	return false
}

// scheduleTick keeps the spinner animation running only while needed.
func (m *model) scheduleTick() tea.Cmd {
	if m.ticker || !m.anyBusy() {
		return nil
	}
	m.ticker = true
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case refreshMsg:
		m.reload()
		return m, tea.Batch(m.waitRefresh(), m.scheduleTick())

	case tickMsg:
		m.ticker = false
		m.frame++
		cmd := m.scheduleTick()
		return m, cmd

	case flashMsg:
		cmd := m.setFlash(msg.text, msg.isErr)
		return m, cmd

	case clearFlashMsg:
		if msg.id == m.flashID {
			m.flash = ""
		}
		return m, nil

	case cardCreatedMsg:
		// Follow the new card: it lands at the end of the first column.
		m.dialog = nil
		m.reload()
		m.selCol = 0
		m.selRow = max(len(m.selectedColumnCards())-1, 0)
		cmd := m.scheduleTick()
		return m, cmd

	case cardMovedMsg:
		// Follow the moved card: it lands at the end of its new column.
		m.reload()
		m.selCol = clamp(msg.colIdx, 0, max(len(m.columns)-1, 0))
		m.selRow = max(len(m.selectedColumnCards())-1, 0)
		cmd := m.scheduleTick()
		return m, cmd

	case attachReadyMsg:
		return m, tea.ExecProcess(msg.cmd, func(err error) tea.Msg { return attachDoneMsg{err: err} })

	case attachFailedMsg:
		m.attaching = false
		cmd := m.setFlash(msg.err.Error(), true)
		return m, cmd

	case attachDoneMsg:
		m.attaching = false
		if msg.err != nil {
			cmd := m.setFlash("attach: "+msg.err.Error(), true)
			return m, cmd
		}
		return m, nil

	case diffLoadedMsg:
		cmd := m.openDialog(newDiffDialog(msg.cardID, msg.title, msg.diff, msg.offset))
		return m, cmd

	case reloadDiffMsg:
		cmd := m.loadDiff(msg.cardID, msg.title, msg.offset)
		return m, cmd

	case closeDialogMsg:
		m.dialog = nil
		return m, nil

	case submitNewCardMsg:
		m.dialog = nil
		m.lastProject = msg.project.Name
		cmd := m.createCard(msg.project, msg.prompt)
		return m, cmd

	case submitProjectMsg:
		if err := m.app.AddProject(msg.project); err != nil {
			cmd := m.setFlash(err.Error(), true)
			return m, cmd
		}
		m.reload()
		if d, ok := m.dialog.(*projectsDialog); ok {
			d.setProjects(m.projects)
		}
		cmd := m.setFlash("Project added to the global config", false)
		return m, cmd

	case deleteProjectMsg:
		if err := m.app.RemoveProject(msg.name); err != nil {
			cmd := m.setFlash(err.Error(), true)
			return m, cmd
		}
		m.reload()
		if d, ok := m.dialog.(*projectsDialog); ok {
			d.setProjects(m.projects)
		}
		return m, nil

	case submitPromptMsg:
		m.dialog = nil
		if err := m.app.SetColumnPrompt(msg.colID, msg.prompt); err != nil {
			cmd := m.setFlash(err.Error(), true)
			return m, cmd
		}
		m.reload()
		cmd := m.setFlash("Prompt saved to the global config", false)
		return m, cmd

	case confirmDeleteMsg:
		m.dialog = nil
		cmd := m.deleteCard(msg.cardID)
		return m, cmd
	}

	if m.dialog != nil {
		// The global quit binding (ctrl+c by default, user-remappable)
		// always quits, even while a dialog captures the keyboard. Plain q
		// stays with the dialog: it may be typed text.
		if press, ok := msg.(tea.KeyPressMsg); ok && key.Matches(press, core.GetKeys().Quit) {
			return m, tea.Quit
		}
		var cmd tea.Cmd
		m.dialog, cmd = m.dialog.Update(msg)
		return m, cmd
	}

	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	case tea.MouseClickMsg:
		return m.handleClick(msg)
	case tea.MouseWheelMsg:
		m.handleWheel(msg)
		return m, nil
	}
	return m, nil
}

func (m *model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, keys.Quit):
		return m, tea.Quit

	case key.Matches(msg, core.GetKeys().Suspend):
		return m, tea.Suspend

	case key.Matches(msg, keys.Left):
		m.moveSelection(-1, 0)
	case key.Matches(msg, keys.Right):
		m.moveSelection(1, 0)
	case key.Matches(msg, keys.Up):
		m.moveSelection(0, -1)
	case key.Matches(msg, keys.Down):
		m.moveSelection(0, 1)
	case key.Matches(msg, keys.First):
		m.selRow = 0
	case key.Matches(msg, keys.Last):
		m.selRow = max(len(m.selectedColumnCards())-1, 0)

	case key.Matches(msg, keys.New):
		cmd := m.openNewCard()
		return m, cmd

	case key.Matches(msg, keys.Attach):
		if card := m.selectedCard(); card != nil {
			cmd := m.attach(card.ID)
			return m, cmd
		}

	case key.Matches(msg, keys.Diff):
		if card := m.selectedCard(); card != nil {
			cmd := m.loadDiff(card.ID, card.Title, 0)
			return m, cmd
		}

	case key.Matches(msg, keys.MoveFwd):
		cmd := m.moveCard(1)
		return m, cmd
	case key.Matches(msg, keys.MoveBack):
		cmd := m.moveCard(-1)
		return m, cmd

	case key.Matches(msg, keys.Delete):
		if card := m.selectedCard(); card != nil {
			cmd := m.openDialog(newConfirmDialog(card))
			return m, cmd
		}

	case key.Matches(msg, keys.Projects):
		cmd := m.openDialog(newProjectsDialog(m.projects))
		return m, cmd

	case key.Matches(msg, keys.Prompt):
		if len(m.columns) > 0 {
			cmd := m.openDialog(newPromptDialog(m.columns[m.selCol]))
			return m, cmd
		}

	case key.Matches(msg, keys.Editor):
		if card := m.selectedCard(); card != nil {
			if err := m.app.OpenEditor(card.ID); err != nil {
				cmd := m.setFlash(err.Error(), true)
				return m, cmd
			}
			cmd := m.setFlash("Opened "+card.Worktree+" in the editor", false)
			return m, cmd
		}

	case key.Matches(msg, keys.Help):
		cmd := m.openDialog(newHelpDialog())
		return m, cmd
	}
	return m, nil
}

// openNewCard opens the new-card dialog, or the projects dialog first when
// no project is configured yet.
func (m *model) openNewCard() tea.Cmd {
	if len(m.projects) == 0 {
		return tea.Batch(
			m.openDialog(newProjectsDialog(nil)),
			m.setFlash("Add a project first: cards are created against a project", false),
		)
	}
	return m.openDialog(newCardDialog(m.projects, m.lastProject))
}

// moveSelection moves the cursor by column (dx) or row (dy).
func (m *model) moveSelection(dx, dy int) {
	if len(m.columns) == 0 {
		return
	}
	if dx != 0 {
		m.selCol = clamp(m.selCol+dx, 0, len(m.columns)-1)
	}
	if dy != 0 {
		m.selRow += dy
	}
	m.clampSelection()
}

// handleClick selects the clicked card; a double-click attaches to it. A
// click on the first column's + button starts a new card.
func (m *model) handleClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	if msg.Button != tea.MouseLeft {
		return m, nil
	}
	if m.plusButtonAt(msg.X, msg.Y) {
		cmd := m.openNewCard()
		return m, cmd
	}
	if m.projectsButtonAt(msg.X, msg.Y) {
		cmd := m.openDialog(newProjectsDialog(m.projects))
		return m, cmd
	}
	// A click on a column's title row opens that column's prompt editor.
	if col, ok := m.columnAt(msg.X, msg.Y); ok && msg.Y == boardTop+1 {
		m.selCol = col
		m.clampSelection()
		cmd := m.openDialog(newPromptDialog(m.columns[col]))
		return m, cmd
	}
	// A click on the footer's card details copies the worktree path.
	if m.worktreeButtonAt(msg.X, msg.Y) {
		if card := m.selectedCard(); card != nil {
			worktree := card.Worktree
			cmd := tea.Batch(
				tea.SetClipboard(worktree),
				func() tea.Msg { _ = clipboard.WriteAll(worktree); return nil },
				m.setFlash("Worktree path copied: "+worktree, false),
			)
			return m, cmd
		}
	}
	col, row, ok := m.cardAt(msg.X, msg.Y)
	if !ok {
		m.lastClickCard = ""
		return m, nil
	}
	m.selCol, m.selRow = col, row
	m.clampSelection()

	card := m.selectedCard()
	if card == nil {
		return m, nil
	}
	if m.lastClickCard == card.ID && time.Since(m.lastClickTime) < styles.DoubleClickThreshold {
		m.lastClickCard = ""
		cmd := m.attach(card.ID)
		return m, cmd
	}
	m.lastClickCard = card.ID
	m.lastClickTime = time.Now()
	return m, nil
}

// handleWheel moves the selection through the column under the cursor, so
// scrolling anywhere on a column walks its cards (the scroll window follows
// the selection). Wheel events outside the columns area are ignored.
func (m *model) handleWheel(msg tea.MouseWheelMsg) {
	col, ok := m.columnAt(msg.X, msg.Y)
	if !ok {
		return
	}
	if col != m.selCol {
		m.selCol = col
		m.clampSelection()
	}
	switch msg.Button {
	case tea.MouseWheelUp:
		m.moveSelection(0, -1)
	case tea.MouseWheelDown:
		m.moveSelection(0, 1)
	}
}

// setFlash shows a transient footer message for a few seconds. The text is
// sanitized and collapsed to one line: errors can embed untrusted content.
func (m *model) setFlash(text string, isErr bool) tea.Cmd {
	m.flash = strings.Join(strings.Fields(sanitize(text)), " ")
	m.isErr = isErr
	m.flashID++
	id := m.flashID
	return tea.Tick(4*time.Second, func(time.Time) tea.Msg { return clearFlashMsg{id: id} })
}

// --- engine commands (all engine calls that can block run in tea.Cmds) ---

func (m *model) createCard(project board.Project, prompt string) tea.Cmd {
	return func() tea.Msg {
		if _, err := m.app.CreateCard(project, prompt); err != nil {
			return flashMsg{text: err.Error(), isErr: true}
		}
		return cardCreatedMsg{}
	}
}

func (m *model) moveCard(direction int) tea.Cmd {
	card := m.selectedCard()
	if card == nil {
		return nil
	}
	dst := m.selCol + direction
	if dst < 0 || dst >= len(m.columns) {
		return nil
	}
	colID := m.columns[dst].ID
	return func() tea.Msg {
		if err := m.app.MoveCard(card.ID, colID); err != nil {
			return flashMsg{text: err.Error(), isErr: true}
		}
		return cardMovedMsg{colIdx: dst}
	}
}

func (m *model) deleteCard(cardID string) tea.Cmd {
	return func() tea.Msg {
		if err := m.app.DeleteCard(cardID); err != nil {
			return flashMsg{text: err.Error(), isErr: true}
		}
		return flashMsg{text: "Card deleted, worktree and session cleaned up", isErr: false}
	}
}

// attach probes the card's agent readiness off the UI loop, then hands the
// tmux attach command back to the update loop to exec. The attaching guard
// stays set until the session detaches (or the probe fails).
func (m *model) attach(cardID string) tea.Cmd {
	if m.attaching {
		return nil
	}
	m.attaching = true
	return func() tea.Msg {
		cmd, err := m.app.AttachCommand(cardID)
		if err != nil {
			return attachFailedMsg{err: err}
		}
		return attachReadyMsg{cmd: cmd}
	}
}

func (m *model) loadDiff(cardID, title string, offset int) tea.Cmd {
	return func() tea.Msg {
		diff, err := m.app.Diff(cardID)
		if err != nil {
			return flashMsg{text: err.Error(), isErr: true}
		}
		return diffLoadedMsg{cardID: cardID, title: title, diff: diff, offset: offset}
	}
}

func clamp(v, lo, hi int) int {
	return min(max(v, lo), hi)
}
