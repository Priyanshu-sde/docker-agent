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
	"github.com/docker/docker-agent/pkg/environment"
	"github.com/docker/docker-agent/pkg/modelsdev"
)

// catalogOnlyModel is a text model present only in the in-memory test catalog
// (not in config.DefaultModels). Asserting it surfaces proves the command
// actually reads the injected models.dev store rather than relying solely on
// the per-provider defaults, which would be added even with an empty catalog.
const catalogOnlyModel = "claude-catalog-only"

// testCatalog is a tiny in-memory models.dev database used by the models-list
// tests so they never fetch the real catalog over the network or read the
// developer's on-disk cache.
func testCatalog() *modelsdev.Database {
	return &modelsdev.Database{
		Providers: map[string]modelsdev.Provider{
			"anthropic": {Models: map[string]modelsdev.Model{
				"claude-sonnet-4-6": {
					Name:       "Claude Sonnet 4.6",
					Modalities: modelsdev.Modalities{Output: []string{"text"}},
				},
				catalogOnlyModel: {
					Name:       "Claude Catalog Only",
					Modalities: modelsdev.Modalities{Output: []string{"text"}},
				},
			}},
			"openai": {Models: map[string]modelsdev.Model{
				"gpt-5": {
					Name:       "GPT-5",
					Modalities: modelsdev.Modalities{Output: []string{"text"}},
				},
			}},
		},
	}
}

// withTestConfig injects a hermetic env provider and an in-memory models.dev
// store into the models command. It keeps listing side-effect-free: without it
// the real env provider chain shells out to the OS keychain / pass / 1Password
// for every missing API key and the store fetches https://models.dev, making
// the tests slow and non-parallelizable.
func withTestConfig(env map[string]string) modelsCmdOption {
	return func(rc *config.RuntimeConfig) {
		rc.EnvProviderForTests = environment.NewMapEnvProvider(env)
		rc.ModelsDevStoreOverride = modelsdev.NewDatabaseStore(testCatalog())
	}
}

func TestModelsListCommand_DefaultOutput(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	cmd := newModelsCmd(withTestConfig(map[string]string{"ANTHROPIC_API_KEY": "test-key"}))
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(nil)

	require.NoError(t, cmd.Execute())

	output := buf.String()
	assert.Contains(t, output, "PROVIDER")
	assert.Contains(t, output, "MODEL")
	assert.Contains(t, output, "anthropic")
	// A catalog-only model must appear, proving the injected store was read.
	assert.Contains(t, output, catalogOnlyModel)
}

func TestModelsListCommand_ProviderFilter(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	cmd := newModelsCmd(withTestConfig(map[string]string{
		"ANTHROPIC_API_KEY": "test-key",
		"OPENAI_API_KEY":    "test-key",
	}))
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--provider", "anthropic"})

	require.NoError(t, cmd.Execute())

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
	t.Parallel()

	var buf bytes.Buffer
	cmd := newModelsCmd(withTestConfig(map[string]string{"ANTHROPIC_API_KEY": "test-key"}))
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--format", "json"})

	require.NoError(t, cmd.Execute())

	var rows []modelRow
	require.NoError(t, json.Unmarshal(buf.Bytes(), &rows))
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
	t.Parallel()

	env := map[string]string{"ANTHROPIC_API_KEY": "test-key"}

	var buf bytes.Buffer
	cmd := newModelsCmd(withTestConfig(env))
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--format", "json"})

	require.NoError(t, cmd.Execute())

	var rows []modelRow
	require.NoError(t, json.Unmarshal(buf.Bytes(), &rows))

	// Exactly one row should be marked default, and it must be the
	// auto-selected model for this environment.
	autoModel := config.AutoModelConfig(t.Context(), "", environment.NewMapEnvProvider(env), nil, nil)
	var defaults []modelRow
	for _, r := range rows {
		if r.Default {
			defaults = append(defaults, r)
		}
	}
	require.Len(t, defaults, 1, "expected exactly one default model")
	assert.Equal(t, autoModel.Provider, defaults[0].Provider)
	assert.Equal(t, autoModel.Model, defaults[0].Model)
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
	assert.False(t, called.Load(), "server must not be reached with an already-canceled context")
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
	t.Parallel()

	// No provider keys — only DMR should remain as fallback.
	var buf bytes.Buffer
	cmd := newModelsCmd(withTestConfig(map[string]string{}))
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(nil)

	require.NoError(t, cmd.Execute())

	// DMR is always available as fallback
	assert.Contains(t, buf.String(), "dmr")
}
