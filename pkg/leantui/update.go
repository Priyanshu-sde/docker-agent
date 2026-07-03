package leantui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/docker/docker-agent/pkg/effort"
	"github.com/docker/docker-agent/pkg/runtime"
	"github.com/docker/docker-agent/pkg/tui/messages"
)

func (m *model) handleKey(ctx context.Context, k key) {
	if m.confirm != nil {
		m.handleConfirmKey(k)
		return
	}

	switch k.typ {
	case keyCtrlC:
		m.handleInterrupt()
	case keyCtrlD:
		if m.editor.isEmpty() {
			m.quit()
		} else {
			m.editor.deleteForward()
		}
	case keyEnter:
		m.handleEnter(ctx)
	case keyAltEnter:
		m.editor.insertNewline()
	case keyTab:
		m.handleTab()
	case keyShiftTab:
		m.handleCycleThinkingLevel(ctx)
	case keyUp:
		if m.ac.active {
			m.ac.moveUp()
		} else if !m.editor.up(m.width) {
			m.editor.historyPrev()
		}
	case keyDown:
		if m.ac.active {
			m.ac.moveDown()
		} else if !m.editor.down(m.width) {
			m.editor.historyNext()
		}
	case keyLeft:
		m.editor.moveLeft()
	case keyRight:
		m.editor.moveRight()
	case keyWordLeft:
		m.editor.moveWordLeft()
	case keyWordRight:
		m.editor.moveWordRight()
	case keyHome:
		m.editor.moveLineStart()
	case keyEnd:
		m.editor.moveLineEnd()
	case keyBackspace:
		m.editor.backspace()
	case keyDelete:
		m.editor.deleteForward()
	case keyCtrlU:
		m.editor.deleteToLineStart()
	case keyCtrlK:
		m.editor.deleteToLineEnd()
	case keyCtrlW:
		m.editor.deleteWordBack()
	case keyEsc:
		m.ac.dismiss()
	case keyCtrlL:
		m.clearScreen()
	case keyRune, keyPaste:
		m.editor.insert(k.runes)
	}

	m.ac.sync(m.editor.text())
}

func (m *model) handleInterrupt() {
	switch {
	case m.busy:
		if m.runCancel != nil {
			m.runCancel()
		}
		m.queue = nil
		m.transcript.addBlock(func(int) []string { return []string{stWarning().Render("⏹ Cancelled")} })
	case !m.editor.isEmpty():
		m.editor.reset()
		m.ac.dismiss()
	default:
		m.quit()
	}
}

func (m *model) handleEnter(ctx context.Context) {
	if m.ac.active {
		if cmd, ok := m.ac.current(); ok {
			m.ac.dismiss()
			m.submit(ctx, "/"+cmd.name)
			return
		}
	}
	m.submit(ctx, m.editor.text())
}

func (m *model) handleTab() {
	if !m.ac.active {
		return
	}
	if cmd, ok := m.ac.current(); ok {
		m.editor.setText("/" + cmd.name + " ")
		m.ac.sync(m.editor.text())
	}
}

func (m *model) handleCycleThinkingLevel(ctx context.Context) {
	if !m.thinkingLevelChangeable() {
		return
	}
	level, err := m.app.CycleAgentThinkingLevel(ctx)
	if err != nil {
		m.reportThinkingLevelError("change", err)
		return
	}
	m.status.thinking = level.String()
}

// handleSetThinkingLevel applies the /effort command: it sets the current
// model's reasoning-effort level to the requested value.
func (m *model) handleSetThinkingLevel(ctx context.Context, level string) {
	if !m.thinkingLevelChangeable() {
		return
	}
	if level == "" {
		m.addNotice("", "Usage: /effort <none|minimal|low|medium|high|xhigh|max>", stMuted())
		return
	}
	parsed, ok := effort.Parse(level)
	if !ok {
		m.addNotice("✗ ", fmt.Sprintf("Unknown effort level %q (valid: none, minimal, low, medium, high, xhigh, max)", level), stError())
		return
	}
	applied, err := m.app.SetAgentThinkingLevel(ctx, parsed)
	if err != nil {
		m.reportThinkingLevelError("set", err)
		return
	}
	m.status.thinking = applied.String()
	m.addNotice("", "Reasoning effort set to "+applied.String(), stMuted())
}

// thinkingLevelChangeable reports whether the reasoning-effort level can be
// changed, emitting an explanatory notice when it cannot.
func (m *model) thinkingLevelChangeable() bool {
	if m.app == nil {
		return false
	}
	if !m.app.SupportsModelSwitching() {
		m.addNotice("", "Thinking levels can't be changed with remote runtimes", stMuted())
		return false
	}
	return true
}

// reportThinkingLevelError emits a notice for a failed thinking-level change,
// distinguishing the unsupported-model case from other failures.
func (m *model) reportThinkingLevelError(action string, err error) {
	if errors.Is(err, runtime.ErrUnsupported) {
		m.addNotice("", "Current model does not support thinking levels", stMuted())
		return
	}
	m.addNotice("✗ ", fmt.Sprintf("Failed to %s thinking level: %v", action, err), stError())
}

func (m *model) submit(ctx context.Context, text string) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return
	}
	m.editor.rememberHistory(trimmed)
	m.editor.reset()
	m.ac.dismiss()

	if strings.HasPrefix(trimmed, "/") && m.handleSlash(ctx, trimmed) {
		return
	}

	m.addUserEcho(trimmed)

	if m.app.IsReadOnly() {
		m.addNotice("⚠ ", "This session is read-only.", stWarning())
		return
	}
	m.enqueueOrRun(ctx, trimmed)
}

// handleSlash dispatches a slash command. It returns true when the command was
// fully handled (built-in, skill, or agent command) and false when the input
// should be treated as a normal message.
func (m *model) handleSlash(ctx context.Context, text string) bool {
	name, rest := splitCommand(text)
	switch name {
	case "exit", "quit":
		m.quit()
		return true
	case "new":
		m.app.NewSession()
		m.resetConversation()
		m.addNotice("", "Started a new session.", stMuted())
		m.refreshCommands(ctx)
		return true
	case "clear":
		m.clearScreen()
		return true
	case "help":
		m.commitHelp()
		return true
	case "compact":
		m.addUserEcho(text)
		m.startCompact(ctx, rest)
		return true
	case "effort":
		m.handleSetThinkingLevel(ctx, rest)
		return true
	}

	if skillName, task, ok := m.app.SkillCommandFork(ctx, text); ok {
		m.addUserEcho(text)
		m.startSkillFork(ctx, skillName, task)
		return true
	}

	if _, _, ok := m.app.LookupCommand(ctx, text); ok {
		m.addUserEcho(text)
		m.enqueueOrRun(ctx, m.app.ResolveInput(ctx, text))
		return true
	}

	if resolved, err := m.app.ResolveSkillCommand(ctx, text); err == nil && resolved != "" {
		m.addUserEcho(text)
		m.enqueueOrRun(ctx, resolved)
		return true
	}

	return false
}

// enqueueOrRun starts a run immediately when idle, or queues the message to run
// after the current response finishes.
func (m *model) enqueueOrRun(ctx context.Context, message string) {
	if m.app.IsReadOnly() {
		return
	}
	if m.busy {
		m.queue = append(m.queue, message)
		return
	}
	m.startRun(ctx, message, nil)
}

func (m *model) sendFirstMessage(ctx context.Context, msg, attachPath string) {
	var atts []messages.Attachment
	if attachPath != "" {
		if abs, err := filepath.Abs(attachPath); err == nil {
			atts = append(atts, messages.Attachment{Name: filepath.Base(abs), FilePath: abs})
		}
	}

	trimmed := strings.TrimSpace(msg)
	switch {
	case trimmed != "":
		m.addUserEcho(trimmed)
	case len(atts) > 0:
		m.addNotice("", "(attached "+atts[0].Name+")", stMuted())
	default:
		return
	}

	content := msg
	if strings.HasPrefix(trimmed, "/") {
		if resolved := m.app.ResolveInput(ctx, trimmed); resolved != "" {
			content = resolved
		}
	}
	m.startRun(ctx, content, atts)
}

// beginRun marks the model busy and returns a cancelable context for a new
// run, storing its cancel func so it can be interrupted.
func (m *model) beginRun(ctx context.Context) (context.Context, context.CancelFunc) {
	runCtx, cancel := context.WithCancel(ctx)
	m.runCancel = cancel
	m.busy = true
	return runCtx, cancel
}

func (m *model) startRun(ctx context.Context, message string, attachments []messages.Attachment) {
	runCtx, cancel := m.beginRun(ctx)
	m.app.Run(runCtx, cancel, message, attachments)
}

func (m *model) startCompact(ctx context.Context, prompt string) {
	runCtx, cancel := m.beginRun(ctx)
	m.app.CompactSession(runCtx, cancel, prompt)
}

func (m *model) startSkillFork(ctx context.Context, name, task string) {
	runCtx, cancel := m.beginRun(ctx)
	m.app.RunSkillFork(runCtx, cancel, name, task, nil)
}

func (m *model) refreshCommands(ctx context.Context) {
	cmds := builtinCommands()
	for name, c := range m.app.CurrentAgentCommands(ctx) {
		if m.disabledCommands[name] {
			continue
		}
		cmds = append(cmds, command{name: name, desc: c.DisplayText(), kind: cmdAgent})
	}
	for _, sk := range m.app.CurrentAgentSkills() {
		cmds = append(cmds, command{name: sk.Name, desc: sk.Description, kind: cmdAgent})
	}
	m.ac.setCommands(cmds)
}

func (m *model) handleConfirmKey(k key) {
	if k.typ == keyEsc {
		m.resolveConfirm(runtime.ResumeReject("rejected by user"))
		return
	}
	if k.typ != keyRune || len(k.runes) == 0 {
		return
	}
	switch k.runes[0] {
	case 'y', 'Y':
		m.resolveConfirm(runtime.ResumeApprove())
	case 'a', 'A':
		m.resolveConfirm(runtime.ResumeApproveTool(m.confirm.tool))
	case 's', 'S':
		m.resolveConfirm(runtime.ResumeApproveSession())
	case 'n', 'N':
		m.resolveConfirm(runtime.ResumeReject("rejected by user"))
	}
}

func (m *model) resolveConfirm(req runtime.ResumeRequest) {
	m.app.Resume(req)
	m.confirm = nil
}

func (m *model) resetConversation() {
	if m.runCancel != nil {
		m.runCancel()
		m.runCancel = nil
	}
	m.transcript.clearActive()
	m.queue = nil
	m.busy = false
	m.confirm = nil
	m.usage.reset()
	m.status.contextLength = 0
	m.status.contextLimit = 0
	m.status.tokens = 0
	m.status.cost = 0
	m.status.costKnown = false
}

func (m *model) clearScreen() {
	m.r.repaint()
}

func (m *model) quit() {
	if m.runCancel != nil {
		m.runCancel()
	}
	m.quitting = true
}

func (m *model) addUserEcho(text string) {
	m.transcript.addBlock(func(w int) []string { return renderUserLines(text, w) })
}

func (m *model) addNotice(prefix, text string, style lipgloss.Style) {
	m.transcript.addBlock(func(w int) []string { return renderNoticeLines(prefix, text, w, style) })
}

func (m *model) commitHelp() {
	m.transcript.addBlock(func(int) []string {
		return []string{
			stBold().Render("Commands"),
			stMuted().Render("  /new       start a new session"),
			stMuted().Render("  /compact   summarize and compact the conversation"),
			stMuted().Render("  /effort    set the model's reasoning effort (e.g. /effort high)"),
			stMuted().Render("  /clear     clear the screen"),
			stMuted().Render("  /help      show this help"),
			stMuted().Render("  /exit      quit"),
			"",
			stBold().Render("Shortcuts"),
			stMuted().Render("  Enter      send             Alt+Enter   insert newline"),
			stMuted().Render("  Up/Down    history           Tab         complete command"),
			stMuted().Render("  Shift+Tab  cycle thinking    Ctrl+C      cancel / quit"),
			stMuted().Render("  Ctrl+W     delete previous word"),
		}
	})
}

func splitCommand(text string) (name, rest string) {
	text = strings.TrimPrefix(strings.TrimSpace(text), "/")
	name, rest, _ = strings.Cut(text, " ")
	return name, strings.TrimSpace(rest)
}
