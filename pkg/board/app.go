package board

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/docker/docker-agent/pkg/paths"
	"github.com/docker/docker-agent/pkg/userconfig"
)

// DefaultAgent is the agent ref used for projects that do not set one.
const DefaultAgent = "default"

// Project is a repository cards can be created against, configured in the
// user's global config file (or through the TUI, which persists there).
type Project struct {
	Name  string
	Path  string
	Agent string
}

// App is the board engine: it owns the cards, the per-card agent sessions,
// and the configuration (projects and columns) stored in the user's global
// config file.
type App struct {
	// ctx is the board-lifetime context used by engine operations that
	// outlive a single UI interaction (git commands, tmux attach).
	ctx        context.Context //nolint:containedctx // board-lifetime context
	store      *Store
	sessions   sessionManager
	controller *controller

	// mu guards config (the mutable projects/columns section of the user's
	// global config file).
	mu      sync.Mutex
	config  *userconfig.Config
	columns []Column

	onChanged func()
}

// NewApp loads the board state and reattaches to any sessions still running
// in tmux. onChanged is called (from arbitrary goroutines) whenever a card
// changes, so the UI can refresh.
func NewApp(ctx context.Context, onChanged func()) (*App, error) {
	if _, err := exec.LookPath("tmux"); err != nil {
		return nil, errors.New("the board runs each agent in a tmux session: please install tmux first")
	}

	// One board per state file: a second instance would run its own
	// watchers and race this one relaunching agents.
	if err := acquireLock(StatePath() + ".lock"); err != nil {
		return nil, err
	}

	cfg, err := userconfig.Load()
	if err != nil {
		return nil, fmt.Errorf("load user config: %w", err)
	}

	store, err := OpenStore(StatePath())
	if err != nil {
		return nil, err
	}

	var columns []userconfig.BoardColumn
	if cfg.Board != nil {
		columns = cfg.Board.Columns
	}

	app := &App{
		ctx:       ctx,
		store:     store,
		sessions:  tmuxSessions{ctx: ctx},
		config:    cfg,
		columns:   ColumnsFromConfig(columns),
		onChanged: onChanged,
	}
	app.controller = newController(ctx, store, app.sessions, onChanged)
	app.controller.ReconcileAll()
	return app, nil
}

// Columns returns the board's pipeline.
func (a *App) Columns() []Column {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.columns)
}

// ColumnIndex returns the position of a column in the pipeline, or -1.
func (a *App) ColumnIndex(colID string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.IndexFunc(a.columns, func(c Column) bool { return c.ID == colID })
}

// SetColumnPrompt updates a column's prompt and persists it to the user's
// global config file.
func (a *App) SetColumnPrompt(colID, prompt string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	i := slices.IndexFunc(a.columns, func(c Column) bool { return c.ID == colID })
	if i < 0 {
		return fmt.Errorf("unknown column %q", colID)
	}
	a.columns[i].Prompt = prompt
	return a.saveConfigLocked()
}

// saveConfigLocked persists the projects and columns to the global config
// file. Callers must hold a.mu.
func (a *App) saveConfigLocked() error {
	board := &userconfig.Board{}
	if a.config.Board != nil {
		*board = *a.config.Board
	}
	if slices.Equal(a.columns, DefaultColumns) {
		// Keep the config free of the built-in pipeline so future changes to
		// the defaults reach users who never customized their columns.
		board.Columns = nil
	} else {
		board.Columns = make([]userconfig.BoardColumn, 0, len(a.columns))
		for _, c := range a.columns {
			board.Columns = append(board.Columns, userconfig.BoardColumn{ID: c.ID, Name: c.Name, Emoji: c.Emoji, Prompt: c.Prompt})
		}
	}
	a.config.Board = board
	return a.config.Save()
}

// Projects returns the configured projects.
func (a *App) Projects() []Project {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.config.Board == nil {
		return nil
	}
	projects := make([]Project, 0, len(a.config.Board.Projects))
	for _, p := range a.config.Board.Projects {
		projects = append(projects, Project{Name: p.Name, Path: p.Path, Agent: p.Agent})
	}
	return projects
}

// AddProject validates and appends a project, persisting it to the user's
// global config file. The path is normalized to an absolute path (expanding
// a leading ~) so cards never depend on the board's working directory.
func (a *App) AddProject(p Project) error {
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("project name is required")
	}
	path, err := normalizeProjectPath(p.Path)
	if err != nil {
		return err
	}
	p.Path = path
	if !isGitRepo(a.ctx, p.Path) {
		return fmt.Errorf("%s is not a git repository", p.Path)
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.config.Board == nil {
		a.config.Board = &userconfig.Board{}
	}
	for _, existing := range a.config.Board.Projects {
		if existing.Name == p.Name {
			return fmt.Errorf("project %q already exists", p.Name)
		}
	}
	a.config.Board.Projects = append(a.config.Board.Projects, userconfig.BoardProject{
		Name:  p.Name,
		Path:  p.Path,
		Agent: p.Agent,
	})
	return a.saveConfigLocked()
}

// normalizeProjectPath expands a leading ~ and makes the path absolute. An
// empty path is rejected: it would silently validate against the board's
// working directory.
func normalizeProjectPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("project path is required")
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		path = filepath.Join(paths.GetHomeDir(), strings.TrimPrefix(path[1:], "/"))
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve project path: %w", err)
	}
	return abs, nil
}

// RemoveProject deletes a project by name and persists the change. Existing
// cards keep the repo path and agent they were created with.
func (a *App) RemoveProject(name string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.config.Board == nil {
		return nil
	}
	a.config.Board.Projects = slices.DeleteFunc(a.config.Board.Projects, func(p userconfig.BoardProject) bool {
		return p.Name == name
	})
	return a.saveConfigLocked()
}

// Cards returns all cards in board order.
func (a *App) Cards() []*Card {
	return a.store.ListCards()
}

// CreateCard creates a card in the first column and launches its agent
// session. docker agent creates the isolated git worktree (named after the
// card) on first launch and exposes its control plane on a per-card unix
// socket; the board records where the worktree lives and starts watching
// the session. The title is a placeholder derived from the prompt, replaced
// when the agent emits its session_title event, so card creation is instant.
func (a *App) CreateCard(project Project, prompt string) (card *Card, err error) {
	if strings.TrimSpace(prompt) == "" {
		return nil, errors.New("prompt is required")
	}
	agent := project.Agent
	if agent == "" {
		agent = DefaultAgent
	}

	worktreeName := newWorktreeName()
	branch := worktreeBranch(worktreeName)
	wtPath := worktreeDir(worktreeName)
	sessionName := newSessionName()
	agentSession := newID()

	defer func() {
		if err != nil {
			_ = a.sessions.KillSession(sessionName)
			removeWorktree(a.ctx, project.Path, wtPath, branch)
		}
	}()

	// Launch from the repository: --worktree branches the new worktree from
	// the repo's upstream base (detected, not assumed).
	base := upstreamBase(a.ctx, project.Path)
	if err := a.sessions.NewSession(sessionName, project.Path, agent, agentSession, socketPath(agentSession), worktreeName, base, prompt); err != nil {
		return nil, fmt.Errorf("tmux session: %w", err)
	}

	firstColumn := a.Columns()[0].ID
	card = &Card{
		ID:           newID(),
		Title:        placeholderTitle(prompt),
		Column:       firstColumn,
		Status:       StatusStarting,
		Project:      project.Name,
		Agent:        agent,
		RepoPath:     project.Path,
		Branch:       branch,
		Worktree:     wtPath,
		Session:      sessionName,
		AgentSession: agentSession,
	}

	if err := a.store.InsertCard(card); err != nil {
		return nil, fmt.Errorf("insert card: %w", err)
	}

	a.controller.ExpectTurn(card.ID)
	a.controller.Start(card)
	a.onChanged()
	return card, nil
}

// MoveCard moves a card to the given column. A move never changes the
// card's status: the status tracks the agent's activity, not the move. A
// busy card cannot move forward; the check is enforced atomically by the
// store so a watcher flipping the status concurrently cannot slip past it.
// Moving forward sends the destination column's prompt to the card's agent;
// the move stays observable even when the prompt cannot be delivered.
func (a *App) MoveCard(cardID, colID string) error {
	card, err := a.store.GetCard(cardID)
	if err != nil {
		return err
	}

	dstIdx := a.ColumnIndex(colID)
	if dstIdx < 0 {
		return fmt.Errorf("unknown column %q", colID)
	}
	movedForward := dstIdx > a.ColumnIndex(card.Column)

	moved, err := a.store.MoveCard(cardID, colID, movedForward)
	if err != nil {
		return err
	}
	a.controller.Start(moved) // no-op if already watching
	a.onChanged()

	if movedForward {
		return a.controller.SendPrompt(moved, a.Columns()[dstIdx].Prompt)
	}
	return nil
}

// DeleteCard removes a card, kills its session, and cleans up its worktree.
func (a *App) DeleteCard(cardID string) error {
	card, err := a.store.GetCard(cardID)
	if err != nil {
		return err
	}
	// Remove from the store first: combined with the controller's relaunch
	// lock (Teardown), this guarantees no in-flight relaunch resurrects the
	// session after it is killed here.
	if err := a.store.DeleteCard(cardID); err != nil {
		return err
	}
	a.controller.Stop(cardID)
	a.controller.Teardown(card)
	removeWorktree(a.ctx, card.RepoPath, card.Worktree, card.Branch)
	// The agent is gone for good: drop its control-plane socket file too.
	_ = os.Remove(socketPath(card.AgentSession))
	a.onChanged()
	return nil
}

// Diff returns the card's full worktree diff against the upstream base.
func (a *App) Diff(cardID string) (string, error) {
	card, err := a.store.GetCard(cardID)
	if err != nil {
		return "", err
	}
	return worktreeDiff(a.ctx, card.Worktree)
}

// OpenEditor opens the card's worktree in the user's GUI editor: the
// command named by $BOARD_EDITOR, defaulting to VS Code's `code`. The
// editor is started detached and reaped in the background.
func (a *App) OpenEditor(cardID string) error {
	card, err := a.store.GetCard(cardID)
	if err != nil {
		return err
	}
	editor := cmp.Or(os.Getenv("BOARD_EDITOR"), "code")
	cmd := exec.CommandContext(a.ctx, editor, card.Worktree)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open editor (%s): %w", editor, err)
	}
	// Reap the editor when it exits so it does not linger as a zombie.
	go func() { _ = cmd.Wait() }()
	return nil
}

// ErrAgentStarting means the card's agent has not answered on its control
// plane yet, so there is no UI worth attaching to.
var ErrAgentStarting = errors.New("the agent is still starting")

// AttachCommand returns the command that attaches the caller's terminal to
// the card's agent session. It fails with [ErrAgentStarting] until the
// agent's control plane answers, so the user never lands on a bare launch
// command. Before attaching, the session's header row is refreshed with the
// card's current title and project.
func (a *App) AttachCommand(cardID string) (*exec.Cmd, error) {
	card, err := a.store.GetCard(cardID)
	if err != nil {
		return nil, err
	}
	if !a.controller.Ready(card) {
		return nil, ErrAgentStarting
	}
	socket, err := TmuxSocketPath()
	if err != nil {
		return nil, err
	}
	setSessionHeader(a.ctx, card.Session, card.Title, card.Project, card.Worktree)
	return exec.CommandContext(a.ctx, "tmux", "-S", socket, "attach", "-t", "="+card.Session), nil
}
