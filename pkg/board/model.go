// Package board implements a Kanban board for orchestrating docker agents.
// Each card owns an agent running in a tmux session on an isolated git
// worktree; the board observes and drives the agent through the control
// plane its run exposes with --listen. Columns form a pipeline: moving a
// card forward sends the destination column's prompt to its agent.
package board

import (
	"crypto/rand"
	"encoding/hex"
	"path/filepath"
	"strings"

	"github.com/docker/docker-agent/pkg/paths"
	"github.com/docker/docker-agent/pkg/userconfig"
)

// Column is one kanban column with the prompt sent to a card's agent when
// the card moves forward into it.
type Column struct {
	ID     string
	Name   string
	Emoji  string
	Prompt string
}

// DefaultColumns is the pipeline used when the user config defines none.
var DefaultColumns = []Column{
	{ID: "dev", Name: "Dev", Emoji: "🔨"},
	{ID: "review", Name: "Review", Emoji: "🔍", Prompt: "Review the local changes. Look for bugs, security issues, and code quality problems. Fix any issues you find."},
	{ID: "push", Name: "Push", Emoji: "🚀", Prompt: "Start by committing any remaining uncommitted files. Then rebase on top of the upstream default branch and fix any test failures and linter issues. Finally, squash all commits on this branch into a single commit with a clear and concise commit message. Push the branch to your fork (or the appropriate remote). Then use gh to open a pull request."},
	{ID: "done", Name: "Done", Emoji: "✅"},
}

// ColumnsFromConfig maps user-configured columns to board columns, falling
// back to [DefaultColumns] when none are configured.
func ColumnsFromConfig(cols []userconfig.BoardColumn) []Column {
	if len(cols) == 0 {
		return DefaultColumns
	}
	out := make([]Column, 0, len(cols))
	for _, c := range cols {
		out = append(out, Column{ID: c.ID, Name: c.Name, Emoji: c.Emoji, Prompt: c.Prompt})
	}
	return out
}

// CardStatus tracks what a card's agent is doing.
type CardStatus string

const (
	// StatusStarting marks a card whose agent is launching but has not yet
	// answered on its control plane. The watcher replaces it with a real
	// status as soon as the agent emits events.
	StatusStarting CardStatus = "starting"
	StatusRunning  CardStatus = "running"
	StatusWaiting  CardStatus = "waiting"
	// StatusPaused marks a card whose turn is blocked on /pause. It lasts
	// until the runtime emits events again (resume) or the turn ends.
	StatusPaused CardStatus = "paused"
	// StatusError marks a card whose last turn failed. It is sticky: the
	// watcher keeps it until the next turn starts.
	StatusError CardStatus = "error"
)

// Busy reports whether the card's agent cannot accept a prompt right now: it
// is either still starting or in the middle of a turn.
func (s CardStatus) Busy() bool {
	return s == StatusStarting || s == StatusRunning
}

// Card is one task on the board.
type Card struct {
	ID       string     `json:"id"`
	Title    string     `json:"title"`
	Column   string     `json:"column"`
	Status   CardStatus `json:"status"`
	Project  string     `json:"project"`
	Agent    string     `json:"agent"`
	RepoPath string     `json:"repoPath"`
	Branch   string     `json:"branch"`
	Worktree string     `json:"worktree"`
	Session  string     `json:"session"`
	// AgentSession is the docker-agent conversation ID the card owns. It is
	// passed to `docker agent run --session` on every launch, so a session
	// recreated after the agent (or tmux) dies resumes the same conversation
	// instead of starting over.
	AgentSession string `json:"agentSession"`
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// newWorktreeName returns a unique worktree name for a card. docker-agent
// derives the worktree directory (<data>/worktrees/<name>) and branch
// (worktree-<name>) from it, so the name must be a single path segment.
func newWorktreeName() string {
	return "board-" + newID()
}

// newSessionName returns a unique tmux session name for a card.
func newSessionName() string {
	return "board-" + newID()[:8]
}

// socketPath returns the unix socket a card's agent control plane listens
// on. It is derived from the (unique) docker-agent session id, so it is
// stable across board restarts and needs no extra storage. Kept short to
// stay under the ~104-byte unix sun_path limit.
func socketPath(agentSession string) string {
	return filepath.Join(paths.GetDataDir(), "run", "board-"+agentSession+".sock")
}

// worktreeDir returns the directory docker-agent creates for a worktree of
// the given name, mirroring its --worktree convention so the board can
// locate the worktree for diffs and cleanup.
func worktreeDir(name string) string {
	return filepath.Join(paths.GetDataDir(), "worktrees", name)
}

// worktreeBranch returns the branch docker-agent checks out for a worktree
// of the given name.
func worktreeBranch(name string) string {
	return "worktree-" + name
}

// placeholderTitle is the short temporary title shown until the agent emits
// its session_title event. It is the prompt's first line, trimmed and cut to
// a few words so a long prompt never becomes an unwieldy card title.
func placeholderTitle(prompt string) string {
	title := prompt
	if i := strings.IndexByte(title, '\n'); i >= 0 {
		title = title[:i]
	}
	title = strings.TrimSpace(title)

	const maxLen = 40
	runes := []rune(title)
	if len(runes) <= maxLen {
		return title
	}

	cut := string(runes[:maxLen])
	// Prefer a word boundary so the title does not end mid-word.
	if i := strings.LastIndexByte(cut, ' '); i > 0 {
		cut = cut[:i]
	}
	return strings.TrimSpace(cut) + "…"
}
