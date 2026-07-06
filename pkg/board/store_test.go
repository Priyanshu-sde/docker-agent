package board

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "cards.json"))
	require.NoError(t, err)
	return s
}

func TestStoreRoundTrip(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "cards.json")
	s, err := OpenStore(path)
	require.NoError(t, err)

	card := &Card{ID: "c1", Title: "Task", Column: "dev", Status: StatusStarting}
	require.NoError(t, s.InsertCard(card))

	// Reopen from disk.
	s2, err := OpenStore(path)
	require.NoError(t, err)
	got, err := s2.GetCard("c1")
	require.NoError(t, err)
	assert.Equal(t, card, got)
}

func TestStoreReturnsCopies(t *testing.T) {
	t.Parallel()

	s := testStore(t)
	require.NoError(t, s.InsertCard(&Card{ID: "c1", Title: "Task", Column: "dev"}))

	got, err := s.GetCard("c1")
	require.NoError(t, err)
	got.Title = "mutated"

	fresh, err := s.GetCard("c1")
	require.NoError(t, err)
	assert.Equal(t, "Task", fresh.Title)
}

func TestStoreUpdateFields(t *testing.T) {
	t.Parallel()

	s := testStore(t)
	require.NoError(t, s.InsertCard(&Card{ID: "c1", Title: "Task", Column: "dev", Status: StatusStarting}))

	changed, err := s.UpdateCardStatus("c1", StatusRunning)
	require.NoError(t, err)
	assert.True(t, changed)

	// No-op update reports unchanged.
	changed, err = s.UpdateCardStatus("c1", StatusRunning)
	require.NoError(t, err)
	assert.False(t, changed)

	changed, err = s.UpdateCardTitle("c1", "Renamed")
	require.NoError(t, err)
	assert.True(t, changed)

	got, err := s.GetCard("c1")
	require.NoError(t, err)
	assert.Equal(t, StatusRunning, got.Status)
	assert.Equal(t, "Renamed", got.Title)

	_, err = s.UpdateCardStatus("missing", StatusRunning)
	assert.ErrorIs(t, err, ErrCardNotFound)
}

func TestStoreMoveCard(t *testing.T) {
	t.Parallel()

	s := testStore(t)
	require.NoError(t, s.InsertCard(&Card{ID: "c1", Column: "dev", Status: StatusWaiting}))
	require.NoError(t, s.InsertCard(&Card{ID: "c2", Column: "dev", Status: StatusRunning}))

	// Moving re-inserts the card at the end of the board order.
	moved, err := s.MoveCard("c1", "review", true)
	require.NoError(t, err)
	assert.Equal(t, "review", moved.Column)
	assert.Equal(t, StatusWaiting, moved.Status)

	cards := s.ListCards()
	require.Len(t, cards, 2)
	assert.Equal(t, "c2", cards[0].ID)
	assert.Equal(t, "c1", cards[1].ID)

	// A busy card cannot move forward.
	_, err = s.MoveCard("c2", "review", true)
	require.ErrorIs(t, err, ErrCardBusy)

	// But it can move backward (requireIdle false).
	moved, err = s.MoveCard("c2", "done", false)
	require.NoError(t, err)
	assert.Equal(t, "done", moved.Column)
}

func TestStoreDeleteCard(t *testing.T) {
	t.Parallel()

	s := testStore(t)
	require.NoError(t, s.InsertCard(&Card{ID: "c1", Column: "dev"}))

	require.NoError(t, s.DeleteCard("c1"))
	_, err := s.GetCard("c1")
	require.ErrorIs(t, err, ErrCardNotFound)

	// Deleting a missing card is a no-op.
	require.NoError(t, s.DeleteCard("c1"))
}
