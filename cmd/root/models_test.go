package root

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/config"
	"github.com/docker/docker-agent/pkg/paths"
)

func TestModelsListCommand_DefaultOutput(t *testing.T) {
	// With ANTHROPIC_API_KEY set, the default output should include
	// at least the anthropic default model.
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("DOCKER_AGENT_MODELS_GATEWAY", "")
	t.Setenv("DOCKER_AGENT_DEFAULT_MODEL", "")

	// Point at an empty config dir so userconfig.Load returns an empty
	// config without reading the developer's real ~/.config/cagent.
	paths.SetConfigDir(t.TempDir())
	t.Cleanup(func() { paths.SetConfigDir("") })

	var buf bytes.Buffer
	cmd := newModelsCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(nil)

	err := cmd.Execute()
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "PROVIDER")
	assert.Contains(t, output, "MODEL")
	assert.Contains(t, output, "anthropic")
}

func TestModelsListCommand_ProviderFilter(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("DOCKER_AGENT_MODELS_GATEWAY", "")
	t.Setenv("DOCKER_AGENT_DEFAULT_MODEL", "")

	// Point at an empty config dir so userconfig.Load returns an empty
	// config without reading the developer's real ~/.config/cagent.
	paths.SetConfigDir(t.TempDir())
	t.Cleanup(func() { paths.SetConfigDir("") })

	var buf bytes.Buffer
	cmd := newModelsCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--provider", "anthropic"})

	err := cmd.Execute()
	require.NoError(t, err)

	output := buf.String()
	// Every non-header line should be anthropic
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "PROVIDER") {
			continue
		}
		assert.True(t, strings.HasPrefix(line, "anthropic"),
			"expected anthropic provider, got: %s", line)
	}
}

func TestModelsListCommand_JSONFormat(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("DOCKER_AGENT_MODELS_GATEWAY", "")
	t.Setenv("DOCKER_AGENT_DEFAULT_MODEL", "")

	// Point at an empty config dir so userconfig.Load returns an empty
	// config without reading the developer's real ~/.config/cagent.
	paths.SetConfigDir(t.TempDir())
	t.Cleanup(func() { paths.SetConfigDir("") })

	var buf bytes.Buffer
	cmd := newModelsCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--format", "json"})

	err := cmd.Execute()
	require.NoError(t, err)

	var rows []modelRow
	err = json.Unmarshal(buf.Bytes(), &rows)
	require.NoError(t, err)
	assert.NotEmpty(t, rows)

	// At least one should be the default
	hasDefault := false
	for _, r := range rows {
		if r.Default {
			hasDefault = true
			break
		}
	}
	assert.True(t, hasDefault, "expected at least one default model")
}

func TestModelsListCommand_DefaultMarker(t *testing.T) {
	// When a default model is configured via env, it should be marked.
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("DOCKER_AGENT_MODELS_GATEWAY", "")
	t.Setenv("DOCKER_AGENT_DEFAULT_MODEL", "")

	// Point at an empty config dir so userconfig.Load returns an empty
	// config without reading the developer's real ~/.config/cagent.
	paths.SetConfigDir(t.TempDir())
	t.Cleanup(func() { paths.SetConfigDir("") })

	var buf bytes.Buffer
	cmd := newModelsCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--format", "json"})

	err := cmd.Execute()
	require.NoError(t, err)

	var rows []modelRow
	require.NoError(t, json.Unmarshal(buf.Bytes(), &rows))

	// The auto-selected model should be marked as default
	rc := config.RuntimeConfig{}
	autoModel := config.AutoModelConfig(t.Context(), "", rc.EnvProvider(), nil, nil)
	for _, r := range rows {
		if r.Provider == autoModel.Provider && r.Model == autoModel.Model {
			assert.True(t, r.Default, "auto-selected model %s/%s should be marked as default", r.Provider, r.Model)
		}
	}
}

func TestFetchModelsFromURL_Success(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/models", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"object":"list","data":[
			{"id":"model-a","object":"model"},
			{"id":"model-b","object":"model"},
			{"id":"model-c","object":"model"}
		]}`))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)

	models := fetchModelsFromURL(t.Context(), server.URL+"/v1/models", server.Client())
	assert.Equal(t, []string{"model-a", "model-b", "model-c"}, models)
}

func TestFetchModelsFromURL_Non200(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	models := fetchModelsFromURL(t.Context(), server.URL+"/v1/models", server.Client())
	assert.Empty(t, models)
}

func TestFetchModelsFromURL_Status500(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	models := fetchModelsFromURL(t.Context(), server.URL+"/v1/models", server.Client())
	assert.Empty(t, models)
}

func TestFetchModelsFromURL_MalformedJSON(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`not json`))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)

	models := fetchModelsFromURL(t.Context(), server.URL+"/v1/models", server.Client())
	assert.Empty(t, models)
}

func TestFetchModelsFromURL_EmptyBody(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
	}))
	t.Cleanup(server.Close)

	models := fetchModelsFromURL(t.Context(), server.URL+"/v1/models", server.Client())
	assert.Empty(t, models)
}

func TestFetchModelsFromURL_EmptyDataArray(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"object":"list","data":[]}`))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)

	models := fetchModelsFromURL(t.Context(), server.URL+"/v1/models", server.Client())
	assert.Empty(t, models)
}

func TestFetchModelsFromURL_DuplicateIDs(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"object":"list","data":[
			{"id":"dup"},
			{"id":"dup"},
			{"id":"unique"}
		]}`))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)

	models := fetchModelsFromURL(t.Context(), server.URL+"/v1/models", server.Client())
	assert.Equal(t, []string{"dup", "dup", "unique"}, models)
}

func TestFetchModelsFromURL_EmptyIDs(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"object":"list","data":[
			{"id":""},
			{"id":"valid"},
			{"id":""}
		]}`))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)

	models := fetchModelsFromURL(t.Context(), server.URL+"/v1/models", server.Client())
	assert.Equal(t, []string{"valid"}, models)
}

func TestFetchModelsFromURL_ContextCanceled(t *testing.T) {
	t.Parallel()

	var called atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Store(true)
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	models := fetchModelsFromURL(ctx, server.URL+"/v1/models", server.Client())
	assert.Empty(t, models)
}

func TestFetchModelsFromURL_SkipsEmbeddingModels(t *testing.T) {
	// The function passes all model IDs through; embedding filtering
	// is done at the caller level (collectModels). Verify IDs are intact.
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"object":"list","data":[
			{"id":"text-embedding-3"},
			{"id":"gpt-5"}
		]}`))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)

	models := fetchModelsFromURL(t.Context(), server.URL+"/v1/models", server.Client())
	assert.Equal(t, []string{"text-embedding-3", "gpt-5"}, models)
}

func TestModelsListCommand_NoCredentials(t *testing.T) {
	// Clear all provider keys — only DMR should remain as fallback.
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("MISTRAL_API_KEY", "")
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_ROLE_ARN", "")
	t.Setenv("DOCKER_AGENT_MODELS_GATEWAY", "")
	t.Setenv("DOCKER_AGENT_DEFAULT_MODEL", "")

	// Point at an empty config dir so userconfig.Load returns an empty
	// config without reading the developer's real ~/.config/cagent.
	paths.SetConfigDir(t.TempDir())
	t.Cleanup(func() { paths.SetConfigDir("") })

	var buf bytes.Buffer
	cmd := newModelsCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(nil)

	err := cmd.Execute()
	require.NoError(t, err)

	output := buf.String()
	// DMR is always available as fallback
	assert.Contains(t, output, "dmr")
}
