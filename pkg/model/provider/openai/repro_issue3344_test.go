package openai

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/chat"
	"github.com/docker/docker-agent/pkg/config/latest"
	"github.com/docker/docker-agent/pkg/environment"
)

// TestReproIssue3344_QwenViaVLLM reproduces
// https://github.com/docker/docker-agent/issues/3344.
//
// docker-agent emits one system message per source: the agent instruction plus
// each toolset's instructions (see session.go buildInvariantSystemMessages).
// When such a request is sent to a Qwen 3.5/3.6 model served by vLLM, the
// model's Jinja chat template rejects any system message that is not the very
// first message with:
//
//	HTTP 400: System message must be at the beginning.
//
// (see the unsloth Qwen3.5 chat_template.jinja, and issue #2327).
//
// The chat-completions path already coalesces consecutive system messages, but
// only for a hard-coded allow-list (explicit api_type=openai_chatcompletions,
// baseten, ovhcloud). Users who point docker-agent at a self-hosted vLLM server
// with the intuitive `provider: openai` + `base_url` form, or through an
// OpenAI-compatible alias (openrouter, nebius, ...), bypass the merge and hit
// the error. These are exactly the configs exercised below: each must send a
// single system message so the vLLM/Qwen template accepts the request.
func TestReproIssue3344_QwenViaVLLM(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		cfg  *latest.ModelConfig
	}{
		{
			// The config reported in #3344 / #2327: point the built-in openai
			// provider at a self-hosted vLLM endpoint via base_url.
			name: "provider_openai_with_custom_base_url",
			cfg: &latest.ModelConfig{
				Provider: "openai",
				Model:    "Qwen/Qwen3.6-35B",
				TokenKey: "MY_TOKEN",
			},
		},
		{
			// OpenAI-compatible aliases (here OpenRouter, which serves Qwen)
			// resolve to api_type "openai" rather than "openai_chatcompletions",
			// so they hit the same gap.
			name: "openai_compatible_alias_openrouter",
			cfg: &latest.ModelConfig{
				Provider: "openrouter",
				Model:    "qwen/qwen3.6-35b",
				TokenKey: "MY_TOKEN",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertVLLMQwenAcceptsRequest(t, tc.cfg)
		})
	}
}

// assertVLLMQwenAcceptsRequest stands up a fake vLLM server that enforces the
// Qwen chat-template rule ("system message must be at the beginning") and
// asserts that docker-agent's request is accepted, i.e. that it carries a
// single, leading system message.
func assertVLLMQwenAcceptsRequest(t *testing.T, cfg *latest.ModelConfig) {
	t.Helper()

	var (
		systemCount int
		mu          sync.Mutex
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		var req struct {
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(body, &req)

		count := 0
		violation := false
		for i, m := range req.Messages {
			if m.Role == "system" {
				count++
				if i != 0 {
					// Mimic the Qwen 3.5/3.6 Jinja template served by vLLM.
					violation = true
				}
			}
		}

		mu.Lock()
		systemCount = count
		mu.Unlock()

		if violation {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{
					"message": "System message must be at the beginning.",
					"type":    "BadRequestError",
					"code":    400,
				},
			})
			return
		}

		writeSSEResponse(w)
	}))
	defer server.Close()

	requestCfg := *cfg
	requestCfg.BaseURL = server.URL
	env := environment.NewMapEnvProvider(map[string]string{"MY_TOKEN": "secret"})

	client, err := NewClient(t.Context(), &requestCfg, env)
	require.NoError(t, err)

	// The message list docker-agent builds for an agent with a toolset: one
	// system message for the agent instruction, one for the toolset's
	// instructions, then the user turn.
	stream, err := client.CreateChatCompletionStream(
		t.Context(),
		[]chat.Message{
			{Role: chat.MessageRoleSystem, Content: "You are a helpful coding assistant."},
			{Role: chat.MessageRoleSystem, Content: "## Filesystem Tools\n\n- Relative paths resolve from the working directory"},
			{Role: chat.MessageRoleUser, Content: "List the files."},
		},
		nil,
	)
	require.NoError(t, err)
	defer stream.Close()

	var streamErr error
	for {
		if _, err := stream.Recv(); err != nil {
			if !errors.Is(err, io.EOF) {
				streamErr = err
			}
			break
		}
	}

	mu.Lock()
	defer mu.Unlock()

	if streamErr != nil && strings.Contains(streamErr.Error(), "System message must be at the beginning") {
		t.Fatalf("vLLM/Qwen rejected the request: docker-agent sent %d system messages instead of 1 (issue #3344): %v", systemCount, streamErr)
	}
	require.NoError(t, streamErr)
	assert.Equal(t, 1, systemCount, "docker-agent must coalesce its system messages into a single leading one for vLLM/Qwen (issue #3344)")
}

// TestShouldMergeConsecutiveMessages_Gating documents which endpoints coalesce
// consecutive system messages. Self-hosted OpenAI-compatible servers and
// open-model host aliases (which may front strict-template models like Qwen)
// merge; first-party APIs with a fixed model lineup (official OpenAI, Mistral,
// xAI, ...) tolerate multiple system messages and are left untouched.
func TestShouldMergeConsecutiveMessages_Gating(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  *latest.ModelConfig
		want bool
	}{
		{"nil config", nil, false},
		{"official openai, no base_url", &latest.ModelConfig{Provider: "openai", Model: "gpt-4o"}, false},
		{"openai with custom base_url (vLLM)", &latest.ModelConfig{Provider: "openai", Model: "Qwen/Qwen3.6-35B", BaseURL: "http://box:8000/v1"}, true},
		{"open-model host alias openrouter", &latest.ModelConfig{Provider: "openrouter", Model: "qwen/qwen3.6-35b"}, true},
		{"open-model host alias nebius", &latest.ModelConfig{Provider: "nebius", Model: "Qwen/Qwen3"}, true},
		{"baseten", &latest.ModelConfig{Provider: "baseten", Model: "zai-org/GLM-5.2"}, true},
		{"ovhcloud", &latest.ModelConfig{Provider: "ovhcloud", Model: "Qwen3.5-397B-A17B"}, true},
		{"open-model host alias cerebras", &latest.ModelConfig{Provider: "cerebras", Model: "qwen-3-coder-480b"}, true},
		{"open-model host fireworks", &latest.ModelConfig{Provider: "fireworks", Model: "accounts/fireworks/models/kimi-k2-instruct"}, true},
		{"explicit api_type openai_chatcompletions", &latest.ModelConfig{Provider: "custom", Model: "qwen3", ProviderOpts: map[string]any{"api_type": "openai_chatcompletions"}}, true},
		// First-party APIs with fixed model lineups: unchanged (no merge).
		{"first-party mistral", &latest.ModelConfig{Provider: "mistral", Model: "mistral-small"}, false},
		{"first-party xai", &latest.ModelConfig{Provider: "xai", Model: "grok-4"}, false},
		{"first-party deepseek", &latest.ModelConfig{Provider: "deepseek", Model: "deepseek-chat"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, shouldMergeConsecutiveMessages(tt.cfg))
		})
	}
}
