package editfile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/tools"
	"github.com/docker/docker-agent/pkg/tui/types"
)

// TestRenderEditFile_EndToEnd writes a temporary source file, builds an
// edit_file tool call against it, and renders both unified and split views.
// The test focuses on structural elements rather than exact escape sequences,
// which depend on the active theme.
func TestRenderEditFile_EndToEnd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")

	updated := `package main

import "fmt"

func main() {
	x := 10
	y := 20
	fmt.Println(x + y)
}
`
	// Simulate the post-execution state: file already contains the new content.
	require.NoError(t, os.WriteFile(path, []byte(updated), 0o644))

	args := map[string]any{
		"path": path,
		"edits": []map[string]string{
			{
				"oldText": "\tx := 1\n\ty := 2",
				"newText": "\tx := 10\n\ty := 20",
			},
		},
	}
	encoded, err := json.Marshal(args)
	require.NoError(t, err)

	toolCall := tools.ToolCall{
		ID: "test-render-1",
		Function: tools.FunctionCall{
			Name:      "edit_file",
			Arguments: string(encoded),
		},
	}

	// Reset cache so the test is hermetic across runs.
	InvalidateCaches()
	t.Cleanup(InvalidateCaches)

	unified := renderEditFile(toolCall, 120, false, types.ToolStatusCompleted)
	split := renderEditFile(toolCall, 120, true, types.ToolStatusCompleted)

	for _, out := range []string{unified, split} {
		assert.NotEmpty(t, out)
		// Source content should appear in the diff regardless of theme escapes.
		assert.True(t, strings.Contains(out, "10") || strings.Contains(out, "20"))
	}

	added, removed := countDiffLines(toolCall, types.ToolStatusCompleted)
	assert.Equal(t, 2, added)
	assert.Equal(t, 2, removed)
}

func TestRenderEditFile_TabIndentedLineDoesNotPanic(t *testing.T) {
	// Regression: tab-indented modified lines used to feed raw (1-byte-tab)
	// text into diffWords while chroma tokens were built from the
	// tab-expanded variant, producing out-of-bounds slice indices in
	// applyWordEmphasis. The fix routes both through prepareContent.
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")

	updated := "package main\n\nfunc main() {\n\tx := 10\n}\n"
	require.NoError(t, os.WriteFile(path, []byte(updated), 0o644))

	args := map[string]any{
		"path": path,
		"edits": []map[string]string{
			{"oldText": "\tx := 1", "newText": "\tx := 10"},
		},
	}
	encoded, _ := json.Marshal(args)
	toolCall := tools.ToolCall{
		ID:       "test-tab-1",
		Function: tools.FunctionCall{Name: "edit_file", Arguments: string(encoded)},
	}

	InvalidateCaches()
	t.Cleanup(InvalidateCaches)

	assert.NotPanics(t, func() {
		_ = renderEditFile(toolCall, 120, false, types.ToolStatusCompleted)
		_ = renderEditFile(toolCall, 120, true, types.ToolStatusCompleted)
	})
}

func TestRenderEditFile_MissingFileReturnsEmptyDiff(t *testing.T) {
	args := map[string]any{
		"path": "/nonexistent/path/that/does/not/exist.go",
		"edits": []map[string]string{
			{"oldText": "a", "newText": "b"},
		},
	}
	encoded, _ := json.Marshal(args)
	toolCall := tools.ToolCall{
		ID:       "test-missing-1",
		Function: tools.FunctionCall{Name: "edit_file", Arguments: string(encoded)},
	}

	InvalidateCaches()
	t.Cleanup(InvalidateCaches)

	// Should not panic on a missing source file.
	assert.NotPanics(t, func() {
		_ = renderEditFile(toolCall, 100, false, types.ToolStatusCompleted)
	})
}
