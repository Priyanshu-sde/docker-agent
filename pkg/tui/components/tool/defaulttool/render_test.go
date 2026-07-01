package defaulttool

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/tools"
	"github.com/docker/docker-agent/pkg/tui/service"
	"github.com/docker/docker-agent/pkg/tui/types"
)

func TestFormatValue_String(t *testing.T) {
	t.Parallel()

	result := formatValue("hello")
	assert.Equal(t, "hello", result)
}

func TestFormatValue_SingleElementArray(t *testing.T) {
	t.Parallel()

	result := formatValue([]any{"/src/main/cmd/root/run.go"})
	assert.Equal(t, `["/src/main/cmd/root/run.go"]`, result)
}

func TestFormatValue_MultiElementArray(t *testing.T) {
	t.Parallel()

	result := formatValue([]any{"file1.go", "file2.go", "file3.go"})
	expected := `[
  "file1.go",
  "file2.go",
  "file3.go"
]`
	assert.Equal(t, expected, result)
}

func TestFormatValue_EmptyArray(t *testing.T) {
	t.Parallel()

	result := formatValue([]any{})
	assert.Equal(t, "[]", result)
}

func TestFormatValue_Map(t *testing.T) {
	t.Parallel()

	result := formatValue(map[string]any{"key": "value"})
	expected := `{
  "key": "value"
}`
	assert.Equal(t, expected, result)
}

func TestFormatValue_Number(t *testing.T) {
	t.Parallel()

	result := formatValue(42.0)
	assert.Equal(t, "42", result)
}

func TestDecodeArgumentsAcceptsPartialStringValue(t *testing.T) {
	t.Parallel()

	args, err := decodeArguments(`{"path": "/tmp/file", "content": "hello`)
	require.NoError(t, err)
	assert.Equal(t, []kv{
		{Key: "path", Value: "/tmp/file"},
		{Key: "content", Value: "hello"},
	}, args)
}

func TestRendererKeepsLastArgsWhenJSONTemporarilyInvalid(t *testing.T) {
	t.Parallel()

	msg := types.ToolCallMessage("agent", tools.ToolCall{
		ID: "call-1",
		Function: tools.FunctionCall{
			Name:      "Write",
			Arguments: `{"path": "/tmp/file", "content": "hello"`,
		},
	}, tools.Tool{Name: "Write"}, types.ToolStatusPending)

	view := New(msg, service.StaticSessionState{})
	_ = view.SetSize(80, 0)

	first := ansi.Strip(view.View())
	require.Contains(t, first, "content")
	require.Contains(t, first, "hello")

	msg.ToolCall.Function.Arguments += ","
	next := ansi.Strip(view.View())
	assert.Contains(t, next, "content")
	assert.Contains(t, next, "hello")
}
