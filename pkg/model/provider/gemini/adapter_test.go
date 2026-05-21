package gemini

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/genai"

	"github.com/docker/docker-agent/pkg/chat"
)

func TestStreamAdapter_GeminiUsageMetadata(t *testing.T) {
	// Gemini 3 (and any future model that emits usage metadata on its own chunk
	// without accompanying text/tool calls) was previously losing token counts
	// because the stream adapter dropped chunks that lacked text/function calls.
	// These tests pin the fixed behaviour.

	t.Run("forwards chunks containing only UsageMetadata", func(t *testing.T) {
		textChunk := &genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{
				{
					Content: &genai.Content{
						Parts: []*genai.Part{{Text: "Hello"}},
					},
				},
			},
		}
		usageOnlyChunk := &genai.GenerateContentResponse{
			UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
				PromptTokenCount:     14,
				CandidatesTokenCount: 5,
				ThoughtsTokenCount:   3,
			},
		}

		iter := func(fn func(*genai.GenerateContentResponse, error) bool) {
			if !fn(textChunk, nil) {
				return
			}
			fn(usageOnlyChunk, nil)
		}

		adapter := NewStreamAdapter(iter, "test-model", true)

		// First Recv: text chunk, no usage.
		r1, err := adapter.Recv()
		require.NoError(t, err)
		require.Equal(t, "Hello", r1.Choices[0].Delta.Content)
		require.Nil(t, r1.Usage)

		// Second Recv: the usage-only chunk must be forwarded (regression guard).
		r2, err := adapter.Recv()
		require.NoError(t, err)
		require.NotNil(t, r2.Usage, "usage-only chunk must surface token counts")
		require.Equal(t, int64(14), r2.Usage.InputTokens)
		require.Equal(t, int64(8), r2.Usage.OutputTokens) // candidates + thoughts
		require.Equal(t, int64(3), r2.Usage.ReasoningTokens)

		// Final done event closes the stream.
		rdone, err := adapter.Recv()
		require.NoError(t, err)
		require.Equal(t, chat.FinishReasonStop, rdone.Choices[0].FinishReason)
	})

	t.Run("done event carries usage from last response", func(t *testing.T) {
		// When the upstream stream ends with a chunk that carries usage,
		// the synthesised "done" event must propagate that usage so downstream
		// observers receive the final tally.
		chunk := &genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{
				{
					Content: &genai.Content{
						Parts: []*genai.Part{{Text: "ok"}},
					},
				},
			},
			UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
				PromptTokenCount:     10,
				CandidatesTokenCount: 2,
			},
		}

		iter := func(fn func(*genai.GenerateContentResponse, error) bool) {
			fn(chunk, nil)
		}

		adapter := NewStreamAdapter(iter, "test-model", true)

		// Forwarded chunk carries usage.
		r1, err := adapter.Recv()
		require.NoError(t, err)
		require.NotNil(t, r1.Usage)
		require.Equal(t, int64(10), r1.Usage.InputTokens)

		// Done event also exposes the usage from the last response.
		rdone, err := adapter.Recv()
		require.NoError(t, err)
		require.Equal(t, chat.FinishReasonStop, rdone.Choices[0].FinishReason)
		require.NotNil(t, rdone.Usage, "done event should expose usage from last response")
		require.Equal(t, int64(10), rdone.Usage.InputTokens)
		require.Equal(t, int64(2), rdone.Usage.OutputTokens)
	})

	t.Run("trackUsage=false suppresses usage extraction", func(t *testing.T) {
		// When trackUsage is disabled the adapter must not populate Usage even
		// if upstream returns UsageMetadata.
		chunk := &genai.GenerateContentResponse{
			UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
				PromptTokenCount:     10,
				CandidatesTokenCount: 2,
			},
		}
		iter := func(fn func(*genai.GenerateContentResponse, error) bool) {
			fn(chunk, nil)
		}
		adapter := NewStreamAdapter(iter, "test-model", false)

		r1, err := adapter.Recv()
		require.NoError(t, err)
		require.Nil(t, r1.Usage)
	})
}

func TestStreamAdapter_FunctionCalls(t *testing.T) {
	t.Run("function calls in final message", func(t *testing.T) {
		mockResp := &genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{
				{
					Content: &genai.Content{
						Parts: []*genai.Part{
							{
								FunctionCall: &genai.FunctionCall{
									Name: "test_function",
									Args: map[string]any{"param": "value"},
								},
							},
						},
					},
				},
			},
		}

		// Simulate the iterator behavior
		iter := func(fn func(*genai.GenerateContentResponse, error) bool) {
			// Send the response with function call
			fn(mockResp, nil)
		}

		adapter := NewStreamAdapter(iter, "test-model", true)

		// Read the response
		resp, err := adapter.Recv()
		require.NoError(t, err)

		// Should have tool calls
		require.NotEmpty(t, resp.Choices[0].Delta.ToolCalls)

		// Read the final message
		finalResp, err := adapter.Recv()
		require.NoError(t, err)

		// Should have finish reason tool_calls
		require.Equal(t, chat.FinishReasonToolCalls, finalResp.Choices[0].FinishReason)

		// Should NOT include tool calls in final message (to avoid duplication)
		require.Empty(t, finalResp.Choices[0].Delta.ToolCalls)
	})
}
