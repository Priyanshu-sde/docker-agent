package leantui

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/docker/docker-agent/pkg/app"
	"github.com/docker/docker-agent/pkg/effort"
	"github.com/docker/docker-agent/pkg/runtime"
	"github.com/docker/docker-agent/pkg/session"
	"github.com/docker/docker-agent/pkg/sessiontitle"
	"github.com/docker/docker-agent/pkg/tools"
	skillstool "github.com/docker/docker-agent/pkg/tools/builtin/skills"
	mcptools "github.com/docker/docker-agent/pkg/tools/mcp"
)

type cycleThinkingRuntime struct {
	supports   bool
	level      effort.Level
	err        error
	cycleCalls int
	setCalls   int
	setLevel   effort.Level
}

func (r *cycleThinkingRuntime) CurrentAgentInfo(context.Context) runtime.CurrentAgentInfo {
	return runtime.CurrentAgentInfo{}
}
func (r *cycleThinkingRuntime) CurrentAgentName(context.Context) string       { return "coder" }
func (r *cycleThinkingRuntime) SetCurrentAgent(context.Context, string) error { return nil }
func (r *cycleThinkingRuntime) CurrentAgentTools(context.Context) ([]tools.Tool, error) {
	return nil, nil
}
func (r *cycleThinkingRuntime) CurrentAgentToolsetStatuses() []tools.ToolsetStatus { return nil }
func (r *cycleThinkingRuntime) RestartToolset(context.Context, string) error       { return nil }
func (r *cycleThinkingRuntime) EmitStartupInfo(context.Context, *session.Session, runtime.EventSink) {
}

func (r *cycleThinkingRuntime) EmitAgentInfo(_ context.Context, sink runtime.EventSink) {
	sink.Emit(runtime.TeamInfo([]runtime.AgentDetails{{Name: "coder", Thinking: r.level.String()}}, "coder"))
}
func (r *cycleThinkingRuntime) ResetStartupInfo() {}
func (r *cycleThinkingRuntime) RunStream(context.Context, *session.Session) <-chan runtime.Event {
	ch := make(chan runtime.Event)
	close(ch)
	return ch
}

func (r *cycleThinkingRuntime) Run(context.Context, *session.Session) ([]session.Message, error) {
	return nil, nil
}
func (r *cycleThinkingRuntime) Resume(context.Context, runtime.ResumeRequest) {}
func (r *cycleThinkingRuntime) ResumeElicitation(context.Context, tools.ElicitationAction, map[string]any) error {
	return nil
}
func (r *cycleThinkingRuntime) SessionStore() session.Store { return nil }
func (r *cycleThinkingRuntime) Summarize(context.Context, *session.Session, string, runtime.EventSink) {
}
func (r *cycleThinkingRuntime) PermissionsInfo() *runtime.PermissionsInfo { return nil }
func (r *cycleThinkingRuntime) CurrentAgentSkillsToolset() *skillstool.ToolSet {
	return nil
}

func (r *cycleThinkingRuntime) RunSkillFork(context.Context, *session.Session, skillstool.RunSkillArgs, runtime.EventSink) (*tools.ToolCallResult, error) {
	return nil, nil
}

func (r *cycleThinkingRuntime) CurrentMCPPrompts(context.Context) map[string]mcptools.PromptInfo {
	return nil
}

func (r *cycleThinkingRuntime) ExecuteMCPPrompt(context.Context, string, map[string]string) (string, error) {
	return "", nil
}

func (r *cycleThinkingRuntime) UpdateSessionTitle(_ context.Context, sess *session.Session, title string) error {
	sess.Title = title
	return nil
}
func (r *cycleThinkingRuntime) TitleGenerator(context.Context) *sessiontitle.Generator { return nil }
func (r *cycleThinkingRuntime) Close() error                                           { return nil }
func (r *cycleThinkingRuntime) Stop()                                                  {}
func (r *cycleThinkingRuntime) Steer(context.Context, runtime.QueuedMessage) error     { return nil }
func (r *cycleThinkingRuntime) FollowUp(context.Context, runtime.QueuedMessage) error  { return nil }
func (r *cycleThinkingRuntime) QueueStatus() runtime.QueueStatus                       { return runtime.QueueStatus{} }

func (r *cycleThinkingRuntime) TogglePause(context.Context) (bool, error) {
	return false, nil
}
func (r *cycleThinkingRuntime) SetAgentModel(context.Context, string, string) error { return nil }
func (r *cycleThinkingRuntime) CycleAgentThinkingLevel(context.Context, string) (effort.Level, error) {
	r.cycleCalls++
	if r.err != nil {
		return "", r.err
	}
	return r.level, nil
}

func (r *cycleThinkingRuntime) SetAgentThinkingLevel(_ context.Context, _ string, level effort.Level) (effort.Level, error) {
	r.setCalls++
	if r.err != nil {
		return "", r.err
	}
	r.setLevel = level
	return level, nil
}
func (r *cycleThinkingRuntime) AvailableModels(context.Context) []runtime.ModelChoice { return nil }
func (r *cycleThinkingRuntime) SupportsModelSwitching() bool                          { return r.supports }
func (r *cycleThinkingRuntime) OnToolsChanged(func(runtime.Event))                    {}

var _ runtime.Runtime = (*cycleThinkingRuntime)(nil)

func TestShiftTabCyclesThinkingLevel(t *testing.T) {
	t.Parallel()
	rt := &cycleThinkingRuntime{supports: true, level: effort.High}
	m := bareModel(24)
	m.app = app.New(t.Context(), rt, session.New())

	m.handleKey(t.Context(), key{typ: keyShiftTab})

	assert.Equal(t, 1, rt.cycleCalls)
	assert.Equal(t, "high", m.status.thinking)
	assert.Empty(t, m.blocks)
}

func TestShiftTabReportsUnsupportedThinkingLevel(t *testing.T) {
	t.Parallel()
	rt := &cycleThinkingRuntime{supports: true, err: runtime.ErrUnsupported}
	m := bareModel(24)
	m.app = app.New(t.Context(), rt, session.New())

	m.handleKey(t.Context(), key{typ: keyShiftTab})

	assert.Equal(t, 1, rt.cycleCalls)
	assert.Empty(t, m.status.thinking)
	assert.Len(t, m.blocks, 1)
}

func TestEffortCommandSetsThinkingLevel(t *testing.T) {
	t.Parallel()
	rt := &cycleThinkingRuntime{supports: true}
	m := bareModel(24)
	m.app = app.New(t.Context(), rt, session.New())

	m.handleSetThinkingLevel(t.Context(), "high")

	assert.Equal(t, 1, rt.setCalls)
	assert.Equal(t, effort.High, rt.setLevel)
	assert.Equal(t, "high", m.status.thinking)
}

func TestEffortCommandRejectsUnknownLevel(t *testing.T) {
	t.Parallel()
	rt := &cycleThinkingRuntime{supports: true}
	m := bareModel(24)
	m.app = app.New(t.Context(), rt, session.New())

	m.handleSetThinkingLevel(t.Context(), "turbo")

	assert.Zero(t, rt.setCalls)
	assert.Empty(t, m.status.thinking)
	assert.Len(t, m.blocks, 1)
}
