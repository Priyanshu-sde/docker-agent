package sidebar

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/docker/docker-agent/pkg/runtime"
	"github.com/docker/docker-agent/pkg/session"
	"github.com/docker/docker-agent/pkg/tui/service"
)

func TestActiveSessionTokens_SingleSession(t *testing.T) {
	t.Parallel()

	m := newTestSidebar()
	m.startStream("session-1", "root")
	m.recordUsageTokens("session-1", "root", 5000, 3000)

	tokens, found := m.activeSessionTokens()
	assert.True(t, found)
	assert.Equal(t, int64(8000), tokens)
}

// TestActiveSessionTokens_TracksActiveSubSession verifies the panel shows the
// running sub-session's tokens while it is active, then the parent's again once
// the sub-session stops.
func TestActiveSessionTokens_TracksActiveSubSession(t *testing.T) {
	t.Parallel()

	m := newTestSidebar()

	m.startStream("session-root", "root")
	m.recordUsageTokens("session-root", "root", 20000, 10000)

	// Sub-agent runs as a nested stream.
	m.startStream("session-child", "developer")
	m.recordUsageTokens("session-child", "developer", 8000, 2000)

	tokens, found := m.activeSessionTokens()
	assert.True(t, found)
	assert.Equal(t, int64(10000), tokens, "while the sub-agent runs, show its tokens")

	// Sub-agent done — back to the parent's tokens.
	m.stopStream()
	tokens, found = m.activeSessionTokens()
	assert.True(t, found)
	assert.Equal(t, int64(30000), tokens, "after the sub-agent returns, show the parent's tokens")
}

func TestActiveSessionTokens_FallbackToSingleSession(t *testing.T) {
	t.Parallel()

	sess := session.New()
	sessionState := service.NewSessionState(sess)
	m := New(sessionState).(*model)

	m.sessionUsage["session-1"] = &runtime.Usage{
		InputTokens:  5000,
		OutputTokens: 5000,
	}

	tokens, found := m.activeSessionTokens()
	assert.True(t, found)
	assert.Equal(t, int64(10000), tokens)
}

func TestActiveSessionTokens_Empty(t *testing.T) {
	t.Parallel()

	sess := session.New()
	sessionState := service.NewSessionState(sess)
	m := New(sessionState).(*model)

	tokens, found := m.activeSessionTokens()
	assert.False(t, found)
	assert.Equal(t, int64(0), tokens)
}

// TestActiveSessionTokens_StableDuringSubAgent verifies the reported count does
// not flicker while a sub-agent is the active session: it consistently reports
// the sub-session's tokens regardless of which agent last emitted an event.
func TestActiveSessionTokens_StableDuringSubAgent(t *testing.T) {
	t.Parallel()

	m := newTestSidebar()

	m.startStream("session-root", "root")
	m.recordUsageTokens("session-root", "root", 20000, 10000)

	m.startStream("session-child", "developer")
	m.recordUsageTokens("session-child", "developer", 8000, 2000)

	// Even if the parent agent name lingers in currentAgent, the active
	// session is the sub-session and must be reported consistently.
	m.currentAgent = "root"
	for range 100 {
		tokens, found := m.activeSessionTokens()
		assert.True(t, found)
		assert.Equal(t, int64(10000), tokens, "activeSessionTokens() flickered while a sub-agent was running")
	}
}

// TestActiveSessionTokens_RecoversFromImbalancedStreams verifies that a new
// top-level run starting with a reset is not pinned to a leaked sub-session
// from a previous run whose stream events were left unbalanced.
func TestActiveSessionTokens_RecoversFromImbalancedStreams(t *testing.T) {
	t.Parallel()

	m := newTestSidebar()

	// Turn 1: a sub-agent stream is left unbalanced (no matching stop).
	m.startStream("session-root", "root")
	m.recordUsageTokens("session-root", "root", 20000, 10000) // 30000
	m.startStream("session-child", "developer")
	m.recordUsageTokens("session-child", "developer", 8000, 2000) // 10000

	// Turn 2: a new top-level run resets tracking, runs, and completes.
	m.ResetStreamTracking()
	m.startStream("session-root", "root")
	m.recordUsageTokens("session-root", "root", 25000, 10000) // 35000
	m.stopStream()

	// Idle after the turn: must show the main session, not the leaked child.
	tokens, found := m.activeSessionTokens()
	assert.True(t, found)
	assert.Equal(t, int64(35000), tokens)
}

func TestTokenUsageSummary_SingleSession(t *testing.T) {
	t.Parallel()

	m := newTestSidebar()
	m.startStream("session-1", "root")
	m.SetTokenUsage(&runtime.TokenUsageEvent{
		SessionID:    "session-1",
		AgentContext: runtime.AgentContext{AgentName: "root"},
		Usage: &runtime.Usage{
			InputTokens:   5000,
			OutputTokens:  3000,
			ContextLength: 8000,
			ContextLimit:  100000,
			Cost:          0.05,
		},
	})

	summary := m.tokenUsageSummary()
	// Single session: shows total tokens, cost, and context
	assert.Contains(t, summary, "Tokens: 8.0K")
	assert.Contains(t, summary, "Cost: $0.05")
	assert.Contains(t, summary, "Context: 8%")
	assert.NotContains(t, summary, "sub-sessions")
}

func TestTokenUsageSummary_MultipleSessions_ShowsActiveSessionTokens(t *testing.T) {
	t.Parallel()

	m := newTestSidebar()

	// Root agent session: 30K tokens, $0.10
	m.startStream("session-root", "root")
	m.SetTokenUsage(&runtime.TokenUsageEvent{
		SessionID:    "session-root",
		AgentContext: runtime.AgentContext{AgentName: "root"},
		Usage: &runtime.Usage{
			InputTokens:   20000,
			OutputTokens:  10000,
			ContextLength: 30000,
			ContextLimit:  100000,
			Cost:          0.10,
		},
	})

	// Child agent session: 10K tokens, $0.05
	m.startStream("session-child", "developer")
	m.SetTokenUsage(&runtime.TokenUsageEvent{
		SessionID:    "session-child",
		AgentContext: runtime.AgentContext{AgentName: "developer"},
		Usage: &runtime.Usage{
			InputTokens:   8000,
			OutputTokens:  2000,
			ContextLength: 10000,
			ContextLimit:  200000,
			Cost:          0.05,
		},
	})

	// While the sub-agent runs, show its tokens and context, with the
	// aggregated cost and sub-session count.
	summary := m.tokenUsageSummary()
	assert.Contains(t, summary, "Tokens: 10.0K")
	assert.Contains(t, summary, "Cost: $0.15")
	assert.Contains(t, summary, "Context: 5%")
	assert.Contains(t, summary, "1 sub-sessions")

	// Once it returns, the parent's tokens and context are shown again.
	m.stopStream()
	summary = m.tokenUsageSummary()
	assert.Contains(t, summary, "Tokens: 30.0K")
	assert.Contains(t, summary, "Cost: $0.15")
	assert.Contains(t, summary, "Context: 30%")
}

func TestTokenUsageSummary_Empty(t *testing.T) {
	t.Parallel()

	sess := session.New()
	sessionState := service.NewSessionState(sess)
	m := New(sessionState).(*model)

	assert.Empty(t, m.tokenUsageSummary())
}
