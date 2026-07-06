package board

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestShQuote(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "'plain'", shQuote("plain"))
	assert.Equal(t, `'it'\''s'`, shQuote("it's"))
	assert.Equal(t, "'two words'", shQuote("two words"))
}

func TestAgentCommand(t *testing.T) {
	t.Parallel()

	// First run: creates the worktree from the base.
	cmd := agentCommand("coder", "sess1", "/tmp/a.sock", "board-abc", "origin/main", "do the thing")
	assert.Contains(t, cmd, " run 'coder' --yolo --session 'sess1' --listen 'unix:///tmp/a.sock'")
	assert.Contains(t, cmd, "--worktree='board-abc' --worktree-base 'origin/main'")
	assert.True(t, strings.HasSuffix(cmd, " 'do the thing'"))

	// Resume: no worktree flags, no prompt.
	cmd = agentCommand("coder", "sess1", "/tmp/a.sock", "", "", "")
	assert.NotContains(t, cmd, "--worktree")
	assert.True(t, strings.HasSuffix(cmd, "--listen 'unix:///tmp/a.sock'"))
}

func TestTmuxFormatEscape(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "plain title", tmuxFormatEscape("plain title"))
	// '#' introduces tmux format expansion: doubled to stay literal.
	assert.Equal(t, "fix ##123 · ##{pane_pid}", tmuxFormatEscape("fix #123 · #{pane_pid}"))
	// Control characters are dropped.
	assert.Equal(t, "ab", tmuxFormatEscape("a\x1b\nb\x7f"))
}
