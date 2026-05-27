package tools

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/docker/aijson"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewHandler_WithArguments(t *testing.T) {
	type Args struct {
		Name string `json:"name"`
	}

	handler := NewHandler(func(_ context.Context, args Args) (*ToolCallResult, error) {
		return ResultSuccess("hello " + args.Name), nil
	})

	result, err := handler(t.Context(), ToolCall{
		ID:   "call_1",
		Type: "function",
		Function: FunctionCall{
			Name:      "greet",
			Arguments: `{"name":"world"}`,
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "hello world", result.Output)
}

func TestNewHandler_EmptyArguments(t *testing.T) {
	handler := NewHandler(func(_ context.Context, _ map[string]any) (*ToolCallResult, error) {
		return ResultSuccess("ok"), nil
	})

	result, err := handler(t.Context(), ToolCall{
		ID:   "call_1",
		Type: "function",
		Function: FunctionCall{
			Name:      "get_memories",
			Arguments: "",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "ok", result.Output)
}

func TestNewHandler_EmptyObjectArguments(t *testing.T) {
	handler := NewHandler(func(_ context.Context, _ map[string]any) (*ToolCallResult, error) {
		return ResultSuccess("ok"), nil
	})

	result, err := handler(t.Context(), ToolCall{
		ID:   "call_1",
		Type: "function",
		Function: FunctionCall{
			Name:      "list_things",
			Arguments: "{}",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "ok", result.Output)
}

func TestNewHandler_InvalidArguments(t *testing.T) {
	handler := NewHandler(func(_ context.Context, _ map[string]any) (*ToolCallResult, error) {
		return ResultSuccess("ok"), nil
	})

	_, err := handler(t.Context(), ToolCall{
		ID:   "call_1",
		Type: "function",
		Function: FunctionCall{
			Name:      "broken",
			Arguments: `{"unterminated`,
		},
	})
	require.Error(t, err)
}

// The next three tests pin the docker-agent-specific behavior of NewHandler.
// Repair semantics themselves are covered by github.com/docker/aijson; what
// is local to this package is (a) that NewHandler actually delegates to
// aijson, and (b) that the OnRepair hook fans out to the tool_input_repaired
// slog event with the expected fields (and stays quiet on the hot path).

// TestNewHandler_DelegatesToAijson is the wiring canary: if someone swaps
// aijson.Unmarshal back to encoding/json.Unmarshal, this test catches it.
func TestNewHandler_DelegatesToAijson(t *testing.T) {
	type args struct {
		Paths []string `json:"paths"`
	}
	var got args
	handler := NewHandler(func(_ context.Context, a args) (*ToolCallResult, error) {
		got = a
		return ResultSuccess("ok"), nil
	})

	_, err := handler(t.Context(), ToolCall{
		Type: "function",
		Function: FunctionCall{
			Name:      "read_multiple_files",
			Arguments: `{"paths":"only.txt"}`,
		},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"only.txt"}, got.Paths)
}

func TestNewHandler_EmitsRepairTelemetry(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	type args struct {
		Paths []string `json:"paths"`
	}
	handler := NewHandler(func(_ context.Context, _ args) (*ToolCallResult, error) {
		return ResultSuccess("ok"), nil
	})

	_, err := handler(t.Context(), ToolCall{
		Type: "function",
		Function: FunctionCall{
			Name:      "read_multiple_files",
			Arguments: `{"paths":"only.txt"}`,
		},
	})
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "tool_input_repaired")
	assert.Contains(t, out, "tool=read_multiple_files")
	// Track the exported aijson constant rather than the underlying string
	// so the assertion follows the library if it ever renames the value.
	assert.Contains(t, out, string(aijson.KindWrapInArray))
}

// TestNewHandler_NoTelemetryOnValidInput pins the hot-path contract: a
// well-formed tool call must NOT emit tool_input_repaired. Without this,
// a regression in aijson's strict-first ordering would silently flood logs.
func TestNewHandler_NoTelemetryOnValidInput(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	type args struct {
		Paths []string `json:"paths"`
	}
	handler := NewHandler(func(_ context.Context, _ args) (*ToolCallResult, error) {
		return ResultSuccess("ok"), nil
	})

	_, err := handler(t.Context(), ToolCall{
		Type: "function",
		Function: FunctionCall{
			Name:      "read_multiple_files",
			Arguments: `{"paths":["a.txt","b.txt"]}`,
		},
	})
	require.NoError(t, err)
	assert.NotContains(t, buf.String(), "tool_input_repaired")
}

func TestToolCallResultWithoutPayload(t *testing.T) {
	result := &ToolCallResult{
		Output:            "large output",
		IsError:           true,
		Meta:              "metadata",
		Images:            []MediaContent{{Data: "image", MimeType: "image/png"}},
		Audios:            []MediaContent{{Data: "audio", MimeType: "audio/wav"}},
		StructuredContent: map[string]any{"key": "value"},
	}

	slim := result.WithoutPayload()

	require.NotNil(t, slim)
	assert.Empty(t, slim.Output)
	assert.True(t, slim.IsError)
	assert.Equal(t, "metadata", slim.Meta)
	assert.Nil(t, slim.Images)
	assert.Nil(t, slim.Audios)
	assert.Nil(t, slim.StructuredContent)
}
