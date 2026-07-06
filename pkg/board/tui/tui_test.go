package tui

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/board"
)

func TestGroupCardsFallsBackToFirstColumn(t *testing.T) {
	t.Parallel()

	columns := []board.Column{{ID: "dev"}, {ID: "done"}}
	cards := []*board.Card{
		{ID: "a", Column: "dev"},
		{ID: "b", Column: "removed"}, // column dropped from the config
		{ID: "c", Column: "done"},
	}

	grouped := groupCards(columns, cards)
	assert.Len(t, grouped["dev"], 2, "orphaned card should land in the first column")
	assert.Len(t, grouped["done"], 1)
}

func TestColumnWindowFollowsSelection(t *testing.T) {
	t.Parallel()

	m := &model{
		width:  50, // fits 2 columns of minColumnWidth
		height: 40,
		columns: []board.Column{
			{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"},
		},
		cards:  map[string][]*board.Card{},
		scroll: map[string]int{},
	}

	offset, count := m.columnWindow()
	assert.Equal(t, 0, offset)
	assert.Equal(t, 2, count)

	// Selecting the last column slides the window right…
	m.selCol = 3
	offset, _ = m.columnWindow()
	assert.Equal(t, 2, offset)

	// …and hit-testing accounts for the offset: x=0 is now column c.
	col, ok := m.columnAt(0, boardTop+1)
	require.True(t, ok)
	assert.Equal(t, 2, col)

	// Selecting the first column slides back.
	m.selCol = 0
	offset, _ = m.columnWindow()
	assert.Equal(t, 0, offset)

	// A wide terminal shows everything.
	m.width = 200
	offset, count = m.columnWindow()
	assert.Equal(t, 0, offset)
	assert.Equal(t, 4, count)
}

func TestCardAtMirrorsLayout(t *testing.T) {
	t.Parallel()

	columns := []board.Column{{ID: "dev"}, {ID: "done"}}
	m := &model{
		width:   120,
		height:  40,
		columns: columns,
		cards: map[string][]*board.Card{
			"dev":  {{ID: "a"}, {ID: "b"}},
			"done": {{ID: "c"}},
		},
		scroll: map[string]int{},
	}
	// colWidth = (120-1)/2 = 59; cards start at row boardTop+3 = 5.

	col, row, ok := m.cardAt(5, 5) // first card, first column
	require.True(t, ok)
	assert.Equal(t, 0, col)
	assert.Equal(t, 0, row)

	col, row, ok = m.cardAt(5, 5+cardHeight) // second card slot, first column
	require.True(t, ok)
	assert.Equal(t, 0, col)
	assert.Equal(t, 1, row)

	col, row, ok = m.cardAt(65, 5) // first card, second column
	require.True(t, ok)
	assert.Equal(t, 1, col)
	assert.Equal(t, 0, row)

	_, _, ok = m.cardAt(65, 5+cardHeight) // slot exists but column has 1 card
	assert.False(t, ok)

	_, _, ok = m.cardAt(5, 4) // header/rule rows
	assert.False(t, ok)
	_, _, ok = m.cardAt(59, 5) // gap between columns
	assert.False(t, ok)
}

func TestPlusButtonAt(t *testing.T) {
	t.Parallel()

	m := &model{
		width:   120,
		height:  40,
		columns: []board.Column{{ID: "dev"}, {ID: "done"}},
		cards:   map[string][]*board.Card{},
		scroll:  map[string]int{},
	}
	// colWidth = (120-1)/2 = 59; the header ends with "+ (0) ": the + sits
	// at x = colWidth-3-4 = 52 on the header row (boardTop+1 = 3).
	assert.True(t, m.plusButtonAt(52, 3))
	assert.True(t, m.plusButtonAt(51, 3))     // one cell of slack
	assert.False(t, m.plusButtonAt(52, 4))    // rule row
	assert.False(t, m.plusButtonAt(30, 3))    // middle of the title row
	assert.False(t, m.plusButtonAt(52+59, 3)) // second column has no +

	// The + belongs to the first column: hidden when it is scrolled out.
	m.width, m.selCol = 50, 1 // fits 2 of 4 columns
	m.columns = []board.Column{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}}
	m.selCol = 3
	assert.False(t, m.plusButtonAt(20, 3))
}

func TestDiffDialogClampsRestoredOffset(t *testing.T) {
	t.Parallel()

	// A restored offset can exceed the reloaded diff's length; rendering
	// must clamp it once the real viewport dimensions are known.
	d := newDiffDialog("card", "title", "+a\n+b\n+c", 100)
	_ = d.View(120, 30)
	assert.LessOrEqual(t, d.view.YOffset(), d.view.TotalLineCount())
	_ = d.View(120, 30) // stable on re-render
	assert.GreaterOrEqual(t, d.view.YOffset(), 0)
}

func TestPlaceOverlayCompositesOverBase(t *testing.T) {
	t.Parallel()

	base := strings.TrimSuffix(strings.Repeat("ABCDEFGH\n", 8), "\n")
	out := placeOverlay(base, "XX\nXX", 8, 8)

	// The dialog is centered…
	assert.Contains(t, out, "ABCXXFGH")
	// …and the board stays visible around it.
	assert.Contains(t, out, "ABCDEFGH")
}

func TestSanitize(t *testing.T) {
	t.Parallel()

	// ANSI/OSC sequences and control characters are stripped.
	assert.Equal(t, "title", sanitize("\x1b[31mti\x1b]2;pwned\x07tle\x00"))
	// Newlines survive, tabs become spaces.
	assert.Equal(t, "a\n    b", sanitize("a\n\tb"))
}

func TestSplitTitle(t *testing.T) {
	t.Parallel()

	l1, l2 := splitTitle("Short", 20)
	assert.Equal(t, "Short", l1)
	assert.Empty(t, l2)

	l1, l2 = splitTitle("A somewhat longer card title", 10)
	assert.Equal(t, "A somewhat", l1)
	assert.NotEmpty(t, l2)
}

func TestColorizeDiffKeepsLineCount(t *testing.T) {
	t.Parallel()

	diff := "diff --git a/f b/f\n--- a/f\n+++ b/f\n@@ -1 +1 @@\n-old\n+new\n context"
	colored := colorizeDiff(diff)
	assert.Len(t, strings.Split(colored, "\n"), len(strings.Split(diff, "\n")))
	assert.Contains(t, colored, "old")
	assert.Contains(t, colored, "new")
}

func TestDialogWidth(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 80, dialogWidth(80, 200))
	assert.Equal(t, 56, dialogWidth(80, 60)) // clamped to terminal
	assert.Equal(t, 24, dialogWidth(80, 10)) // floor
}

func TestPlural(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "1 card", plural(1, "card"))
	assert.Equal(t, "2 cards", plural(2, "card"))
	assert.Equal(t, "0 projects", plural(0, "project"))
}
