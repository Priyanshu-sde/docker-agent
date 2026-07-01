//go:build !js && !docker_agent_no_openai

package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/chat"
	"github.com/docker/docker-agent/pkg/config/latest"
	"github.com/docker/docker-agent/pkg/environment"
	"github.com/docker/docker-agent/pkg/tools"
)

// TestDeepSeekProvider_EndToEndRequest drives a real request through the full
// stack (alias resolution -> OpenAI chat-completions client -> HTTP -> SSE
// parsing) against a local server emulating DeepSeek's OpenAI-compatible API.
//
// It proves the deepseek alias is wired correctly without a live key:
//   - the request is authenticated with DEEPSEEK_API_KEY (alias TokenEnvVar),
//   - it is routed to the chat-completions endpoint (alias APIType "openai"),
//   - the configured model is sent verbatim, and
//   - the streamed content is reassembled correctly.
func TestDeepSeekProvider_EndToEndRequest(t *testing.T) {
	t.Parallel()

	const apiKey = "sk-test-deepseek-key"

	var (
		mu               sync.Mutex
		receivedMethod   string
		receivedAuth     string
		receivedPath     string
		receivedModel    string
		receivedMessages string
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		receivedMethod = r.Method
		receivedAuth = r.Header.Get("Authorization")
		receivedPath = r.URL.Path
		mu.Unlock()

		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err == nil {
			mu.Lock()
			if m, ok := payload["model"].(string); ok {
				receivedModel = m
			}
			if msgs, err := json.Marshal(payload["messages"]); err == nil {
				receivedMessages = string(msgs)
			}
			mu.Unlock()
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		for _, delta := range []string{"Hello", " from", " DeepSeek"} {
			writeSSEChunk(w, map[string]any{
				"id": "chatcmpl-test", "object": "chat.completion.chunk", "model": "deepseek-chat",
				"choices": []map[string]any{{"index": 0, "delta": map[string]any{"content": delta}, "finish_reason": nil}},
			})
			flusher.Flush()
		}
		writeSSEChunk(w, map[string]any{
			"id": "chatcmpl-test", "object": "chat.completion.chunk", "model": "deepseek-chat",
			"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
		})
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer server.Close()

	// BaseURL points at the mock server; TokenKey and api_type are left unset so
	// they are filled in from the built-in deepseek alias, exercising the real
	// resolution path.
	modelCfg := &latest.ModelConfig{
		Provider: "deepseek",
		Model:    "deepseek-chat",
		BaseURL:  server.URL,
	}
	env := environment.NewMapEnvProvider(map[string]string{"DEEPSEEK_API_KEY": apiKey})

	provider, err := fullTestRegistry().New(t.Context(), modelCfg, env)
	require.NoError(t, err)

	stream, err := provider.CreateChatCompletionStream(
		t.Context(),
		[]chat.Message{{Role: chat.MessageRoleUser, Content: "Hi"}},
		[]tools.Tool{},
	)
	require.NoError(t, err)
	defer stream.Close()

	content := collectStreamContent(t, stream)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, http.MethodPost, receivedMethod, "chat completions must be sent as a POST")
	assert.Equal(t, "Bearer "+apiKey, receivedAuth, "auth must use the DEEPSEEK_API_KEY from the alias TokenEnvVar")
	assert.Equal(t, "/chat/completions", receivedPath, "deepseek alias must route to the chat-completions endpoint")
	assert.Equal(t, "deepseek-chat", receivedModel, "the configured model must be sent verbatim")
	assert.Contains(t, receivedMessages, `"role":"user"`, "the outgoing request must carry the user message role")
	assert.Contains(t, receivedMessages, "Hi", "the outgoing request must carry the user message content")
	assert.Equal(t, "Hello from DeepSeek", content, "streamed deltas must be reassembled in order")
}

// TestDeepSeekLiveAPI performs a real request against the DeepSeek API. It is
// skipped unless DEEPSEEK_API_KEY is set in the environment, so the default
// test run stays hermetic while allowing an on-demand real check via:
//
//	DEEPSEEK_API_KEY=sk-... go test -run TestDeepSeekLiveAPI ./pkg/model/provider/
func TestDeepSeekLiveAPI(t *testing.T) {
	apiKey := os.Getenv("DEEPSEEK_API_KEY")
	if apiKey == "" {
		t.Skip("DEEPSEEK_API_KEY not set; skipping live DeepSeek API test")
	}

	// No BaseURL/TokenKey: both come from the built-in deepseek alias, so this
	// hits https://api.deepseek.com/v1 for real.
	modelCfg := &latest.ModelConfig{
		Provider: "deepseek",
		Model:    "deepseek-chat",
	}

	provider, err := fullTestRegistry().New(t.Context(), modelCfg, environment.NewOsEnvProvider())
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	stream, err := provider.CreateChatCompletionStream(
		ctx,
		[]chat.Message{{Role: chat.MessageRoleUser, Content: "Reply with the single word: pong"}},
		[]tools.Tool{},
	)
	require.NoError(t, err)
	defer stream.Close()

	content := collectStreamContent(t, stream)
	require.NotEmpty(t, content, "live DeepSeek API must return a non-empty completion")
	t.Logf("DeepSeek live response: %q", content)
}

// collectStreamContent drains a message stream and returns the concatenated
// text of all content deltas.
func collectStreamContent(t *testing.T, stream chat.MessageStream) string {
	t.Helper()

	var b strings.Builder
	for {
		resp, err := stream.Recv()
		for _, choice := range resp.Choices {
			b.WriteString(choice.Delta.Content)
		}
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
	}
	return b.String()
}
