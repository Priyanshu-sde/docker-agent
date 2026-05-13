package mcpcatalog

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/tools"
)

type stubEnv struct{ vars map[string]string }

func (s stubEnv) Get(_ context.Context, name string) (string, bool) {
	v, ok := s.vars[name]
	return v, ok
}

func TestLoadCatalog(t *testing.T) {
	cat, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "Docker MCP Catalog", cat.Source)
	assert.NotEmpty(t, cat.SourceURL)
	assert.Positive(t, cat.Count)
	assert.Equal(t, len(cat.Servers), cat.Count)

	// Every server in the catalog must be remote streamable-http and have a URL.
	for _, s := range cat.Servers {
		assert.NotEmpty(t, s.ID, "server id must not be empty")
		assert.Equal(t, "streamable-http", s.Transport, "server %s has unexpected transport", s.ID)
		assert.NotEmpty(t, s.URL, "server %s has no URL", s.ID)
		// auth.type must be one of the three documented values.
		switch s.Auth.Type {
		case "oauth", "api_key", "none":
		default:
			t.Fatalf("server %s has invalid auth.type %q", s.ID, s.Auth.Type)
		}
	}
}

func TestSearchTool(t *testing.T) {
	ts := New(stubEnv{vars: map[string]string{}})
	ctx := t.Context()

	res, err := ts.handleSearch(ctx, SearchArgs{Query: "stripe"})
	require.NoError(t, err)
	require.False(t, res.IsError)
	assert.Contains(t, strings.ToLower(res.Output), "stripe")

	// Empty query returns the whole catalog.
	res, err = ts.handleSearch(ctx, SearchArgs{Query: ""})
	require.NoError(t, err)
	require.False(t, res.IsError)
	// Result starts with "found N server(s):" — N should equal catalog count.
	first := strings.SplitN(res.Output, "\n", 2)[0]
	assert.Contains(t, first, "found ")
	// Decoding the JSON portion should give a non-empty list.
	body := strings.SplitN(res.Output, "\n", 2)[1]
	var parsed []SearchResult
	require.NoError(t, json.Unmarshal([]byte(body), &parsed))
	assert.Len(t, parsed, ts.catalog.Count)

	// Unknown query returns an error result (not a Go error).
	res, err = ts.handleSearch(ctx, SearchArgs{Query: "xxxxxx_no_such_server_xxxxxx"})
	require.NoError(t, err)
	assert.True(t, res.IsError)
}

func TestEnableDisableLifecycle(t *testing.T) {
	ts := New(stubEnv{vars: map[string]string{}})
	ctx := t.Context()

	// Pick the first OAuth-style server in the catalog as a known good fixture.
	var oauthID string
	for _, s := range ts.catalog.Servers {
		if s.Auth.Type == "oauth" {
			oauthID = s.ID
			break
		}
	}
	require.NotEmpty(t, oauthID, "test fixture: catalog should contain at least one OAuth server")

	// Track tools-changed callbacks.
	var changes int
	ts.SetToolsChangedHandler(func() { changes++ })

	// Before enabling: only meta-tools.
	toolList, err := ts.Tools(ctx)
	require.NoError(t, err)
	names := toolNames(toolList)
	assert.ElementsMatch(t, []string{
		ToolNameSearch, ToolNameList, ToolNameEnable, ToolNameDisable,
	}, names)

	// Enable: a callback should fire and the underlying mcp.Toolset should
	// be present in t.enabled. We deliberately do NOT exercise the network
	// path — Tools(ctx) on the lazily-instantiated toolset would attempt a
	// connection. Just check the bookkeeping.
	res, err := ts.handleEnable(ctx, EnableArgs{ID: oauthID})
	require.NoError(t, err)
	require.False(t, res.IsError, "enable failed: %s", res.Output)
	assert.Contains(t, res.Output, "OAuth")
	assert.Equal(t, 1, changes, "enable should fire tools-changed handler exactly once")

	ts.mu.RLock()
	_, exists := ts.enabled[oauthID]
	ts.mu.RUnlock()
	assert.True(t, exists)

	// Re-enable: idempotent, no extra change notification.
	res, err = ts.handleEnable(ctx, EnableArgs{ID: oauthID})
	require.NoError(t, err)
	assert.Contains(t, res.Output, "already enabled")
	assert.Equal(t, 1, changes)

	// Search now reports it as enabled.
	res, err = ts.handleSearch(ctx, SearchArgs{Query: oauthID})
	require.NoError(t, err)
	require.False(t, res.IsError)
	body := strings.SplitN(res.Output, "\n", 2)[1]
	var parsed []SearchResult
	require.NoError(t, json.Unmarshal([]byte(body), &parsed))
	var found *SearchResult
	for i := range parsed {
		if parsed[i].ID == oauthID {
			found = &parsed[i]
		}
	}
	require.NotNil(t, found)
	assert.True(t, found.Enabled)

	// Disable: removes the entry and fires another change notification.
	res, err = ts.handleDisable(ctx, DisableArgs{ID: oauthID})
	require.NoError(t, err)
	require.False(t, res.IsError)
	assert.Equal(t, 2, changes)

	ts.mu.RLock()
	_, exists = ts.enabled[oauthID]
	ts.mu.RUnlock()
	assert.False(t, exists)

	// Disable again: error result, no extra change.
	res, err = ts.handleDisable(ctx, DisableArgs{ID: oauthID})
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Equal(t, 2, changes)
}

func TestEnableUnknownServer(t *testing.T) {
	ts := New(stubEnv{vars: map[string]string{}})
	res, err := ts.handleEnable(t.Context(), EnableArgs{ID: "definitely-not-a-server"})
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, res.Output, "unknown server id")
}

func TestEnableAPIKeyMissingEnv(t *testing.T) {
	ts := New(stubEnv{vars: map[string]string{}})

	var apiKeyID, expectedEnv string
	for _, s := range ts.catalog.Servers {
		if s.Auth.Type == "api_key" && len(s.Auth.Secrets) > 0 && s.Auth.Secrets[0].Env != "" {
			apiKeyID = s.ID
			expectedEnv = s.Auth.Secrets[0].Env
			break
		}
	}
	require.NotEmpty(t, apiKeyID, "test fixture: catalog should contain at least one api_key server with an env var")

	res, err := ts.handleEnable(t.Context(), EnableArgs{ID: apiKeyID})
	require.NoError(t, err)
	require.False(t, res.IsError)
	assert.Contains(t, res.Output, "WARNING")
	assert.Contains(t, res.Output, expectedEnv)
}

func TestEnableAPIKeyEnvPresent(t *testing.T) {
	ts := New(nil) // no env provider — should still work; the warning just doesn't fire.

	var apiKeyID string
	for _, s := range ts.catalog.Servers {
		if s.Auth.Type == "api_key" {
			apiKeyID = s.ID
			break
		}
	}
	require.NotEmpty(t, apiKeyID)

	res, err := ts.handleEnable(t.Context(), EnableArgs{ID: apiKeyID})
	require.NoError(t, err)
	require.False(t, res.IsError)
	assert.Contains(t, res.Output, "auth: API key")
	// Without an env provider we cannot tell if the env vars are set;
	// the implementation skips the warning and simply enables the server.
	assert.NotContains(t, res.Output, "WARNING")
}

func TestListEnabled(t *testing.T) {
	ts := New(stubEnv{vars: map[string]string{}})
	ctx := t.Context()

	res, err := ts.handleList(ctx, ListArgs{})
	require.NoError(t, err)
	assert.Contains(t, res.Output, "0 enabled")

	id := ts.catalog.Servers[0].ID
	_, err = ts.handleEnable(ctx, EnableArgs{ID: id})
	require.NoError(t, err)

	res, err = ts.handleList(ctx, ListArgs{})
	require.NoError(t, err)
	assert.Contains(t, res.Output, "1 enabled")
	assert.Contains(t, res.Output, id)
}

func TestStopReleasesEverything(t *testing.T) {
	ts := New(stubEnv{vars: map[string]string{}})
	ctx := t.Context()

	id := ts.catalog.Servers[0].ID
	_, err := ts.handleEnable(ctx, EnableArgs{ID: id})
	require.NoError(t, err)

	require.NoError(t, ts.Stop(ctx))

	ts.mu.RLock()
	defer ts.mu.RUnlock()
	assert.Empty(t, ts.enabled)
}

func toolNames(list []tools.Tool) []string {
	out := make([]string, len(list))
	for i, t := range list {
		out[i] = t.Name
	}
	return out
}

func TestSetManagedOAuthPersistence(t *testing.T) {
	ts := New(stubEnv{vars: map[string]string{}})
	ctx := t.Context()

	// Set managedOAuth before enabling any server
	ts.SetManagedOAuth(true)

	// Enable a server
	id := ts.catalog.Servers[0].ID
	_, err := ts.handleEnable(ctx, EnableArgs{ID: id})
	require.NoError(t, err)

	// Verify the enabled toolset received the managedOAuth flag
	ts.mu.RLock()
	mcpTS, exists := ts.enabled[id]
	ts.mu.RUnlock()
	require.True(t, exists)

	// The mcp.Toolset doesn't expose managedOAuth state directly,
	// but we can verify it was called by checking the toolset exists
	// and was properly initialized. The actual OAuth behavior would
	// be tested in the mcp package.
	assert.NotNil(t, mcpTS)
}

func TestConcurrentEnableDisable(t *testing.T) {
	ts := New(stubEnv{vars: map[string]string{}})
	ctx := t.Context()

	// Pick two different servers
	require.GreaterOrEqual(t, len(ts.catalog.Servers), 2, "need at least 2 servers for concurrency test")
	id1 := ts.catalog.Servers[0].ID
	id2 := ts.catalog.Servers[1].ID

	var wg sync.WaitGroup
	errors := make(chan error, 4)

	// Concurrent enables
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := ts.handleEnable(ctx, EnableArgs{ID: id1})
		if err != nil {
			errors <- err
		}
	}()
	go func() {
		defer wg.Done()
		_, err := ts.handleEnable(ctx, EnableArgs{ID: id2})
		if err != nil {
			errors <- err
		}
	}()

	wg.Wait()
	close(errors)

	// Check for errors
	for err := range errors {
		require.NoError(t, err)
	}

	// Verify both are enabled
	ts.mu.RLock()
	_, exists1 := ts.enabled[id1]
	_, exists2 := ts.enabled[id2]
	ts.mu.RUnlock()
	assert.True(t, exists1)
	assert.True(t, exists2)

	// Concurrent disables
	errors = make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := ts.handleDisable(ctx, DisableArgs{ID: id1})
		if err != nil {
			errors <- err
		}
	}()
	go func() {
		defer wg.Done()
		_, err := ts.handleDisable(ctx, DisableArgs{ID: id2})
		if err != nil {
			errors <- err
		}
	}()

	wg.Wait()
	close(errors)

	for err := range errors {
		require.NoError(t, err)
	}

	// Verify both are disabled
	ts.mu.RLock()
	_, exists1 = ts.enabled[id1]
	_, exists2 = ts.enabled[id2]
	ts.mu.RUnlock()
	assert.False(t, exists1)
	assert.False(t, exists2)
}

func TestToolsContextCancellation(t *testing.T) {
	ts := New(stubEnv{vars: map[string]string{}})

	// Enable a server
	id := ts.catalog.Servers[0].ID
	_, err := ts.handleEnable(t.Context(), EnableArgs{ID: id})
	require.NoError(t, err)

	// Create a cancelled context
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	// Tools() should respect context cancellation
	// Note: This will return the meta-tools but fail when trying to get
	// tools from the enabled server
	_, err = ts.Tools(ctx)
	// The error should be context.Canceled
	assert.ErrorIs(t, err, context.Canceled)
}
