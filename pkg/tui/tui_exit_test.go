package tui

import (
	"bytes"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/tui/components/completion"
	"github.com/docker/docker-agent/pkg/tui/components/editor"
	"github.com/docker/docker-agent/pkg/tui/components/notification"
	"github.com/docker/docker-agent/pkg/tui/core/layout"
	"github.com/docker/docker-agent/pkg/tui/dialog"
	"github.com/docker/docker-agent/pkg/tui/messages"
	"github.com/docker/docker-agent/pkg/tui/page/chat"
	"github.com/docker/docker-agent/pkg/tui/service"
)

// mockChatPage implements chat.Page for testing.
type mockChatPage struct{}

func (m *mockChatPage) Init() tea.Cmd                            { return nil }
func (m *mockChatPage) Update(tea.Msg) (layout.Model, tea.Cmd)   { return m, nil }
func (m *mockChatPage) View() string                             { return "" }
func (m *mockChatPage) SetSize(int, int) tea.Cmd                 { return nil }
func (m *mockChatPage) CompactSession(string) tea.Cmd            { return nil }
func (m *mockChatPage) SetSessionStarred(bool)                   {}
func (m *mockChatPage) SetTitleRegenerating(bool) tea.Cmd        { return nil }
func (m *mockChatPage) ScrollToBottom() tea.Cmd                  { return nil }
func (m *mockChatPage) IsWorking() bool                          { return false }
func (m *mockChatPage) IsInlineEditing() bool                    { return false }
func (m *mockChatPage) QueueLength() int                         { return 0 }
func (m *mockChatPage) FocusMessages() tea.Cmd                   { return nil }
func (m *mockChatPage) FocusMessageAt(int, int) tea.Cmd          { return nil }
func (m *mockChatPage) BlurMessages()                            {}
func (m *mockChatPage) GetSidebarSettings() chat.SidebarSettings { return chat.SidebarSettings{} }
func (m *mockChatPage) SetSidebarSettings(chat.SidebarSettings)  {}
func (m *mockChatPage) Bindings() []key.Binding                  { return nil }
func (m *mockChatPage) Help() help.KeyMap                        { return nil }

// mockEditor implements editor.Editor for testing.
type mockEditor struct {
	cleanupCalled bool
}

func (m *mockEditor) Init() tea.Cmd                          { return nil }
func (m *mockEditor) Update(tea.Msg) (layout.Model, tea.Cmd) { return m, nil }
func (m *mockEditor) View() string                           { return "" }
func (m *mockEditor) SetSize(int, int) tea.Cmd               { return nil }
func (m *mockEditor) Focus() tea.Cmd                         { return nil }
func (m *mockEditor) Blur() tea.Cmd                          { return nil }
func (m *mockEditor) SetWorking(bool) tea.Cmd                { return nil }
func (m *mockEditor) AcceptSuggestion() tea.Cmd              { return nil }
func (m *mockEditor) ScrollByWheel(int)                      {}
func (m *mockEditor) Value() string                          { return "" }
func (m *mockEditor) SetValue(string)                        {}
func (m *mockEditor) InsertText(string)                      {}
func (m *mockEditor) AttachFile(string) error                { return nil }
func (m *mockEditor) Cleanup()                               { m.cleanupCalled = true }
func (m *mockEditor) GetSize() (int, int)                    { return 0, 0 }
func (m *mockEditor) BannerHeight() int                      { return 0 }
func (m *mockEditor) AttachmentAt(int) (editor.AttachmentPreview, bool) {
	return editor.AttachmentPreview{}, false
}
func (m *mockEditor) SetRecording(bool) tea.Cmd                   { return nil }
func (m *mockEditor) IsRecording() bool                           { return false }
func (m *mockEditor) IsHistorySearchActive() bool                 { return false }
func (m *mockEditor) EnterHistorySearch() (layout.Model, tea.Cmd) { return m, nil }
func (m *mockEditor) SendContent() tea.Cmd                        { return nil }

// collectMsgs executes a command (or batch/sequence of commands) and collects all returned messages.
func collectMsgs(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}

	msg := cmd()
	if msg == nil {
		return nil
	}

	if batchMsg, ok := msg.(tea.BatchMsg); ok {
		var msgs []tea.Msg
		for _, innerCmd := range batchMsg {
			if innerCmd != nil {
				msgs = append(msgs, collectMsgs(innerCmd)...)
			}
		}
		return msgs
	}

	// Handle Sequence (unexported type, use reflection)
	msgValue := reflect.ValueOf(msg)
	if msgValue.Kind() == reflect.Slice {
		var msgs []tea.Msg
		for i := range msgValue.Len() {
			elem := msgValue.Index(i)
			if elem.CanInterface() {
				if innerCmd, ok := elem.Interface().(tea.Cmd); ok && innerCmd != nil {
					msgs = append(msgs, collectMsgs(innerCmd)...)
				}
			}
		}
		if len(msgs) > 0 {
			return msgs
		}
	}

	return []tea.Msg{msg}
}

func hasMsg[T any](msgs []tea.Msg) bool {
	for _, msg := range msgs {
		if _, ok := msg.(T); ok {
			return true
		}
	}
	return false
}

func newTestModel(tb testing.TB) (*appModel, *mockEditor) {
	tb.Helper()
	page := &mockChatPage{}
	ed := &mockEditor{}

	m := &appModel{
		ctx:                     tb.Context,
		chatPages:               map[string]chat.Page{"test": page},
		sessionStates:           map[string]*service.SessionState{},
		editors:                 map[string]editor.Editor{"test": ed},
		pendingRestores:         map[string]string{},
		pendingSidebarCollapsed: map[string]bool{},
		stashedDialogs:          map[string]stashedDialog{},
		chatPage:                page,
		editor:                  ed,
		transcriber:             &fakeTranscriber{},
		notification:            notification.New(),
		dialogMgr:               dialog.New(),
		completions:             completion.New(),
	}
	return m, ed
}

// neutralizeExitFunc replaces the model's exit function with a no-op for the
// duration of the test and waits for the safety-net goroutine to fire (or time
// out). It sets per-model fields rather than package globals, so tests using
// it may run in parallel.
func neutralizeExitFunc(t *testing.T, m *appModel) {
	t.Helper()

	fired := make(chan struct{})
	var once sync.Once
	m.exitFunc = func(int) {
		once.Do(func() { close(fired) })
	}
	m.shutdownTimeout = 10 * time.Millisecond

	t.Cleanup(func() {
		select {
		case <-fired:
		case <-time.After(200 * time.Millisecond):
		}
	})
}

func TestExitSessionMsg_ExitsImmediately(t *testing.T) {
	t.Parallel()

	m, ed := newTestModel(t)
	neutralizeExitFunc(t, m)

	_, cmd := m.Update(messages.ExitSessionMsg{})

	assert.True(t, ed.cleanupCalled, "Cleanup() should be called on editor")
	require.NotNil(t, cmd, "cmd should not be nil")
	msgs := collectMsgs(cmd)
	assert.True(t, hasMsg[tea.QuitMsg](msgs), "should produce tea.QuitMsg for immediate exit")
}

func TestExitConfirmedMsg_ExitsImmediately(t *testing.T) {
	t.Parallel()

	m, ed := newTestModel(t)
	neutralizeExitFunc(t, m)

	_, cmd := m.Update(dialog.ExitConfirmedMsg{})

	assert.True(t, ed.cleanupCalled, "Cleanup() should be called on editor")
	require.NotNil(t, cmd, "cmd should not be nil")
	msgs := collectMsgs(cmd)
	assert.True(t, hasMsg[tea.QuitMsg](msgs), "should produce tea.QuitMsg")
}

// blockingWriter is an io.Writer whose Write blocks until unblocked.
type blockingWriter struct {
	mu      sync.Mutex
	blocked chan struct{} // closed once the first Write starts blocking
	gate    chan struct{} // Write blocks until this is closed
}

func newBlockingWriter() *blockingWriter {
	return &blockingWriter{
		blocked: make(chan struct{}),
		gate:    make(chan struct{}),
	}
}

func (w *blockingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	select {
	case <-w.blocked:
	default:
		close(w.blocked)
	}
	gate := w.gate
	w.mu.Unlock()

	<-gate
	return len(p), nil
}

// reblock installs a new gate so that subsequent writes block again.
func (w *blockingWriter) reblock() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.gate = make(chan struct{})
}

// unblock releases all pending and future writes.
func (w *blockingWriter) unblock() {
	w.mu.Lock()
	defer w.mu.Unlock()
	select {
	case <-w.gate:
	default:
		close(w.gate)
	}
}

// quitModel is a minimal bubbletea model that requests alt-screen and quits
// on triggerQuitMsg. onQuit, if set, runs before tea.Quit.
type quitModel struct {
	onQuit func()
}

type triggerQuitMsg struct{}

func (m *quitModel) Init() tea.Cmd { return nil }

func (m *quitModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(triggerQuitMsg); ok {
		if m.onQuit != nil {
			m.onQuit()
		}
		return m, tea.Quit
	}
	return m, nil
}

func (m *quitModel) View() tea.View {
	v := tea.NewView("hello world")
	v.AltScreen = true
	return v
}

// initBlockingBubbletea starts a bubbletea program whose stdout will block
// the renderer on its next flush. Used to reproduce the wedged-renderer
// shutdown deadlock.
func initBlockingBubbletea(t *testing.T, model tea.Model) (*tea.Program, *blockingWriter, <-chan struct{}) {
	t.Helper()

	w := newBlockingWriter()
	var in bytes.Buffer

	p := tea.NewProgram(model,
		tea.WithContext(t.Context()),
		tea.WithInput(&in),
		tea.WithOutput(w),
	)

	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_, _ = p.Run()
	}()

	select {
	case <-w.blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for initial write to block")
	}

	// Let the initial writes through so the event loop starts, then re-block
	// so the next flush stalls.
	w.unblock()
	time.Sleep(200 * time.Millisecond)
	w.reblock()

	return p, w, runDone
}

// TestCleanupAll_SpawnsSafetyNet: an unstarted Program has a nil finished
// channel, so Wait() blocks forever — same shape as a real renderer
// deadlock. exitFunc must fire after shutdownTimeout.
func TestCleanupAll_SpawnsSafetyNet(t *testing.T) {
	t.Parallel()

	m, _ := newTestModel(t)
	m.shutdownTimeout = 200 * time.Millisecond

	exitDone := make(chan int, 1)
	m.exitFunc = func(code int) {
		exitDone <- code
	}

	m.program = tea.NewProgram(&quitModel{})
	m.cleanupAll()

	select {
	case code := <-exitDone:
		assert.Equal(t, 0, code)
	case <-time.After(m.shutdownTimeout + time.Second):
		t.Fatal("exitFunc was not called — safety net is missing from cleanupAll")
	}
}

// TestCleanupAll_GracefulShutdownSkipsExit: when Wait() returns promptly,
// the safety net must not call exitFunc.
func TestCleanupAll_GracefulShutdownSkipsExit(t *testing.T) {
	t.Parallel()

	m, _ := newTestModel(t)
	m.shutdownTimeout = 2 * time.Second

	var exitCalled atomic.Bool
	m.exitFunc = func(int) { exitCalled.Store(true) }

	var in, out bytes.Buffer
	p := tea.NewProgram(&quitModel{},
		tea.WithContext(t.Context()),
		tea.WithInput(&in),
		tea.WithOutput(&out),
	)

	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_, _ = p.Run()
	}()

	// Send blocks until the program is running, which guarantees Run() has
	// initialized p.finished — otherwise Wait() races the assignment.
	p.Send(syncMsg{})

	m.program = p
	m.cleanupAll()

	p.Send(triggerQuitMsg{})

	select {
	case <-runDone:
	case <-time.After(3 * time.Second):
		t.Fatal("p.Run() did not return within deadline")
	}

	// Let the safety-net goroutine observe Wait() returning.
	time.Sleep(100 * time.Millisecond)
	assert.False(t, exitCalled.Load(),
		"exitFunc must not fire on prompt shutdown")
}

// syncMsg pings the program's event loop to confirm Run() has started.
type syncMsg struct{}

// TestCleanupAll_NilProgramIsSafe: with no program wired, cleanupAll is a
// no-op and exitFunc is never called.
func TestCleanupAll_NilProgramIsSafe(t *testing.T) {
	t.Parallel()

	m, _ := newTestModel(t)
	m.shutdownTimeout = 20 * time.Millisecond

	var exitCalled atomic.Bool
	m.exitFunc = func(int) { exitCalled.Store(true) }

	m.program = nil
	assert.NotPanics(t, func() { m.cleanupAll() })

	time.Sleep(m.shutdownTimeout + 50*time.Millisecond)
	assert.False(t, exitCalled.Load(), "exitFunc must not fire without a program")
}

// TestCleanupAll_WedgedStdoutFiresExit: the realistic case. The renderer is
// stuck on a wedged stdout write, and once tea.Quit fires the final flush
// would itself re-acquire the same mutex — a hard deadlock. Wait() never
// returns and ReleaseTerminal would block too; exitFunc must still fire.
func TestCleanupAll_WedgedStdoutFiresExit(t *testing.T) {
	t.Parallel()

	m, _ := newTestModel(t)
	m.shutdownTimeout = 300 * time.Millisecond

	exitDone := make(chan struct{})
	m.exitFunc = func(int) { close(exitDone) }

	p, w, _ := initBlockingBubbletea(t, &quitModel{})
	defer w.unblock()

	m.program = p
	m.cleanupAll()

	// Drive the program into the deadlock path: tea.Quit triggers the final
	// render flush against the wedged writer, which is the actual upstream
	// bug the safety net guards against.
	p.Send(triggerQuitMsg{})

	select {
	case <-exitDone:
	case <-time.After(m.shutdownTimeout + 2*time.Second):
		t.Fatal("exitFunc was not called — safety net is blocked by ReleaseTerminal")
	}
}

// TestCleanupAll_MultipleCallsFireExitOnce: cleanupAll is invoked from
// several message handlers (ExitSessionMsg, ExitConfirmedMsg, …) and may
// run more than once on the same model. Each safety-net goroutine snapshots
// exitFunc, so without a guard each one would call exit(0) on timeout —
// fine in production where exit is os.Exit, fatal in tests where it's a
// channel close.
func TestCleanupAll_MultipleCallsFireExitOnce(t *testing.T) {
	t.Parallel()

	m, _ := newTestModel(t)
	m.shutdownTimeout = 100 * time.Millisecond

	var exitCount atomic.Int32
	m.exitFunc = func(int) { exitCount.Add(1) }

	m.program = tea.NewProgram(&quitModel{})

	m.cleanupAll()
	m.cleanupAll()
	m.cleanupAll()

	time.Sleep(m.shutdownTimeout + 200*time.Millisecond)
	assert.Equal(t, int32(1), exitCount.Load(),
		"only the first cleanupAll should arm a safety net")
}

// TestExitDeadlock_BlockedStdout proves the underlying bubbletea bug: Run()
// hangs when stdout blocks during the final render after tea.Quit.
func TestExitDeadlock_BlockedStdout(t *testing.T) {
	t.Parallel()

	model := &quitModel{}
	p, w, runDone := initBlockingBubbletea(t, model)

	p.Send(triggerQuitMsg{})

	select {
	case <-runDone:
		t.Skip("bubbletea returned without deadlocking; upstream fix may have landed")
	case <-time.After(2 * time.Second):
	}

	w.unblock()
}

// TestExitSafetyNet_BlockedStdout: with a wedged renderer, an external
// safety-net (simulated here in onQuit) must force the process to exit.
func TestExitSafetyNet_BlockedStdout(t *testing.T) {
	t.Parallel()

	const safetyNetTimeout = 500 * time.Millisecond
	var exitCalled atomic.Bool
	exitDone := make(chan int, 1)
	testExitFunc := func(code int) {
		exitCalled.Store(true)
		exitDone <- code
	}

	model := &quitModel{
		onQuit: func() {
			go func() {
				time.Sleep(safetyNetTimeout)
				testExitFunc(0)
			}()
		},
	}
	p, w, runDone := initBlockingBubbletea(t, model)
	defer w.unblock()

	p.Send(triggerQuitMsg{})

	select {
	case code := <-exitDone:
		assert.True(t, exitCalled.Load())
		assert.Equal(t, 0, code)
	case <-runDone:
		// Run() returned on its own — also acceptable.
	case <-time.After(safetyNetTimeout + 2*time.Second):
		t.Fatal("neither Run() returned nor safety-net exitFunc fired")
	}
}

// TestExitSafetyNet_GracefulShutdown: on a clean shutdown, Run() must return
// before the safety net fires.
func TestExitSafetyNet_GracefulShutdown(t *testing.T) {
	t.Parallel()

	const safetyNetTimeout = 2 * time.Second
	var exitCalled atomic.Bool
	testExitFunc := func(int) {
		exitCalled.Store(true)
	}

	var mu sync.Mutex
	cleanupCalled := false

	model := &quitModel{
		onQuit: func() {
			mu.Lock()
			cleanupCalled = true
			mu.Unlock()
			go func() {
				time.Sleep(safetyNetTimeout)
				testExitFunc(0)
			}()
		},
	}
	var buf bytes.Buffer
	var in bytes.Buffer

	p := tea.NewProgram(model,
		tea.WithContext(t.Context()),
		tea.WithInput(&in),
		tea.WithOutput(&buf),
	)

	runDone := make(chan error, 1)
	go func() {
		_, err := p.Run()
		runDone <- err
	}()

	time.Sleep(200 * time.Millisecond)

	p.Send(triggerQuitMsg{})

	select {
	case err := <-runDone:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("p.Run() did not return")
	}

	mu.Lock()
	assert.True(t, cleanupCalled)
	mu.Unlock()
	assert.False(t, exitCalled.Load(), "exitFunc must not fire on graceful shutdown")
}
