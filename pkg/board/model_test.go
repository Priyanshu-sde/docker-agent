package board

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/docker/docker-agent/pkg/userconfig"
)

func TestPlaceholderTitle(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "Fix the bug", placeholderTitle("Fix the bug"))
	assert.Equal(t, "First line", placeholderTitle("First line\nsecond line"))
	assert.Equal(t, "Trimmed", placeholderTitle("  Trimmed  "))

	long := placeholderTitle(strings.Repeat("word ", 20))
	assert.LessOrEqual(t, len([]rune(long)), 41)
	assert.True(t, strings.HasSuffix(long, "…"))

	// A long word without boundaries is cut mid-word.
	assert.True(t, strings.HasSuffix(placeholderTitle(strings.Repeat("a", 50)), "…"))
}

func TestColumnsFromConfig(t *testing.T) {
	t.Parallel()

	assert.Equal(t, DefaultColumns, ColumnsFromConfig(nil))

	cols := ColumnsFromConfig([]userconfig.BoardColumn{
		{ID: "todo", Name: "Todo", Emoji: "📝", Prompt: "do it"},
	})
	assert.Equal(t, []Column{{ID: "todo", Name: "Todo", Emoji: "📝", Prompt: "do it"}}, cols)
}

func TestCardStatusBusy(t *testing.T) {
	t.Parallel()

	assert.True(t, StatusStarting.Busy())
	assert.True(t, StatusRunning.Busy())
	assert.False(t, StatusWaiting.Busy())
	assert.False(t, StatusPaused.Busy())
	assert.False(t, StatusError.Busy())
}

func TestNewWorktreeName(t *testing.T) {
	t.Parallel()

	name := newWorktreeName()
	assert.True(t, strings.HasPrefix(name, "board-"))
	assert.NotContains(t, name, "/")
	assert.NotEqual(t, name, newWorktreeName())
}
