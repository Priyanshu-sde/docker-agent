package board

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeSessions struct{}

func (fakeSessions) NewSession(_, _, _, _, _, _, _, _ string) error { return nil }
func (fakeSessions) KillSession(string) error                       { return nil }
func (fakeSessions) Alive(string) (bool, error)                     { return true, nil }

// fakeClient replays a scripted event stream, then blocks like a live
// connection until the watcher is cancelled.
type fakeClient struct {
	snap   snapshot
	events []event
}

func (f *fakeClient) Snapshot(context.Context) (snapshot, error) { return f.snap, nil }

func (f *fakeClient) Followup(context.Context, string, string) error { return nil }

func (f *fakeClient) StreamEvents(ctx context.Context, _ uint64, onEvent func(event) bool) error {
	for _, ev := range f.events {
		if !onEvent(ev) {
			return nil
		}
	}
	<-ctx.Done()
	return ctx.Err()
}

// watchCard spins up a controller whose client replays the given events for
// a fresh card, and returns the store to observe the mirrored state.
func watchCard(t *testing.T, snap snapshot, events []event) (*Store, *controller) {
	t.Helper()
	store := testStore(t)
	require.NoError(t, store.InsertCard(&Card{ID: "c1", Title: "Task", Column: "dev", Status: StatusStarting}))

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	c := newController(ctx, store, fakeSessions{}, func() {})
	c.clientFor = func(_, _ string) sessionClient {
		return &fakeClient{snap: snap, events: events}
	}
	card, err := store.GetCard("c1")
	require.NoError(t, err)
	c.Start(card)
	t.Cleanup(func() { c.Stop("c1") })
	return store, c
}

func waitForStatus(t *testing.T, store *Store, want CardStatus) {
	t.Helper()
	assert.Eventually(t, func() bool {
		card, err := store.GetCard("c1")
		return err == nil && card.Status == want
	}, 3*time.Second, 10*time.Millisecond, "expected status %s", want)
}

func TestControllerRunningThenWaiting(t *testing.T) {
	t.Parallel()

	store, _ := watchCard(t, snapshot{}, []event{
		{Type: eventUserMessage},
		{Type: eventStreamStarted},
		{Type: eventStreamStopped, Reason: reasonNormal},
	})
	waitForStatus(t, store, StatusWaiting)
}

func TestControllerExpectedTurnSkipsReadyFlash(t *testing.T) {
	t.Parallel()

	// A fresh card launches with an initial prompt: the control plane
	// answers before the first event, but the card must not flash "ready"
	// before its first turn (starting → running → ready).
	store := testStore(t)
	require.NoError(t, store.InsertCard(&Card{ID: "c1", Title: "Task", Column: "dev", Status: StatusStarting}))

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	c := newController(ctx, store, fakeSessions{}, func() {})
	c.clientFor = func(_, _ string) sessionClient {
		return &fakeClient{}
	}
	card, err := store.GetCard("c1")
	require.NoError(t, err)
	c.ExpectTurn("c1")
	c.Start(card)
	t.Cleanup(func() { c.Stop("c1") })

	// With no events yet, the card stays "starting" instead of "ready".
	assert.Never(t, func() bool {
		card, err := store.GetCard("c1")
		return err == nil && card.Status != StatusStarting
	}, 300*time.Millisecond, 10*time.Millisecond)
}

func TestControllerExpectedTurnRunsThenWaits(t *testing.T) {
	t.Parallel()

	store := testStore(t)
	require.NoError(t, store.InsertCard(&Card{ID: "c1", Title: "Task", Column: "dev", Status: StatusStarting}))

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	c := newController(ctx, store, fakeSessions{}, func() {})
	c.clientFor = func(_, _ string) sessionClient {
		return &fakeClient{events: []event{
			{Type: eventUserMessage},
			{Type: eventStreamStarted},
			{Type: eventStreamStopped, Reason: reasonNormal},
		}}
	}
	card, err := store.GetCard("c1")
	require.NoError(t, err)
	c.ExpectTurn("c1")
	c.Start(card)
	t.Cleanup(func() { c.Stop("c1") })

	// The first turn runs and completes: only then is the card ready.
	waitForStatus(t, store, StatusWaiting)
	assert.False(t, c.turnExpected("c1"), "expectation should be cleared by the first turn")
}

func TestControllerStaysRunningWithNestedStreams(t *testing.T) {
	t.Parallel()

	store, _ := watchCard(t, snapshot{}, []event{
		{Type: eventStreamStarted},
		{Type: eventStreamStarted}, // sub-agent
		{Type: eventStreamStopped, Reason: reasonNormal},
	})
	// The outer stream is still open: the card stays running.
	waitForStatus(t, store, StatusRunning)
}

func TestControllerErrorIsSticky(t *testing.T) {
	t.Parallel()

	store, _ := watchCard(t, snapshot{}, []event{
		{Type: eventStreamStarted},
		{Type: eventError},
		{Type: eventStreamStopped, Reason: "error"},
	})
	waitForStatus(t, store, StatusError)
}

func TestControllerNormalStopClearsSubAgentError(t *testing.T) {
	t.Parallel()

	// A sub-agent error the parent recovered from: the outermost stop's
	// "normal" reason is authoritative.
	store, _ := watchCard(t, snapshot{}, []event{
		{Type: eventStreamStarted},
		{Type: eventError},
		{Type: eventStreamStopped, Reason: reasonNormal},
	})
	waitForStatus(t, store, StatusWaiting)
}

func TestControllerPause(t *testing.T) {
	t.Parallel()

	store, _ := watchCard(t, snapshot{}, []event{
		{Type: eventStreamStarted},
		{Type: eventRuntimePaused},
	})
	waitForStatus(t, store, StatusPaused)
}

func TestControllerReplayAppliesFinalStatusOnly(t *testing.T) {
	t.Parallel()

	// Replayed history contains a long-resolved error: only the state at the
	// snapshot's seq lands in the store.
	store, _ := watchCard(t, snapshot{LastEventSeq: 3}, []event{
		{Type: eventStreamStarted, Seq: 1},
		{Type: eventError, Seq: 2},
		{Type: eventStreamStopped, Reason: reasonNormal, Seq: 3},
	})
	waitForStatus(t, store, StatusWaiting)
}

func TestControllerTitleFromSnapshot(t *testing.T) {
	t.Parallel()

	store, _ := watchCard(t, snapshot{Title: "Real title"}, nil)
	assert.Eventually(t, func() bool {
		card, err := store.GetCard("c1")
		return err == nil && card.Title == "Real title"
	}, 3*time.Second, 10*time.Millisecond)
}

// recordingSessions counts session creations so tests can assert whether a
// relaunch really happened.
type recordingSessions struct {
	alive       bool
	newSessions int
}

func (r *recordingSessions) NewSession(_, _, _, _, _, _, _, _ string) error {
	r.newSessions++
	return nil
}
func (r *recordingSessions) KillSession(string) error   { return nil }
func (r *recordingSessions) Alive(string) (bool, error) { return r.alive, nil }

func TestRelaunchAbortsForDeletedCard(t *testing.T) {
	t.Parallel()

	sessions := &recordingSessions{}
	c := newController(t.Context(), testStore(t), sessions, func() {})

	err := c.relaunch(&Card{ID: "gone", Session: "s"}, "prompt")
	require.ErrorIs(t, err, ErrCardNotFound)
	assert.Zero(t, sessions.newSessions)
}

func TestRelaunchSkipsResurrectedSession(t *testing.T) {
	t.Parallel()

	store := testStore(t)
	require.NoError(t, store.InsertCard(&Card{ID: "c1", Session: "s"}))
	sessions := &recordingSessions{alive: true}
	c := newController(t.Context(), store, sessions, func() {})
	card, err := store.GetCard("c1")
	require.NoError(t, err)

	// A plain resume backs off when the session is already alive again…
	require.NoError(t, c.relaunch(card, ""))
	assert.Zero(t, sessions.newSessions)

	// …but a prompt-bearing relaunch proceeds: its prompt must be delivered.
	require.NoError(t, c.relaunch(card, "do it"))
	assert.Equal(t, 1, sessions.newSessions)
}

func TestControllerStopWaits(t *testing.T) {
	t.Parallel()

	store, c := watchCard(t, snapshot{}, []event{{Type: eventStreamStarted}})
	waitForStatus(t, store, StatusRunning)

	c.Stop("c1")
	// Stopping twice (or a never-watched card) is safe.
	c.Stop("c1")
	c.Stop("unknown")
}
