package root

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
)

func TestParseAgentPickerRefs(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{"empty defaults", "", []string{"default", "coder"}},
		{"whitespace defaults", "   ", []string{"default", "coder"}},
		{"single ref", "coder", []string{"coder"}},
		{"multiple refs", "default,coder", []string{"default", "coder"}},
		{"trims whitespace", " default , coder ", []string{"default", "coder"}},
		{"drops empty entries", "default,,coder,", []string{"default", "coder"}},
		{"only commas defaults", ",,,", []string{"default", "coder"}},
		{"external refs", "default,agentcatalog/pirate", []string{"default", "agentcatalog/pirate"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, parseAgentPickerRefs(tt.raw))
		})
	}
}

func TestPrependAgentRef(t *testing.T) {
	assert.Equal(t, []string{"coder"}, prependAgentRef("coder", nil))
	assert.Equal(t, []string{"coder", "hello"}, prependAgentRef("coder", []string{"hello"}))
	assert.Equal(t, []string{"coder", "a", "b"}, prependAgentRef("coder", []string{"a", "b"}))
}

func TestTruncateDetail(t *testing.T) {
	// Collapses newlines and runs of whitespace into single spaces.
	assert.Equal(t, "a b c", truncateDetail("a\nb\t  c", 80))
	// Truncates to width with an ellipsis.
	assert.Equal(t, "hel…", truncateDetail("hello world", 4))
	// Empty / whitespace-only input collapses to empty.
	assert.Empty(t, truncateDetail("   \n\t ", 80))
}

func TestAgentPickerRenderNoPanic(t *testing.T) {
	choices := []agentChoice{
		{ref: "default", description: "A helpful AI assistant", yaml: "agents:\n  root:\n    model: auto\n"},
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
	m := newAgentPickerModel(nil)
	// YAML is syntax-highlighted, so compare with ANSI stripped.
	assert.Equal(t, "a: b", ansi.Strip(m.detailsContent(agentChoice{yaml: "a: b\n\n"})))
	assert.Contains(t, m.detailsContent(agentChoice{err: errors.New("boom")}), "boom")
	assert.Equal(t, "No configuration available.", m.detailsContent(agentChoice{}))
}

func TestHighlightYAML(t *testing.T) {
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
	assert.Equal(t, "0%", percentLabel(0))
	assert.Equal(t, "50%", percentLabel(0.5))
	assert.Equal(t, "100%", percentLabel(1))
	assert.Equal(t, "0%", percentLabel(-0.5))
	assert.Equal(t, "100%", percentLabel(2))
}

func TestAgentPickerDetailsFixedSize(t *testing.T) {
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
	// CRLF/CR normalized to LF, tabs expanded, ESC/control chars stripped.
	assert.Equal(t, "a\nb", sanitizeYAML("a\r\nb"))
	assert.Equal(t, "a\nb", sanitizeYAML("a\rb"))
	assert.Equal(t, "    x", sanitizeYAML("\tx"))
	assert.NotContains(t, sanitizeYAML("key: \x1b[31mvalue\x1b[0m"), "\x1b")
}

func TestHighlightYAMLStripsInjectedEscapes(t *testing.T) {
	// A malicious config can't smuggle its own escape sequences through.
	out := highlightYAML("key: \x1b[31mvalue\x1b[0m\x07")
	plain := ansi.Strip(out)
	assert.NotContains(t, plain, "\x1b")
	assert.NotContains(t, plain, "\x07")
	assert.Contains(t, plain, "value")
}

func TestAgentPickerModelNavigation(t *testing.T) {
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
