package openai

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/chat"
	"github.com/docker/docker-agent/pkg/config/latest"
	"github.com/docker/docker-agent/pkg/environment"
)

// TestChatCompletions_MergesConsecutiveSystemMessages is a regression test for
// https://github.com/docker/docker-agent/issues/3145. docker-agent emits a
// separate system message per source (the agent instruction plus each toolset's
// instructions). Some OpenAI-compatible backends (e.g. OVHcloud's Qwen3.5)
// silently return an empty stream when a request carries more than one system
// message, which surfaces as an agent that "does nothing". The chat-completions
// path must coalesce consecutive system messages into one before sending,
// matching the DMR client.
func TestChatCompletions_MergesConsecutiveSystemMessages(t *testing.T) {
	t.Parallel()

	assertMergesConsecutiveMessages(t, &latest.ModelConfig{
		Provider:     "custom",
		Model:        "qwen3",
		TokenKey:     "MY_TOKEN",
		ProviderOpts: map[string]any{"api_type": "openai_chatcompletions"},
	})
}

func TestBaseten_MergesConsecutiveSystemMessages(t *testing.T) {
	t.Parallel()

	assertMergesConsecutiveMessages(t, &latest.ModelConfig{
		Provider: "baseten",
		Model:    "zai-org/GLM-5.2",
		TokenKey: "MY_TOKEN",
	})
}

func TestOVHcloud_MergesConsecutiveSystemMessages(t *testing.T) {
	t.Parallel()

	assertMergesConsecutiveMessages(t, &latest.ModelConfig{
		Provider: "ovhcloud",
		Model:    "Qwen3.5-397B-A17B",
		TokenKey: "MY_TOKEN",
	})
}

func assertMergesConsecutiveMessages(t *testing.T, cfg *latest.ModelConfig) {
	t.Helper()

	var (
		body []byte
		mu   sync.Mutex
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		body = b
		mu.Unlock()
		writeSSEResponse(w)
	}))
	defer server.Close()

	requestCfg := *cfg
	requestCfg.BaseURL = server.URL
	env := environment.NewMapEnvProvider(map[string]string{"MY_TOKEN": "secret"})

	client, err := NewClient(t.Context(), &requestCfg, env)
	require.NoError(t, err)

	stream, err := client.CreateChatCompletionStream(
		t.Context(),
		[]chat.Message{
			{Role: chat.MessageRoleSystem, Content: "You are a helpful assistant."},
			{Role: chat.MessageRoleSystem, Content: "## Filesystem Tools\n\n- Relative paths resolve from the working directory"},
			{Role: chat.MessageRoleUser, Content: "List the files."},
		},
		nil,
	)
	require.NoError(t, err)
	defer stream.Close()
	for {
		if _, err := stream.Recv(); err != nil {
			break
		}
	}

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, body, "chat/completions should have been called")

	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(body, &req))

	var systemCount int
	var systemContent string
	for _, m := range req.Messages {
		if m.Role == "system" {
			systemCount++
			if s, ok := m.Content.(string); ok {
				systemContent = s
			}
		}
	}

	assert.Equal(t, 1, systemCount, "consecutive system messages must be coalesced into one (see #3145)")
	// Both original system contents must survive in the merged message.
	assert.Contains(t, systemContent, "You are a helpful assistant.")
	assert.Contains(t, systemContent, "Filesystem Tools")
}
