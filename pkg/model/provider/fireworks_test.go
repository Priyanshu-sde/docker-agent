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

// TestFireworksProvider_EndToEndRequest drives a real request through the full
// stack (alias resolution -> OpenAI chat-completions client -> HTTP -> SSE
// parsing) against a local server emulating Fireworks AI's OpenAI-compatible
// API.
//
// It proves the fireworks alias is wired correctly without a live key:
//   - the request is authenticated with FIREWORKS_API_KEY (alias TokenEnvVar),
//   - it is routed to the chat-completions endpoint (alias APIType "openai"),
//   - the configured model is sent verbatim,
//   - the streamed content is reassembled correctly, and
//   - because Fireworks fronts open-weight models with strict chat templates,
//     the per-source system messages are coalesced into a single leading one
//     (open-model-host merge, issue #3344).
func TestFireworksProvider_EndToEndRequest(t *testing.T) {
	t.Parallel()

	const apiKey = "fw-test-fireworks-key"

	var (
		mu               sync.Mutex
		receivedMethod   string
		receivedAuth     string
		receivedPath     string
		receivedModel    string
		receivedMessages string
		systemCount      int
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		receivedMethod = r.Method
		receivedAuth = r.Header.Get("Authorization")
		receivedPath = r.URL.Path
		mu.Unlock()

		var payload struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err == nil {
			count := 0
			for _, m := range payload.Messages {
				if m.Role == "system" {
					count++
				}
			}
			msgs, _ := json.Marshal(payload.Messages)
			mu.Lock()
			receivedModel = payload.Model
			receivedMessages = string(msgs)
			systemCount = count
			mu.Unlock()
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		for _, delta := range []string{"Hello", " from", " Fireworks"} {
			writeSSEChunk(w, map[string]any{
				"id": "chatcmpl-test", "object": "chat.completion.chunk", "model": "accounts/fireworks/models/kimi-k2-instruct",
				"choices": []map[string]any{{"index": 0, "delta": map[string]any{"content": delta}, "finish_reason": nil}},
			})
			flusher.Flush()
		}
		writeSSEChunk(w, map[string]any{
			"id": "chatcmpl-test", "object": "chat.completion.chunk", "model": "accounts/fireworks/models/kimi-k2-instruct",
			"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
		})
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer server.Close()

	// BaseURL points at the mock server; TokenKey and api_type are left unset so
	// they are filled in from the built-in fireworks alias, exercising the real
	// resolution path.
	modelCfg := &latest.ModelConfig{
		Provider: "fireworks",
		Model:    "accounts/fireworks/models/kimi-k2-instruct",
		BaseURL:  server.URL,
	}
	env := environment.NewMapEnvProvider(map[string]string{"FIREWORKS_API_KEY": apiKey})

	provider, err := fullTestRegistry().New(t.Context(), modelCfg, env)
	require.NoError(t, err)

	// Two system messages (agent instruction + toolset instruction) plus a user
	// turn: exactly the shape docker-agent builds for an agent with a toolset.
	stream, err := provider.CreateChatCompletionStream(
		t.Context(),
		[]chat.Message{
			{Role: chat.MessageRoleSystem, Content: "AGENT-INSTRUCTION: you are helpful."},
			{Role: chat.MessageRoleSystem, Content: "TOOLSET-INSTRUCTION: use tools wisely."},
			{Role: chat.MessageRoleUser, Content: "PING-MARKER"},
		},
		[]tools.Tool{},
	)
	require.NoError(t, err)
	defer stream.Close()

	content := collectStreamContent(t, stream)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, http.MethodPost, receivedMethod, "chat completions must be sent as a POST")
	assert.Equal(t, "Bearer "+apiKey, receivedAuth, "auth must use the FIREWORKS_API_KEY from the alias TokenEnvVar")
	assert.Equal(t, "/chat/completions", receivedPath, "fireworks alias must route to the chat-completions endpoint")
	assert.Equal(t, "accounts/fireworks/models/kimi-k2-instruct", receivedModel, "the configured model must be sent verbatim")
	assert.Equal(t, 1, systemCount, "fireworks is an open-model host: consecutive system messages must be coalesced into one (issue #3344)")
	assert.Contains(t, receivedMessages, "AGENT-INSTRUCTION", "the coalesced system message must retain the agent instruction")
	assert.Contains(t, receivedMessages, "TOOLSET-INSTRUCTION", "the coalesced system message must retain the toolset instruction")
	assert.Contains(t, receivedMessages, "PING-MARKER", "the outgoing request must carry the user message content")
	assert.Equal(t, "Hello from Fireworks", content, "streamed deltas must be reassembled in order")
}

// TestFireworksLiveAPI performs a real request against the Fireworks AI API. It
// is skipped unless FIREWORKS_API_KEY is set in the environment, so the default
// test run stays hermetic while allowing an on-demand real check via:
//
//	FIREWORKS_API_KEY=fw-... go test -run TestFireworksLiveAPI ./pkg/model/provider/
func TestFireworksLiveAPI(t *testing.T) {
	apiKey := os.Getenv("FIREWORKS_API_KEY")
	if apiKey == "" {
		t.Skip("FIREWORKS_API_KEY not set; skipping live Fireworks AI API test")
	}

	// No BaseURL/TokenKey: both come from the built-in fireworks alias, so this
	// hits https://api.fireworks.ai/inference/v1 for real.
	modelCfg := &latest.ModelConfig{
		Provider: "fireworks",
		Model:    "accounts/fireworks/models/kimi-k2-instruct",
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
	require.NotEmpty(t, content, "live Fireworks AI API must return a non-empty completion")
	t.Logf("Fireworks live response: %q", content)
}
