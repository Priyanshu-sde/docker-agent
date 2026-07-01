//go:build !js && !docker_agent_no_openai

package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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

// TestCerebrasProvider_EndToEndRequest drives a real request through the full
// stack (alias resolution -> OpenAI chat-completions client -> HTTP -> SSE
// parsing) against a local server emulating Cerebras's OpenAI-compatible API.
//
// It proves the cerebras alias is wired correctly without a live key:
//   - the request is authenticated with CEREBRAS_API_KEY (alias TokenEnvVar),
//   - it is routed to the chat-completions endpoint (alias APIType "openai"),
//   - the configured model is sent verbatim, and
//   - the streamed content is reassembled correctly.
func TestCerebrasProvider_EndToEndRequest(t *testing.T) {
	t.Parallel()

	const apiKey = "csk-test-cerebras-key"

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

		for _, delta := range []string{"Hello", " from", " Cerebras"} {
			writeSSEChunk(w, map[string]any{
				"id": "chatcmpl-test", "object": "chat.completion.chunk", "model": "gpt-oss-120b",
				"choices": []map[string]any{{"index": 0, "delta": map[string]any{"content": delta}, "finish_reason": nil}},
			})
			flusher.Flush()
		}
		writeSSEChunk(w, map[string]any{
			"id": "chatcmpl-test", "object": "chat.completion.chunk", "model": "gpt-oss-120b",
			"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
		})
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer server.Close()

	// BaseURL points at the mock server; TokenKey and api_type are left unset so
	// they are filled in from the built-in cerebras alias, exercising the real
	// resolution path.
	modelCfg := &latest.ModelConfig{
		Provider: "cerebras",
		Model:    "gpt-oss-120b",
		BaseURL:  server.URL,
	}
	env := environment.NewMapEnvProvider(map[string]string{"CEREBRAS_API_KEY": apiKey})

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
	assert.Equal(t, "Bearer "+apiKey, receivedAuth, "auth must use the CEREBRAS_API_KEY from the alias TokenEnvVar")
	assert.Equal(t, "/chat/completions", receivedPath, "cerebras alias must route to the chat-completions endpoint")
	assert.Equal(t, "gpt-oss-120b", receivedModel, "the configured model must be sent verbatim")
	assert.Contains(t, receivedMessages, `"role":"user"`, "the outgoing request must carry the user message role")
	assert.Contains(t, receivedMessages, "Hi", "the outgoing request must carry the user message content")
	assert.Equal(t, "Hello from Cerebras", content, "streamed deltas must be reassembled in order")
}

// TestCerebrasLiveAPI performs a real request against the Cerebras API. It is
// skipped unless CEREBRAS_API_KEY is set in the environment, so the default
// test run stays hermetic while allowing an on-demand real check via:
//
//	CEREBRAS_API_KEY=csk-... go test -run TestCerebrasLiveAPI ./pkg/model/provider/
func TestCerebrasLiveAPI(t *testing.T) {
	apiKey := os.Getenv("CEREBRAS_API_KEY")
	if apiKey == "" {
		t.Skip("CEREBRAS_API_KEY not set; skipping live Cerebras API test")
	}

	// No BaseURL/TokenKey: both come from the built-in cerebras alias, so this
	// hits https://api.cerebras.ai/v1 for real.
	modelCfg := &latest.ModelConfig{
		Provider: "cerebras",
		Model:    "gpt-oss-120b",
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
	require.NotEmpty(t, content, "live Cerebras API must return a non-empty completion")
	t.Logf("Cerebras live response: %q", content)
}
