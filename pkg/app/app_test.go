package app

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/effort"
	"github.com/docker/docker-agent/pkg/hooks"
	"github.com/docker/docker-agent/pkg/hooks/builtins"
	"github.com/docker/docker-agent/pkg/runtime"
	"github.com/docker/docker-agent/pkg/session"
	"github.com/docker/docker-agent/pkg/sessiontitle"
	"github.com/docker/docker-agent/pkg/tools"
	skillstool "github.com/docker/docker-agent/pkg/tools/builtin/skills"
	mcptools "github.com/docker/docker-agent/pkg/tools/mcp"
	"github.com/docker/docker-agent/pkg/tui/messages"
)

// mockRuntime is a minimal mock for testing App without a real runtime.
// Snapshot operations are NOT modeled here: they are driven through a
// [builtins.SnapshotController] passed to the App via WithSnapshotController,
// so the mock runtime stays small and focused on the runtime.Runtime
// surface.
type mockRuntime struct {
	store session.Store
}

func (m *mockRuntime) CurrentAgentInfo(ctx context.Context) runtime.CurrentAgentInfo {
	return runtime.CurrentAgentInfo{}
}
func (m *mockRuntime) CurrentAgentName(context.Context) string              { return "mock" }
func (m *mockRuntime) SetCurrentAgent(_ context.Context, name string) error { return nil }
func (m *mockRuntime) CurrentAgentTools(ctx context.Context) ([]tools.Tool, error) {
	return nil, nil
}

func (m *mockRuntime) CurrentAgentToolsetStatuses() []tools.ToolsetStatus { return nil }
func (m *mockRuntime) RestartToolset(context.Context, string) error       { return nil }

func (m *mockRuntime) EmitStartupInfo(ctx context.Context, sess *session.Session, events runtime.EventSink) {
}
func (m *mockRuntime) EmitAgentInfo(context.Context, runtime.EventSink) {}
func (m *mockRuntime) ResetStartupInfo()                                {}
func (m *mockRuntime) RunStream(ctx context.Context, sess *session.Session) <-chan runtime.Event {
	ch := make(chan runtime.Event)
	close(ch)
	return ch
}

func (m *mockRuntime) Run(ctx context.Context, sess *session.Session) ([]session.Message, error) {
	return nil, nil
}
func (m *mockRuntime) Resume(ctx context.Context, req runtime.ResumeRequest) {}
func (m *mockRuntime) ResumeElicitation(ctx context.Context, action tools.ElicitationAction, content map[string]any) error {
	return nil
}
func (m *mockRuntime) SessionStore() session.Store { return m.store }
func (m *mockRuntime) Summarize(ctx context.Context, sess *session.Session, additionalPrompt string, events runtime.EventSink) {
}
func (m *mockRuntime) PermissionsInfo() *runtime.PermissionsInfo { return nil }
func (m *mockRuntime) CurrentAgentSkillsToolset() *skillstool.ToolSet {
	return nil
}

func (m *mockRuntime) RunSkillFork(context.Context, *session.Session, skillstool.RunSkillArgs, runtime.EventSink) (*tools.ToolCallResult, error) {
	return nil, nil
}

func (m *mockRuntime) CurrentMCPPrompts(context.Context) map[string]mcptools.PromptInfo {
	return make(map[string]mcptools.PromptInfo)
}

func (m *mockRuntime) ExecuteMCPPrompt(context.Context, string, map[string]string) (string, error) {
	return "", nil
}

func (m *mockRuntime) UpdateSessionTitle(_ context.Context, sess *session.Session, title string) error {
	sess.Title = title
	return nil
}
func (m *mockRuntime) TitleGenerator(context.Context) *sessiontitle.Generator    { return nil }
func (m *mockRuntime) Close() error                                              { return nil }
func (m *mockRuntime) Stop()                                                     {}
func (m *mockRuntime) Steer(_ context.Context, _ runtime.QueuedMessage) error    { return nil }
func (m *mockRuntime) FollowUp(_ context.Context, _ runtime.QueuedMessage) error { return nil }
func (m *mockRuntime) QueueStatus() runtime.QueueStatus                          { return runtime.QueueStatus{} }
func (m *mockRuntime) TogglePause(context.Context) (bool, error)                 { return false, nil }
func (m *mockRuntime) SetAgentModel(context.Context, string, string) error {
	return nil
}

func (m *mockRuntime) CycleAgentThinkingLevel(context.Context, string) (effort.Level, error) {
	return "", runtime.ErrUnsupported
}
func (m *mockRuntime) AvailableModels(context.Context) []runtime.ModelChoice { return nil }
func (m *mockRuntime) SupportsModelSwitching() bool                          { return false }
func (m *mockRuntime) OnToolsChanged(func(runtime.Event))                    {}

// Verify mockRuntime implements runtime.Runtime
var _ runtime.Runtime = (*mockRuntime)(nil)

// retryMockRuntime mimics the real run loop's startup event ordering: it
// re-emits a UserMessageEvent for the session's trailing message BEFORE
// StreamStarted (exactly what LocalRuntime.runStreamLoop does when
// SendUserMessage is set), then a StreamStopped. Used to verify App.Retry
// suppresses the pre-StreamStarted re-emission.
type retryMockRuntime struct {
	mockRuntime
}

func (m *retryMockRuntime) RunStream(_ context.Context, sess *session.Session) <-chan runtime.Event {
	ch := make(chan runtime.Event, 8)
	go func() {
		defer close(ch)
		// Re-emitted user message (pre-StreamStarted): must be suppressed.
		ch <- runtime.UserMessage("hello", sess.ID, nil, 0)
		ch <- runtime.StreamStarted(sess.ID, "mock")
		// A genuine mid-run user message (post-StreamStarted): must pass through.
		ch <- runtime.UserMessage("steered", sess.ID, nil, 1)
		ch <- runtime.StreamStopped(sess.ID, "mock", "normal")
	}()
	return ch
}

func TestApp_Retry_SuppressesReEmittedUserMessage(t *testing.T) {
	t.Parallel()

	events := make(chan tea.Msg, 16)
	app := &App{
		runtime: &retryMockRuntime{},
		session: session.New(),
		events:  events,
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	app.Retry(ctx, cancel)

	var userMessages []string
	var sawStreamStarted, sawStreamStopped bool
	deadline := time.After(2 * time.Second)
	for !sawStreamStopped {
		select {
		case ev := <-events:
			switch e := ev.(type) {
			case *runtime.UserMessageEvent:
				userMessages = append(userMessages, e.Message)
			case *runtime.StreamStartedEvent:
				sawStreamStarted = true
			case *runtime.StreamStoppedEvent:
				sawStreamStopped = true
			}
		case <-deadline:
			t.Fatal("timed out waiting for StreamStopped")
		}
	}

	assert.True(t, sawStreamStarted, "StreamStarted should be forwarded")
	// The pre-StreamStarted re-emission is dropped; the post-StreamStarted
	// (steered) user message is kept.
	assert.Equal(t, []string{"steered"}, userMessages,
		"only the post-StreamStarted user message should be forwarded")
}

// stubSnapshotController is a tiny SnapshotController used by the app
// tests to drive /undo without spinning up a real shadow-git
// repository. enabled gates SnapshotsEnabled(), and the (files, ok,
// err) tuple is returned verbatim from UndoLast / Reset so each test
// can assert the result-shaping logic in [snapshotResult].
type stubSnapshotController struct {
	enabled bool
	files   int
	ok      bool
	err     error
}

func (s *stubSnapshotController) Enabled() bool { return s.enabled }
func (s *stubSnapshotController) UndoLast(context.Context, string, string) (int, bool, error) {
	return s.files, s.ok, s.err
}

func (s *stubSnapshotController) List(string) []builtins.SnapshotInfo { return nil }
func (s *stubSnapshotController) Reset(context.Context, string, string, int) (int, bool, error) {
	return s.files, s.ok, s.err
}
func (s *stubSnapshotController) AutoInject(*hooks.Config) {}

var _ builtins.SnapshotController = (*stubSnapshotController)(nil)

func TestApp_NewSession_PreservesToolsApproved(t *testing.T) {
	t.Parallel()

	rt := &mockRuntime{}

	// Create initial session with tools approved
	initialSess := session.New(session.WithToolsApproved(true))
	require.True(t, initialSess.ToolsApproved, "Initial session should have tools approved")

	app := New(t.Context(), rt, initialSess)

	// Call NewSession - should preserve ToolsApproved
	app.NewSession()

	assert.True(t, app.Session().ToolsApproved, "NewSession should preserve ToolsApproved")
}

func TestApp_NewSession_PreservesHideToolResults(t *testing.T) {
	t.Parallel()

	rt := &mockRuntime{}

	// Create initial session with hide tool results
	initialSess := session.New(session.WithHideToolResults(true))
	require.True(t, initialSess.HideToolResults, "Initial session should have HideToolResults")

	app := New(t.Context(), rt, initialSess)

	// Call NewSession - should preserve HideToolResults
	app.NewSession()

	assert.True(t, app.Session().HideToolResults, "NewSession should preserve HideToolResults")
}

func TestApp_NewSession_WithNilSession(t *testing.T) {
	t.Parallel()

	rt := &mockRuntime{}

	// Create app with nil session (edge case)
	app := &App{
		ctx:     t.Context,
		runtime: rt,
		session: nil,
	}

	// Call NewSession - should not panic and create a new session with defaults
	app.NewSession()

	require.NotNil(t, app.Session(), "NewSession should create a new session")
	assert.False(t, app.Session().ToolsApproved, "NewSession with nil should use default ToolsApproved=false")
}

func TestApp_UpdateSessionTitle(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	t.Run("updates title in session", func(t *testing.T) {
		t.Parallel()

		rt := &mockRuntime{}
		sess := session.New()
		events := make(chan tea.Msg, 16)
		app := &App{
			runtime: rt,
			session: sess,
			events:  events,
		}

		err := app.UpdateSessionTitle(ctx, "New Title")
		require.NoError(t, err)

		assert.Equal(t, "New Title", sess.Title)

		// Check that an event was emitted
		select {
		case event := <-events:
			titleEvent, ok := event.(*runtime.SessionTitleEvent)
			require.True(t, ok, "should emit SessionTitleEvent")
			assert.Equal(t, "New Title", titleEvent.Title)
		default:
			t.Fatal("expected SessionTitleEvent to be emitted")
		}
	})

	t.Run("returns error when no session", func(t *testing.T) {
		t.Parallel()

		rt := &mockRuntime{}
		app := &App{
			runtime: rt,
			session: nil,
		}

		err := app.UpdateSessionTitle(ctx, "New Title")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no active session")
	})

	t.Run("returns ErrTitleGenerating when generation in progress", func(t *testing.T) {
		t.Parallel()

		rt := &mockRuntime{}
		sess := session.New()
		events := make(chan tea.Msg, 16)
		app := &App{
			runtime: rt,
			session: sess,
			events:  events,
		}

		// Simulate title generation in progress
		app.titleGenerating.Store(true)

		err := app.UpdateSessionTitle(ctx, "New Title")
		require.ErrorIs(t, err, ErrTitleGenerating)

		// Title should not be updated
		assert.Empty(t, sess.Title)
	})
}

func TestApp_ResolveSkillCommand_NoLocalRuntime(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	rt := &mockRuntime{}
	sess := session.New()
	app := New(t.Context(), rt, sess)

	// mockRuntime is not a LocalRuntime, so no skills should be returned
	resolved, err := app.ResolveSkillCommand(ctx, "/some-skill")
	require.NoError(t, err)
	assert.Empty(t, resolved)
}

func TestApp_ResolveSkillCommand_NotSlashCommand(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	rt := &mockRuntime{}
	sess := session.New()
	app := New(t.Context(), rt, sess)

	resolved, err := app.ResolveSkillCommand(ctx, "not a slash command")
	require.NoError(t, err)
	assert.Empty(t, resolved)
}

func TestApp_UndoLastSnapshot(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	app := New(t.Context(), &mockRuntime{}, session.New(),
		WithSnapshotController(&stubSnapshotController{enabled: true, files: 2, ok: true}),
	)
	result, err := app.UndoLastSnapshot(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, result.RestoredFiles)
}

func TestApp_UndoLastSnapshot_NoSnapshot(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	app := New(t.Context(), &mockRuntime{}, session.New(),
		WithSnapshotController(&stubSnapshotController{enabled: true}),
	)
	_, err := app.UndoLastSnapshot(ctx)
	assert.ErrorIs(t, err, ErrNothingToUndo)
}

func TestApp_UndoLastSnapshot_NoController(t *testing.T) {
	t.Parallel()

	// Without a SnapshotController the App reports nothing to undo,
	// so the same UI affordance can light up regardless of which
	// runtime the embedder paired the App with.
	ctx := t.Context()
	app := New(t.Context(), &mockRuntime{}, session.New())
	_, err := app.UndoLastSnapshot(ctx)
	require.ErrorIs(t, err, ErrNothingToUndo)
	assert.False(t, app.SnapshotsEnabled())
}

func TestApp_SnapshotsEnabled_DoesNotRequireSession(t *testing.T) {
	t.Parallel()

	// SnapshotsEnabled answers a controller-capability question; it
	// must not silently return false just because no session is attached.
	app := &App{
		ctx:                t.Context,
		runtime:            &mockRuntime{},
		session:            nil,
		snapshotController: &stubSnapshotController{enabled: true},
	}
	assert.True(t, app.SnapshotsEnabled())
}

func TestApp_SubscribeWith_FanOutToMultipleSubscribers(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	rt := &mockRuntime{}
	app := New(t.Context(), rt, session.New())

	recv := func() (chan tea.Msg, context.CancelFunc) {
		subCtx, subCancel := context.WithCancel(ctx)
		ch := make(chan tea.Msg, 16)
		go app.SubscribeWith(subCtx, func(m tea.Msg) { ch <- m })
		return ch, subCancel
	}

	a, cancelA := recv()
	b, cancelB := recv()
	defer cancelA()
	defer cancelB()

	// Wait until both subscribers are registered before publishing.
	require.Eventually(t, func() bool {
		app.subsMu.Lock()
		defer app.subsMu.Unlock()
		return len(app.subs) == 2
	}, time.Second, 5*time.Millisecond)

	app.events <- runtime.SessionTitle("sess", "hello")

	for _, ch := range []chan tea.Msg{a, b} {
		select {
		case msg := <-ch:
			ev, ok := msg.(*runtime.SessionTitleEvent)
			require.True(t, ok)
			assert.Equal(t, "hello", ev.Title)
		case <-time.After(time.Second):
			t.Fatal("subscriber did not receive event")
		}
	}
}

func TestApp_RegenerateSessionTitle(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	t.Run("returns error when no session", func(t *testing.T) {
		t.Parallel()

		rt := &mockRuntime{}
		app := &App{
			runtime: rt,
			session: nil,
		}

		err := app.RegenerateSessionTitle(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no active session")
	})

	t.Run("returns error when no title generator is available", func(t *testing.T) {
		t.Parallel()

		rt := &mockRuntime{}
		sess := session.New()
		events := make(chan tea.Msg, 16)
		app := &App{
			runtime: rt,
			session: sess,
			events:  events,
			// titleGen is nil - no title generator available
		}

		err := app.RegenerateSessionTitle(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "title regeneration not available")
	})

	t.Run("returns ErrTitleGenerating when already generating", func(t *testing.T) {
		t.Parallel()

		rt := &mockRuntime{}
		sess := session.New()
		events := make(chan tea.Msg, 16)
		app := &App{
			runtime: rt,
			session: sess,
			events:  events,
		}

		// Simulate title generation already in progress
		app.titleGenerating.Store(true)

		err := app.RegenerateSessionTitle(ctx)
		require.ErrorIs(t, err, ErrTitleGenerating)
	})
}

// TestApp_InjectUserMessage verifies a follow-up injected by an external
// driver is published on the event bus as a SendMsg — the same message the
// TUI produces when the user submits input — so it flows through the normal
// run path (queueing, title generation, event streaming).
func TestApp_InjectUserMessage(t *testing.T) {
	t.Parallel()

	events := make(chan tea.Msg, 4)
	app := &App{
		ctx:     t.Context,
		runtime: &mockRuntime{},
		session: session.New(),
		events:  events,
	}

	app.InjectUserMessage(t.Context(), "do the thing")

	select {
	case msg := <-events:
		sendMsg, ok := msg.(messages.SendMsg)
		require.True(t, ok, "should emit a SendMsg, got %T", msg)
		assert.Equal(t, "do the thing", sendMsg.Content)
	default:
		t.Fatal("expected a SendMsg to be emitted")
	}
}
