package editfile

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTokenizeForWordDiff(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"single word", "foo", []string{"foo"}},
		{"two words", "foo bar", []string{"foo", " ", "bar"}},
		{
			name: "function call",
			in:   "fmt.Printf(\"hello\")",
			want: []string{"fmt", ".", "Printf", "(", "\"", "hello", "\"", ")"},
		},
		{
			name: "preserves whitespace runs",
			in:   "  foo   bar",
			want: []string{"  ", "foo", "   ", "bar"},
		},
		{
			name: "punctuation runs split into individual tokens",
			in:   "x++",
			want: []string{"x", "+", "+"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tokenizeForWordDiff(tc.in)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestDiffWords_IdenticalLines(t *testing.T) {
	oldSegs, newSegs := diffWords("foo bar", "foo bar")
	assert.Equal(t, []wordSegment{{Text: "foo bar", Changed: false}}, oldSegs)
	assert.Equal(t, []wordSegment{{Text: "foo bar", Changed: false}}, newSegs)
}

func TestDiffWords_SingleWordChange(t *testing.T) {
	oldSegs, newSegs := diffWords("foo bar baz", "foo qux baz")

	assert.Equal(t, "foo bar baz", concat(oldSegs))
	assert.Equal(t, "foo qux baz", concat(newSegs))

	assert.True(t, hasChange(oldSegs, "bar"))
	assert.True(t, hasChange(newSegs, "qux"))
	assert.True(t, hasUnchanged(oldSegs, "foo "))
	assert.True(t, hasUnchanged(newSegs, " baz"))
}

func TestDiffWords_OneSideEmpty(t *testing.T) {
	oldSegs, newSegs := diffWords("", "added line")
	assert.Empty(t, concat(oldSegs))
	assert.Equal(t, "added line", concat(newSegs))
	assert.True(t, anyChanged(newSegs))
}

func TestDiffWords_PunctuationOnlyChange(t *testing.T) {
	oldSegs, newSegs := diffWords("return err", "return fmt.Errorf(\"%w\", err)")

	assert.Equal(t, "return err", concat(oldSegs))
	assert.Equal(t, "return fmt.Errorf(\"%w\", err)", concat(newSegs))

	// The literal "return" identifier and " err" run should both be reported
	// as unchanged on the new side; only the inserted fmt.Errorf wrapper is
	// flagged as a change.
	assert.True(t, hasUnchanged(newSegs, "return"))
	assert.True(t, hasUnchanged(newSegs, " err"))
	assert.True(t, anyChanged(newSegs))
}

// TestDiffWords_SegmentsReconstructInputs guards against asymmetric LCS gaps:
// the concatenation of the returned segments must equal each side's input,
// otherwise byte offsets fed to applyWordEmphasis would be wrong.
func TestDiffWords_SegmentsReconstructInputs(t *testing.T) {
	cases := []struct {
		old, new string
	}{
		{"foo bar baz", "foo qux baz"},
		{"", "added"},
		{"removed", ""},
		{"a b c d e", "a x y c z e"},
		{"\tx := 1", "\tx := 10"},
		{"func foo() error", "func foo(ctx context.Context) error"},
	}
	for _, tc := range cases {
		oldSegs, newSegs := diffWords(tc.old, tc.new)
		assert.Equal(t, tc.old, concat(oldSegs), "old=%q", tc.old)
		assert.Equal(t, tc.new, concat(newSegs), "new=%q", tc.new)
	}
}

func concat(segs []wordSegment) string {
	var b strings.Builder
	for _, seg := range segs {
		b.WriteString(seg.Text)
	}
	return b.String()
}

func hasChange(segs []wordSegment, text string) bool {
	for _, s := range segs {
		if s.Changed && s.Text == text {
			return true
		}
	}
	return false
}

func hasUnchanged(segs []wordSegment, text string) bool {
	for _, s := range segs {
		if !s.Changed && s.Text == text {
			return true
		}
	}
	return false
}
