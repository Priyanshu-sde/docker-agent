package sidebar

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/runtime"
	"github.com/docker/docker-agent/pkg/session"
	"github.com/docker/docker-agent/pkg/tui/service"
	"github.com/docker/docker-agent/pkg/tui/styles"
)

// newAgentPanelSidebar builds a sidebar whose current agent is "root" and whose
// roster is set, ready to render the Agents panel at the given outer width.
func newAgentPanelSidebar(t *testing.T, width int, agents ...runtime.AgentDetails) *model {
	t.Helper()
	sess := session.New()
	ss := service.NewSessionState(sess)
	ss.SetCurrentAgentName("root")
	m := New(t.Context(), ss).(*model)
	m.sessionHasContent = true
	m.titleGenerated = true
	m.sessionTitle = "Test"
	m.currentAgent = "root"
	m.availableAgents = agents
	m.width = width
	m.height = 200
	return m
}

// renderAgentPanel returns the ANSI-stripped lines of the Agents panel body.
func renderAgentPanel(m *model) []string {
	out := ansi.Strip(m.agentInfo(m.contentWidth(false)))
	return strings.Split(out, "\n")
}

const tabHeaderLines = 2 // tab title + TabStyle top padding before the body

// agentBody returns the ANSI-stripped panel body lines aligned 1:1 with
// m.agentLineOwners (populated as a side effect of rendering).
func agentBody(m *model) (body []string) {
	lines := renderAgentPanel(m)
	return lines[tabHeaderLines : tabHeaderLines+len(m.agentLineOwners)]
}

// agentLines returns the two ANSI-stripped content lines owned by the named
// agent: line1 (name + thinking + shortcut) and line2 (provider/model).
func agentLines(m *model, name string) (line1, line2 string) {
	body := agentBody(m)
	for j, owner := range m.agentLineOwners {
		if owner == name {
			if j+1 < len(body) {
				return body[j], body[j+1]
			}
			return body[j], ""
		}
	}
	return "", ""
}

func TestClassifyThinking(t *testing.T) {
	t.Parallel()

	cases := []struct {
		label    string
		wantKind thinkingKind
		wantTok  int64
	}{
		{"", thinkingNone, 0},
		{"off", thinkingOff, 0},
		{"adaptive", thinkingAdaptive, 0},
		{"8192", thinkingTokens, 8192},
		{"high", thinkingLevel, 0},
		{"minimal", thinkingLevel, 0},
	}
	for _, c := range cases {
		kind, tok := classifyThinking(c.label)
		assert.Equalf(t, c.wantKind, kind, "kind for %q", c.label)
		assert.Equalf(t, c.wantTok, tok, "tokens for %q", c.label)
	}
}

// TestAgentEntryLayout verifies an agent renders as two lines: line 1 carries
// the name, thinking badge and "^N" shortcut (no description), and line 2 the
// provider/model.
func TestAgentEntryLayout(t *testing.T) {
	t.Parallel()

	m := newAgentPanelSidebar(t, 40,
		runtime.AgentDetails{Name: "root", Provider: "anthropic", Model: "claude-opus-4-8", Description: "Executive assistant", Thinking: "high"},
	)

	line1, line2 := agentLines(m, "root")
	require.NotEmpty(t, line1)
	assert.Contains(t, line1, "root", "line 1 shows the agent name")
	assert.Contains(t, line1, "^1", "line 1 shows the switch shortcut")
	assert.Contains(t, line1, gaugePattern(4), "line 1 shows the thinking gauge")
	assert.NotContains(t, line1, "Executive assistant", "description is not shown")

	body := strings.Join(agentBody(m), "\n")
	assert.NotContains(t, body, "Executive assistant", "description is not shown anywhere")
	assert.Contains(t, line2, "anthropic/claude-opus-4-8", "line 2 shows the provider/model")
}

// TestCurrentAgentMarker verifies the current agent is marked with ▶ while the
// other agents are not, and that each agent owns exactly its two content lines
// (separators are unowned).
func TestCurrentAgentMarker(t *testing.T) {
	t.Parallel()

	m := newAgentPanelSidebar(t, 40,
		runtime.AgentDetails{Name: "first", Provider: "openai", Model: "gpt-5.4-mini", Thinking: "off"},
		runtime.AgentDetails{Name: "root", Provider: "anthropic", Model: "claude-opus-4-8", Thinking: "high"},
		runtime.AgentDetails{Name: "last", Provider: "google", Model: "gemini-flash", Thinking: "off"},
	)

	rootLine1, _ := agentLines(m, "root")
	firstLine1, _ := agentLines(m, "first")
	require.NotEmpty(t, rootLine1)
	require.NotEmpty(t, firstLine1)
	assert.Contains(t, rootLine1, "▶", "current agent is marked with ▶")
	assert.NotContains(t, firstLine1, "▶", "non-current agents have no marker")

	// Each agent owns exactly its two content lines; separators are unowned.
	counts := map[string]int{}
	blanks := 0
	for _, owner := range m.agentLineOwners {
		if owner == "" {
			blanks++
			continue
		}
		counts[owner]++
	}
	assert.Equal(t, 2, counts["first"])
	assert.Equal(t, 2, counts["root"])
	assert.Equal(t, 2, counts["last"])
	assert.Positive(t, blanks, "entries are separated by blank, unowned lines")
}

// TestShortcutAtRightmost verifies the "^N" shortcut is the last visible content
// on the name line: nothing is rendered to the right of it.
func TestShortcutAtRightmost(t *testing.T) {
	t.Parallel()

	m := newAgentPanelSidebar(t, 40,
		runtime.AgentDetails{Name: "root", Provider: "anthropic", Model: "opus", Thinking: "high"},
		runtime.AgentDetails{Name: "alpha", Provider: "openai", Model: "gpt-5.4-mini", Thinking: "8192"},
	)

	for _, name := range []string{"root", "alpha"} {
		line1, _ := agentLines(m, name)
		line := strings.TrimRight(line1, " ")
		require.NotEmpty(t, line)
		assert.Truef(t, strings.HasSuffix(line, "^1") || strings.HasSuffix(line, "^2"),
			"line for %q must end with its shortcut, got %q", name, line)
	}
}

// TestShortcutColumnAlignment verifies the shortcuts align at a single right
// column across name lines regardless of name length or badge width.
func TestShortcutColumnAlignment(t *testing.T) {
	t.Parallel()

	m := newAgentPanelSidebar(t, 40,
		runtime.AgentDetails{Name: "root", Provider: "anthropic", Model: "opus", Thinking: "high"},
		runtime.AgentDetails{Name: "a", Provider: "openai", Model: "gpt-4o", Thinking: "off"},
		runtime.AgentDetails{Name: "longer-name", Provider: "openai", Model: "gpt-5.4", Thinking: "8192"},
	)

	end := -1
	for _, name := range []string{"root", "a", "longer-name"} {
		line1, _ := agentLines(m, name)
		line := strings.TrimRight(line1, " ")
		w := len([]rune(line))
		if end == -1 {
			end = w
		} else {
			assert.Equalf(t, end, w, "shortcuts for %q must end in a single column", name)
		}
	}
}

// TestThinkingBadgeVocabularyOnLine verifies the thinking badge vocabulary
// renders on the agent's name line: effort levels become the gauge, token
// budgets keep the token glyph, adaptive becomes "auto", and off becomes an
// empty gauge.
func TestThinkingBadgeVocabularyOnLine(t *testing.T) {
	t.Parallel()

	m := newAgentPanelSidebar(t, 40,
		runtime.AgentDetails{Name: "root", Provider: "anthropic", Model: "opus", Thinking: "high"},
		runtime.AgentDetails{Name: "alpha", Provider: "openai", Model: "gpt-5.4-mini", Thinking: "off"},
		runtime.AgentDetails{Name: "beta", Provider: "openai", Model: "gpt-5.4", Thinking: "high"},
		runtime.AgentDetails{Name: "gamma", Provider: "openai", Model: "gpt-4o", Thinking: "8192"},
		runtime.AgentDetails{Name: "delta", Provider: "google", Model: "gemini", Thinking: "adaptive"},
	)

	want := map[string]string{
		"alpha": gaugePattern(0),
		"beta":  gaugePattern(4),
		"gamma": styles.TokenGlyph + " 8.2K",
		"delta": "auto",
	}
	for name, badge := range want {
		line1, _ := agentLines(m, name)
		require.NotEmptyf(t, line1, "row for %q should render", name)
		assert.Containsf(t, line1, badge, "row %q should show badge %q", name, badge)
	}
}

// TestModelLineLeftTruncated verifies the provider/model on line 2 keeps its
// informative tail (left-truncation with a leading ellipsis) when it overflows.
func TestModelLineLeftTruncated(t *testing.T) {
	t.Parallel()

	m := newAgentPanelSidebar(t, 28,
		runtime.AgentDetails{Name: "root", Provider: "anthropic", Model: "opus", Thinking: "high"},
		runtime.AgentDetails{Name: "agent2", Provider: "anthropic", Model: "claude-sonnet-4-6", Thinking: "off"},
	)

	_, line2 := agentLines(m, "agent2")
	require.NotEmpty(t, line2)
	assert.Contains(t, line2, "…", "overflowing model is left-truncated with an ellipsis")
	assert.Contains(t, line2, "-4-6", "informative model tail survives left-truncation")
}

// TestMoreThanNineAgentsNoShortcutBeyond9 verifies agents past the 9th get no
// "^N" shortcut hint.
func TestMoreThanNineAgentsNoShortcutBeyond9(t *testing.T) {
	t.Parallel()

	agents := []runtime.AgentDetails{
		{Name: "root", Provider: "anthropic", Model: "opus", Thinking: "high"},
	}
	for i := 2; i <= 12; i++ {
		agents = append(agents, runtime.AgentDetails{
			Name:     "agent" + string(rune('a'+i)),
			Provider: "openai",
			Model:    "gpt-4o",
			Thinking: "off",
		})
	}
	m := newAgentPanelSidebar(t, 40, agents...)

	body := strings.Join(renderAgentPanel(m), "\n")
	assert.Contains(t, body, "^9", "the 9th agent keeps its shortcut")
	assert.NotContains(t, body, "^10", "agents beyond the 9th have no shortcut")
}

// TestNameTruncatesNearMinWidth verifies that at a narrow width the gauge
// collapses to a single cell while the model still occupies line 2.
func TestNameTruncatesNearMinWidth(t *testing.T) {
	t.Parallel()

	m := newAgentPanelSidebar(t, 21,
		runtime.AgentDetails{Name: "root", Provider: "anthropic", Model: "opus", Thinking: "high"},
		runtime.AgentDetails{Name: "agent2", Provider: "anthropic", Model: "claude-sonnet-4-6", Thinking: "high"},
	)

	line1, line2 := agentLines(m, "agent2")
	require.NotEmpty(t, line1)
	// glyph-only step keeps a single filled gauge cell, not the full six-cell gauge.
	assert.NotContains(t, line1, gaugePattern(4), "narrow layout collapses the full gauge")
	assert.Contains(t, line1, styles.GaugeFilled, "narrow layout keeps a single gauge cell")
	assert.Contains(t, line2, "…", "narrow layout left-truncates the model on line 2")
}

// TestClickZonesEveryLine verifies that clicking any rendered agent line (either
// the name line or the model line) resolves to the correct agent.
func TestClickZonesEveryLine(t *testing.T) {
	t.Parallel()

	sess := session.New()
	ss := service.NewSessionState(sess)
	ss.SetCurrentAgentName("root")
	sb := New(t.Context(), ss)
	m := sb.(*model)
	m.sessionHasContent = true
	m.titleGenerated = true
	m.sessionTitle = "Test"
	m.currentAgent = "root"
	m.availableAgents = []runtime.AgentDetails{
		{Name: "first", Provider: "openai", Model: "gpt-5.4-mini", Thinking: "off"},
		{Name: "root", Provider: "anthropic", Model: "claude-opus-4-8", Thinking: "high"},
	}
	m.width = 40
	m.height = 200

	_ = sb.View()

	paddingLeft := m.layoutCfg.PaddingLeft
	foundCurrent := false
	foundOther := false
	for y := range len(m.cachedLines) {
		result, name := sb.HandleClickType(paddingLeft+2, y)
		if result != ClickAgent {
			continue
		}
		if name == "root" {
			foundCurrent = true
		}
		if name == "first" {
			foundOther = true
		}
	}
	assert.True(t, foundCurrent, "clicking the current agent's line switches to it")
	assert.True(t, foundOther, "clicking another agent's line switches to it")
}

// TestRosterSeparatesAgentsWithBlankLine verifies a blank separator line is
// inserted between agent entries and that the separator carries an empty owner,
// so each agent owns exactly its two content lines and click zones stay aligned.
func TestRosterSeparatesAgentsWithBlankLine(t *testing.T) {
	t.Parallel()

	m := newAgentPanelSidebar(t, 40,
		runtime.AgentDetails{Name: "root", Provider: "anthropic", Model: "opus", Thinking: "high"},
		runtime.AgentDetails{Name: "alpha", Provider: "openai", Model: "gpt-5.4-mini", Thinking: "off"},
		runtime.AgentDetails{Name: "beta", Provider: "openai", Model: "gpt-5.4", Thinking: "high"},
	)

	_ = renderAgentPanel(m) // populates agentLineOwners

	counts := map[string]int{}
	blanks := 0
	for _, owner := range m.agentLineOwners {
		if owner == "" {
			blanks++
			continue
		}
		counts[owner]++
	}
	assert.Equal(t, 2, counts["root"], "an agent owns exactly its two content lines, not the separator")
	assert.Equal(t, 2, counts["alpha"])
	assert.Equal(t, 2, counts["beta"])
	assert.Positive(t, blanks, "agents are separated by blank, unowned lines")

	// The panel does not start with a separator, and a blank separator precedes
	// the alpha entry.
	require.NotEmpty(t, m.agentLineOwners)
	assert.NotEmpty(t, m.agentLineOwners[0], "the panel does not start with a separator")
	alphaStart := -1
	for i, owner := range m.agentLineOwners {
		if owner == "alpha" {
			alphaStart = i
			break
		}
	}
	require.Positive(t, alphaStart, "alpha should own lines after root")
	assert.Empty(t, m.agentLineOwners[alphaStart-1], "a blank separator precedes the alpha entry")
}
