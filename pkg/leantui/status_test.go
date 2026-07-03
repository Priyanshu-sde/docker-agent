package leantui

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/docker/docker-agent/pkg/runtime"
)

func TestFormatTokens(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "500", formatTokens(500))
	assert.Equal(t, "999", formatTokens(999))
	assert.Equal(t, "1.0k", formatTokens(1000))
	assert.Equal(t, "1.2k", formatTokens(1234))
	assert.Equal(t, "1.0M", formatTokens(1_000_000))
	assert.Equal(t, "2.5M", formatTokens(2_500_000))
}

func TestComposeLineRightAligns(t *testing.T) {
	t.Parallel()
	out := composeLine("left", "right", 20)
	assert.Equal(t, 20, displayWidth(out))
	assert.GreaterOrEqual(t, len(out), len("left")+len("right"))
	assert.Contains(t, out, "left")
	assert.Contains(t, out, "right")
}

func TestComposeLineTruncatesLeft(t *testing.T) {
	t.Parallel()
	out := composeLine("a very long left side that does not fit", "right", 15)
	assert.LessOrEqual(t, displayWidth(out), 15)
	assert.Contains(t, out, "right")
}

func TestRenderBarWidth(t *testing.T) {
	t.Parallel()
	assert.Equal(t, contextBarWidth, displayWidth(renderBar(0.5)))
	assert.Equal(t, contextBarWidth, displayWidth(renderBar(0)))
	assert.Equal(t, contextBarWidth, displayWidth(renderBar(1)))
	assert.Equal(t, contextBarWidth, displayWidth(renderBar(1.5))) // clamped
}

func TestRenderContextShowsZerosBeforeUsage(t *testing.T) {
	t.Parallel()
	out := renderContext(statusData{})
	assert.NotContains(t, out, "context")
	assert.Contains(t, out, "0% · 0/0")
}

func TestAgentInfoContextLimitShownBeforeUsage(t *testing.T) {
	t.Parallel()
	m := bareModel(24)

	m.handleEvent(t.Context(), runtime.AgentInfo("root", "test/model", "", "", 200_000))

	assert.Equal(t, int64(200_000), m.status.contextLimit)
	assert.Contains(t, renderContext(m.status), "0% · 0/200.0k")
}

func TestRenderStatusFitsWidth(t *testing.T) {
	t.Parallel()
	d := statusData{
		workingDir:    "/home/user/project",
		branch:        "main",
		agent:         "coder",
		model:         "openai/gpt-5",
		thinking:      "high",
		contextLength: 24_000,
		contextLimit:  200_000,
		tokens:        24_000,
		cost:          0.05,
		costKnown:     true,
	}
	lines := renderStatus(d, 80)
	assert.Len(t, lines, 2)
	assert.Contains(t, strings.Join(lines, "\n"), "$0.05")
	for _, l := range lines {
		assert.LessOrEqual(t, displayWidth(l), 80)
	}
}

func TestTokenUsageEventAggregatesSessionCost(t *testing.T) {
	t.Parallel()
	m := bareModel(24)

	m.handleEvent(t.Context(), runtime.StreamStarted("root-session", "root"))
	m.handleEvent(t.Context(), runtime.NewTokenUsageEvent("root-session", "root", &runtime.Usage{
		InputTokens:   2_000,
		OutputTokens:  1_000,
		ContextLength: 3_000,
		ContextLimit:  10_000,
		Cost:          0.10,
	}))
	m.handleEvent(t.Context(), runtime.StreamStarted("child-session", "developer"))
	m.handleEvent(t.Context(), runtime.NewTokenUsageEvent("child-session", "developer", &runtime.Usage{
		InputTokens:   800,
		OutputTokens:  200,
		ContextLength: 1_000,
		ContextLimit:  20_000,
		Cost:          0.05,
	}))

	assert.Equal(t, int64(1_000), m.status.tokens)
	assert.InDelta(t, 0.15, m.status.cost, 0.0001)
	assert.True(t, m.status.costKnown)
	assert.Contains(t, strings.Join(renderStatus(m.status, 80), "\n"), "$0.15")

	m.handleEvent(t.Context(), runtime.StreamStopped("child-session", "developer", "normal"))

	assert.Equal(t, int64(3_000), m.status.tokens)
	assert.InDelta(t, 0.15, m.status.cost, 0.0001)
}

func TestTokenUsageBeforeStreamUsesFirstSessionAsRoot(t *testing.T) {
	t.Parallel()
	m := bareModel(24)

	m.handleEvent(t.Context(), runtime.NewTokenUsageEvent("root-session", "root", &runtime.Usage{
		InputTokens:   2_000,
		OutputTokens:  1_000,
		ContextLength: 3_000,
		ContextLimit:  10_000,
		Cost:          0.10,
	}))
	m.handleEvent(t.Context(), runtime.NewTokenUsageEvent("child-session", "developer", &runtime.Usage{
		InputTokens:   800,
		OutputTokens:  200,
		ContextLength: 1_000,
		ContextLimit:  20_000,
		Cost:          0.05,
	}))

	assert.Equal(t, "root-session", m.usage.rootSessionID)
	assert.Equal(t, int64(3_000), m.status.tokens)
	assert.InDelta(t, 0.15, m.status.cost, 0.0001)
}

func TestEmptySessionUsageDoesNotOverrideSessionScopedUsage(t *testing.T) {
	t.Parallel()
	m := bareModel(24)

	m.handleEvent(t.Context(), runtime.NewTokenUsageEvent("root-session", "root", &runtime.Usage{
		InputTokens:   2_000,
		OutputTokens:  1_000,
		ContextLength: 3_000,
		ContextLimit:  10_000,
		Cost:          0.10,
	}))
	m.handleEvent(t.Context(), runtime.NewTokenUsageEvent("", "root", &runtime.Usage{
		InputTokens:   50,
		ContextLength: 50,
		Cost:          0.99,
	}))

	assert.Equal(t, int64(3_000), m.status.tokens)
	assert.InDelta(t, 0.10, m.status.cost, 0.0001)
}
