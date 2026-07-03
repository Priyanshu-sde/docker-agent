package root

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderTags(t *testing.T) {
	t.Parallel()

	// No tags renders nothing.
	assert.Empty(t, renderTags(nil, 40))
	assert.Empty(t, renderTags([]string{"go"}, 0))

	// Tags render as "#tag" chips joined by spaces (ANSI stripped).
	assert.Equal(t, "#go #cli", ansi.Strip(renderTags([]string{"go", "cli"}, 40)))

	// Blank/whitespace tags are skipped.
	assert.Equal(t, "#go", ansi.Strip(renderTags([]string{" ", "go"}, 40)))

	// Chips that don't fit the width are dropped instead of overflowing.
	assert.Equal(t, "#go", ansi.Strip(renderTags([]string{"go", "verylongtag"}, 4)))
}

func TestParseAgentPickerRefs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{"single ref", "coder", []string{"coder"}},
		{"multiple refs", "default,coder", []string{"default", "coder"}},
		{"trims whitespace", " default , coder ", []string{"default", "coder"}},
		{"drops empty entries", "default,,coder,", []string{"default", "coder"}},
		{"external refs", "default,agentcatalog/pirate", []string{"default", "agentcatalog/pirate"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, parseAgentPickerRefs(tt.raw))
		})
	}
}

func TestParseAgentPickerRefsDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows

	// Without ~/.agents, only the built-in agents are offered.
	for _, raw := range []string{"", "   ", ",,,"} {
		assert.Equal(t, []string{"default", "coder"}, parseAgentPickerRefs(raw))
	}

	// Config files in ~/.agents are appended to the built-ins; directories
	// (e.g. skills) and non-config files are ignored.
	agentsDir := filepath.Join(home, ".agents")
	require.NoError(t, os.MkdirAll(filepath.Join(agentsDir, "skills"), 0o755))
	for _, name := range []string{"assistant.yaml", "gopher.yml", "notes.txt"} {
		require.NoError(t, os.WriteFile(filepath.Join(agentsDir, name), nil, 0o644))
	}

	want := []string{
		"default",
		"coder",
		filepath.Join(agentsDir, "assistant.yaml"),
		filepath.Join(agentsDir, "gopher.yml"),
	}
	assert.Equal(t, want, parseAgentPickerRefs(""))

	// An explicit list bypasses the defaults entirely.
	assert.Equal(t, []string{"coder"}, parseAgentPickerRefs("coder"))
}

func TestPrependAgentRef(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"coder"}, prependAgentRef("coder", nil))
	assert.Equal(t, []string{"coder", "hello"}, prependAgentRef("coder", []string{"hello"}))
	assert.Equal(t, []string{"coder", "a", "b"}, prependAgentRef("coder", []string{"a", "b"}))
}

func TestIsLocalConfigRef(t *testing.T) {
	t.Parallel()

	assert.True(t, isLocalConfigRef("/home/user/.agents/assistant.yaml"))
	assert.True(t, isLocalConfigRef("./agent.yml"))
	assert.True(t, isLocalConfigRef("agent.hcl"))
	assert.False(t, isLocalConfigRef("default"))
	assert.False(t, isLocalConfigRef("agentcatalog/pirate"))
	assert.False(t, isLocalConfigRef("https://example.com/agent.yaml"))
}

func TestAgentPickerCardShowsDescriptionForLocalConfigs(t *testing.T) {
	t.Parallel()

	m := newAgentPickerModel(nil)

	// Local config files show the description as the title, with the path
	// demoted to the detail line.
	card := ansi.Strip(m.renderCard(agentChoice{ref: "/tmp/agents/gopher.yaml", description: "Golang expert"}, 70, false))
	assert.Less(t, strings.Index(card, "Golang expert"), strings.Index(card, "/tmp/agents/gopher.yaml"))

	// Without a description the path remains the title.
	card = ansi.Strip(m.renderCard(agentChoice{ref: "/tmp/agents/gopher.yaml"}, 70, false))
	assert.Contains(t, card, "/tmp/agents/gopher.yaml")
	assert.Contains(t, card, "No description available")

	// Non-path refs keep the ref as title with the description below.
	card = ansi.Strip(m.renderCard(agentChoice{ref: "default", description: "A helpful assistant"}, 70, false))
	assert.Less(t, strings.Index(card, "default"), strings.Index(card, "A helpful assistant"))
}

func TestAgentPickerCardShortensHomePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows

	ref := filepath.Join(home, ".agents", "gopher.yaml")
	card := ansi.Strip(newAgentPickerModel(nil).renderCard(agentChoice{ref: ref, description: "Golang expert"}, 70, false))
	assert.Contains(t, card, filepath.Join("~", ".agents", "gopher.yaml"))
	assert.NotContains(t, card, home)
}

func TestTruncateDetail(t *testing.T) {
	t.Parallel()

	// Collapses newlines and runs of whitespace into single spaces.
	assert.Equal(t, "a b c", truncateDetail("a\nb\t  c", 80))
	// Truncates to width with an ellipsis.
	assert.Equal(t, "hel…", truncateDetail("hello world", 4))
	// Empty / whitespace-only input collapses to empty.
	assert.Empty(t, truncateDetail("   \n\t ", 80))
}

func TestAgentPickerRenderNoPanic(t *testing.T) {
	t.Parallel()

	choices := []agentChoice{
		{ref: "default", description: "A helpful AI assistant", tags: []string{"general", "assistant"}, yaml: "agents:\n  root:\n    model: auto\n"},
		{ref: "agentcatalog/some-really-long-agent-reference-name", description: strings.Repeat("very long description ", 20)},
		{ref: "broken", err: errors.New("multi\nline\nerror that is also quite long and should be truncated cleanly")},
	}
	m := newAgentPickerModel(choices)

	// Render across a range of widths, including degenerate ones, to make
	// sure width math never produces a panic or a negative truncation width.
	for _, w := range []int{0, 1, 10, 30, 80, 200} {
		m.width = w
		m.height = 24
		assert.NotPanics(t, func() { _ = m.render() })
		m.openDetails()
		assert.NotPanics(t, func() { _ = m.renderDetails() })
		m.showDetails = false
	}
}

func TestAgentPickerDetailsToggle(t *testing.T) {
	t.Parallel()

	m := newAgentPickerModel([]agentChoice{
		{ref: "default", yaml: "agents:\n  root:\n    model: auto\n"},
	})
	m.width = 80
	m.height = 24

	assert.False(t, m.showDetails)
	m.openDetails()
	assert.True(t, m.showDetails)
	assert.Contains(t, ansi.Strip(m.details.GetContent()), "model: auto")
}

func TestDetailsContent(t *testing.T) {
	t.Parallel()

	m := newAgentPickerModel(nil)
	// YAML is syntax-highlighted, so compare with ANSI stripped.
	assert.Equal(t, "a: b", ansi.Strip(m.detailsContent(agentChoice{yaml: "a: b\n\n"})))
	assert.Contains(t, m.detailsContent(agentChoice{err: errors.New("boom")}), "boom")
	assert.Equal(t, "No configuration available.", m.detailsContent(agentChoice{}))
}

func TestHighlightYAML(t *testing.T) {
	t.Parallel()

	src := "agents:\n  root:\n    model: auto"
	out := highlightYAML(src)
	// Colorized output differs from the input but preserves the text
	// (ignoring any insignificant trailing whitespace per line).
	assert.NotEqual(t, src, out)
	assert.Equal(t, src, trimTrailingPerLine(ansi.Strip(out)))
}

func trimTrailingPerLine(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.Join(lines, "\n")
}

func TestPercentLabel(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "0%", percentLabel(0))
	assert.Equal(t, "50%", percentLabel(0.5))
	assert.Equal(t, "100%", percentLabel(1))
	assert.Equal(t, "0%", percentLabel(-0.5))
	assert.Equal(t, "100%", percentLabel(2))
}

func TestAgentPickerDetailsFixedSize(t *testing.T) {
	t.Parallel()

	// A long YAML so the viewport is scrollable.
	var sb strings.Builder
	for i := range 200 {
		sb.WriteString("line " + strconv.Itoa(i) + "\n")
	}
	m := newAgentPickerModel([]agentChoice{{ref: "default", yaml: sb.String()}})
	m.width = 120
	m.height = 40
	m.openDetails()

	top := m.renderDetails()
	topW, topH := lipgloss.Size(top)

	// Scroll down a few lines and to the bottom; dimensions must not change.
	for range 5 {
		m.details.ScrollDown(1)
		m.syncDetailsBar()
		w, h := lipgloss.Size(m.renderDetails())
		assert.Equal(t, topW, w, "width changed while scrolling")
		assert.Equal(t, topH, h, "height changed while scrolling")
	}

	m.details.GotoBottom()
	m.syncDetailsBar()
	w, h := lipgloss.Size(m.renderDetails())
	assert.Equal(t, topW, w, "width changed at bottom")
	assert.Equal(t, topH, h, "height changed at bottom")
}

func TestStripControl(t *testing.T) {
	t.Parallel()

	// The ESC byte is removed, neutralizing the escape sequence (the
	// remaining "[31m" is harmless literal text). Other control chars go too;
	// newlines are preserved.
	assert.Equal(t, "[31mredtext[0m", stripControl("\x1b[31mredtext\x1b[0m"))
	assert.NotContains(t, stripControl("\x1b[31mredtext\x1b[0m"), "\x1b")
	assert.Equal(t, "ab", stripControl("a\x07b"))
	assert.Equal(t, "line1\nline2", stripControl("line1\nline2"))
	assert.Equal(t, "ab", stripControl("a\x7fb"))
}

func TestSanitizeYAML(t *testing.T) {
	t.Parallel()

	// CRLF/CR normalized to LF, tabs expanded, ESC/control chars stripped.
	assert.Equal(t, "a\nb", sanitizeYAML("a\r\nb"))
	assert.Equal(t, "a\nb", sanitizeYAML("a\rb"))
	assert.Equal(t, "    x", sanitizeYAML("\tx"))
	assert.NotContains(t, sanitizeYAML("key: \x1b[31mvalue\x1b[0m"), "\x1b")
}

func TestHighlightYAMLStripsInjectedEscapes(t *testing.T) {
	t.Parallel()

	// A malicious config can't smuggle its own escape sequences through.
	out := highlightYAML("key: \x1b[31mvalue\x1b[0m\x07")
	plain := ansi.Strip(out)
	assert.NotContains(t, plain, "\x1b")
	assert.NotContains(t, plain, "\x07")
	assert.Contains(t, plain, "value")
}

func TestAgentPickerModelNavigation(t *testing.T) {
	t.Parallel()

	m := newAgentPickerModel([]agentChoice{
		{ref: "default"},
		{ref: "coder"},
	})

	// Up at the top is a no-op.
	m.moveUp()
	assert.Equal(t, 0, m.cursor)

	m.moveDown()
	assert.Equal(t, 1, m.cursor)

	// Down at the bottom is a no-op.
	m.moveDown()
	assert.Equal(t, 1, m.cursor)

	m.moveUp()
	assert.Equal(t, 0, m.cursor)
}

func TestAgentPickerCardAt(t *testing.T) {
	t.Parallel()

	m := newAgentPickerModel([]agentChoice{
		{ref: "default", description: "first"},
		{ref: "coder", description: "second"},
	})
	m.width = 120
	m.height = 40

	// A point far outside the panel hits nothing.
	_, ok := m.cardAt(0, 0)
	assert.False(t, ok)

	// Find the coordinates of each card by scanning the whole grid and
	// checking the reported index is stable and in range.
	seen := map[int]bool{}
	for y := range m.height {
		for x := range m.width {
			if i, ok := m.cardAt(x, y); ok {
				assert.GreaterOrEqual(t, i, 0)
				assert.Less(t, i, len(m.choices))
				seen[i] = true
			}
		}
	}
	// Both cards must be reachable.
	assert.True(t, seen[0])
	assert.True(t, seen[1])
}

func TestAgentPickerMouseHoverSelects(t *testing.T) {
	t.Parallel()

	m := newAgentPickerModel([]agentChoice{
		{ref: "default"},
		{ref: "coder"},
	})
	m.width = 120
	m.height = 40

	x, y := firstCardPoint(t, m, 1)
	_, _ = m.Update(tea.MouseMotionMsg{X: x, Y: y})
	assert.Equal(t, 1, m.cursor, "hover should move the cursor to the hovered card")
}

func TestAgentPickerDoubleClickSelects(t *testing.T) {
	t.Parallel()

	m := newAgentPickerModel([]agentChoice{
		{ref: "default"},
		{ref: "coder"},
	})
	m.width = 120
	m.height = 40

	x, y := firstCardPoint(t, m, 1)
	click := tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft}

	// First click selects (moves cursor) but does not quit.
	_, cmd := m.Update(click)
	assert.Equal(t, 1, m.cursor)
	assert.Nil(t, cmd, "single click must not quit")

	// Second click on the same card within the threshold quits (selects).
	_, cmd = m.Update(click)
	assert.NotNil(t, cmd, "double click must quit")
	assert.IsType(t, tea.QuitMsg{}, cmd())
}

func TestAgentPickerDoubleClickResetsAfterTimeout(t *testing.T) {
	t.Parallel()

	m := newAgentPickerModel([]agentChoice{{ref: "default"}, {ref: "coder"}})
	m.width = 120
	m.height = 40

	x, y := firstCardPoint(t, m, 0)
	click := tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft}

	_, cmd := m.Update(click)
	assert.Nil(t, cmd)

	// Simulate the threshold elapsing: a stale first click can't complete a
	// double-click, so the next click is treated as a fresh first click.
	m.lastClickTime = time.Now().Add(-2 * time.Second)
	_, cmd = m.Update(click)
	assert.Nil(t, cmd, "click after the threshold must not quit")
}

func TestAgentPickerClickOutsideDoesNothing(t *testing.T) {
	t.Parallel()

	m := newAgentPickerModel([]agentChoice{{ref: "default"}, {ref: "coder"}})
	m.width = 120
	m.height = 40

	_, cmd := m.Update(tea.MouseClickMsg{X: 0, Y: 0, Button: tea.MouseLeft})
	assert.Nil(t, cmd)
	assert.Equal(t, -1, m.lastClickIndex, "a miss resets double-click tracking")
}

func TestAgentPickerPanelSizeMatchesRender(t *testing.T) {
	t.Parallel()

	// panelSize must agree with the actual rendered panel; otherwise cardAt's
	// hit zones drift away from what the user sees.
	cases := [][]agentChoice{
		{{ref: "default"}, {ref: "coder"}},
		{{ref: "a", description: "short"}},
		{
			{ref: "default", description: strings.Repeat("long description ", 10)},
			{ref: "agentcatalog/some-really-long-agent-reference-name"},
			{ref: "broken", err: errors.New("boom")},
		},
	}
	for _, choices := range cases {
		m := newAgentPickerModel(choices)
		m.width = 120
		m.height = 40
		gotW, gotH := m.panelSize()
		wantW, wantH := lipgloss.Size(m.render())
		assert.Equal(t, wantW, gotW, "panel width mismatch")
		assert.Equal(t, wantH, gotH, "panel height mismatch")
	}
}

func TestAgentPickerCardAtMatchesRenderedText(t *testing.T) {
	t.Parallel()

	// Independently relate hit-testing to the rendered output: the row where a
	// card's ref text appears (as centered on screen) must hit that card, and
	// the title/help rows must miss.
	m := newAgentPickerModel([]agentChoice{
		{ref: "alpha-agent"},
		{ref: "beta-agent"},
	})
	m.width = 120
	m.height = 40

	screen := ansi.Strip(lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.render()))
	lines := strings.Split(screen, "\n")

	findRow := func(substr string) int {
		for y, line := range lines {
			if strings.Contains(line, substr) {
				return y
			}
		}
		t.Fatalf("%q not found on screen", substr)
		return -1
	}

	for idx, ref := range []string{"alpha-agent", "beta-agent"} {
		y := findRow(ref)
		x := strings.Index(lines[y], ref)
		i, ok := m.cardAt(x, y)
		assert.True(t, ok, "ref row for %q should hit a card", ref)
		assert.Equal(t, idx, i, "ref row for %q should hit card %d", ref, idx)
	}

	// The title and help rows must not resolve to any card.
	titleY := findRow("Choose an agent to run")
	_, ok := m.cardAt(m.width/2, titleY)
	assert.False(t, ok, "title row must not hit a card")

	helpY := findRow("double-click")
	_, ok = m.cardAt(m.width/2, helpY)
	assert.False(t, ok, "help row must not hit a card")
}

func TestAgentPickerDetailsResetsClickTracking(t *testing.T) {
	t.Parallel()

	m := newAgentPickerModel([]agentChoice{
		{ref: "default", yaml: "a: b\n"},
		{ref: "coder", yaml: "c: d\n"},
	})
	m.width = 120
	m.height = 40

	x, y := firstCardPoint(t, m, 0)
	click := tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft}

	// First click primes double-click tracking.
	_, cmd := m.Update(click)
	assert.Nil(t, cmd)

	// Opening then closing the details dialog must clear that state, so the
	// next click can't be paired with the pre-dialog one into a double-click.
	m.openDetails()
	assert.Equal(t, -1, m.lastClickIndex)

	_, _ = m.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	assert.False(t, m.showDetails)
	assert.Equal(t, -1, m.lastClickIndex)

	_, cmd = m.Update(click)
	assert.Nil(t, cmd, "click after closing details must not complete a double-click")
}

func TestAgentPickerWheelIgnoredWithoutDetails(t *testing.T) {
	t.Parallel()

	m := newAgentPickerModel([]agentChoice{{ref: "default"}, {ref: "coder"}})
	m.width = 120
	m.height = 40

	_, cmd := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	assert.Nil(t, cmd, "wheel does nothing while the card list is shown")
	assert.Equal(t, 0, m.cursor)
}

func TestAgentPickerLeanCheckboxDefaultsUnticked(t *testing.T) {
	t.Parallel()

	m := newAgentPickerModel([]agentChoice{{ref: "default"}, {ref: "coder"}})
	m.width = 120
	m.height = 40

	assert.False(t, m.leanMode, "lean mode must be off by default")
	assert.Contains(t, ansi.Strip(m.render()), "[ ] Lean Mode", "checkbox renders unticked by default")
}

func TestAgentPickerLeanCheckboxSeeded(t *testing.T) {
	t.Parallel()

	// When the run would already be lean (--lean or user config), the
	// checkbox must reflect it instead of lying about the run mode.
	m := newAgentPickerModel([]agentChoice{{ref: "default"}, {ref: "coder"}})
	m.leanMode = true
	m.width = 120
	m.height = 40

	assert.Contains(t, ansi.Strip(m.render()), "[x] Lean Mode")
}

func TestAgentPickerLeanCheckboxKeyToggle(t *testing.T) {
	t.Parallel()

	m := newAgentPickerModel([]agentChoice{{ref: "default"}, {ref: "coder"}})
	m.width = 120
	m.height = 40

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'l', Text: "l"})
	assert.Nil(t, cmd)
	assert.True(t, m.leanMode)
	assert.Contains(t, ansi.Strip(m.render()), "[x] Lean Mode")

	_, _ = m.Update(tea.KeyPressMsg{Code: 'l', Text: "l"})
	assert.False(t, m.leanMode)
}

func TestAgentPickerLeanCheckboxClickToggle(t *testing.T) {
	t.Parallel()

	m := newAgentPickerModel([]agentChoice{{ref: "default"}, {ref: "coder"}})
	m.width = 120
	m.height = 40

	// Locate the checkbox on the rendered screen and click it. Note:
	// strings.Index returns a byte offset; convert the prefix to display
	// columns because border runes (│) are multi-byte.
	screen := ansi.Strip(lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.render()))
	lines := strings.Split(screen, "\n")
	var x, y int
	found := false
	for row, line := range lines {
		if prefix, _, ok := strings.Cut(line, "[ ] Lean Mode"); ok {
			x, y, found = lipgloss.Width(prefix), row, true
			break
		}
	}
	assert.True(t, found, "checkbox not found on screen")
	assert.True(t, m.leanCheckboxAt(x, y), "hit zone must match the rendered checkbox")

	// Hit-zone boundaries: both edges are inside; one cell beyond each edge
	// and the adjacent rows are outside.
	checkboxWidth := len("[ ] Lean Mode")
	assert.True(t, m.leanCheckboxAt(x+checkboxWidth-1, y), "right edge must be inside")
	assert.False(t, m.leanCheckboxAt(x-1, y), "left of the checkbox must miss")
	assert.False(t, m.leanCheckboxAt(x+checkboxWidth, y), "right of the checkbox must miss")
	assert.False(t, m.leanCheckboxAt(x, y-1), "row above must miss")
	assert.False(t, m.leanCheckboxAt(x, y+1), "row below must miss")

	click := tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft}
	_, cmd := m.Update(click)
	assert.Nil(t, cmd, "clicking the checkbox must not quit")
	assert.True(t, m.leanMode)

	// A second click within the double-click threshold toggles back rather
	// than being treated as a card double-click.
	_, cmd = m.Update(click)
	assert.Nil(t, cmd)
	assert.False(t, m.leanMode)

	// The checkbox row must not hit any card.
	_, ok := m.cardAt(x, y)
	assert.False(t, ok, "checkbox row must not resolve to a card")
}

// firstCardPoint scans the grid for a coordinate that maps to card index want.
func firstCardPoint(t *testing.T, m *agentPickerModel, want int) (int, int) {
	t.Helper()
	for y := range m.height {
		for x := range m.width {
			if i, ok := m.cardAt(x, y); ok && i == want {
				return x, y
			}
		}
	}
	t.Fatalf("no coordinate maps to card %d", want)
	return 0, 0
}
