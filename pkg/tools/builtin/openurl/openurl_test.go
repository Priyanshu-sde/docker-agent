package openurl

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/config"
	"github.com/docker/docker-agent/pkg/config/latest"
	"github.com/docker/docker-agent/pkg/environment"
	"github.com/docker/docker-agent/pkg/js"
	"github.com/docker/docker-agent/pkg/tools"
)

func TestOpenURL_Opens(t *testing.T) {
	t.Parallel()

	var opened string
	tool := New("https://example.com/dashboard", WithOpener(func(_ context.Context, url string) error {
		opened = url
		return nil
	}))

	result, err := tool.callTool(t.Context(), struct{}{})
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Equal(t, "https://example.com/dashboard", opened)
	assert.Contains(t, result.Output, "https://example.com/dashboard")
}

func TestOpenURL_OpenerError(t *testing.T) {
	t.Parallel()

	tool := New("https://example.com", WithOpener(func(context.Context, string) error {
		return errors.New("boom")
	}))

	result, err := tool.callTool(t.Context(), struct{}{})
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, result.Output, "boom")
}

func TestOpenURL_ExpandsEnv(t *testing.T) {
	t.Parallel()

	expander := js.NewJsExpander(environment.NewMapEnvProvider(map[string]string{
		"DOCS_VERSION": "v2",
	}))

	var opened string
	tool := New("https://docs.example.com/${env.DOCS_VERSION}",
		WithExpander(expander),
		WithOpener(func(_ context.Context, url string) error {
			opened = url
			return nil
		}),
	)

	_, err := tool.callTool(t.Context(), struct{}{})
	require.NoError(t, err)
	assert.Equal(t, "https://docs.example.com/v2", opened)
}

func TestOpenURL_CustomName(t *testing.T) {
	t.Parallel()

	tool := New("https://example.com", WithName("open_dashboard"))
	toolsList, err := tool.Tools(t.Context())
	require.NoError(t, err)
	require.Len(t, toolsList, 1)
	assert.Equal(t, "open_dashboard", toolsList[0].Name)
}

func TestOpenURL_DefaultName(t *testing.T) {
	t.Parallel()

	tool := New("https://example.com")
	toolsList, err := tool.Tools(t.Context())
	require.NoError(t, err)
	require.Len(t, toolsList, 1)
	assert.Equal(t, ToolNameOpenURL, toolsList[0].Name)
}

// TestOpenURL_OpenerNotCanceled guards against a regression where the
// request-scoped context was forwarded straight to the launcher: a canceled
// turn would then kill the fire-and-forget open/xdg-open/rundll32 process
// before the OS finished handing off to the browser. The handler must strip
// cancellation before calling the opener.
func TestOpenURL_OpenerNotCanceled(t *testing.T) {
	t.Parallel()

	canceledCtx, cancel := context.WithCancel(t.Context())
	cancel()

	var sawErr error
	tool := New("https://example.com", WithOpener(func(ctx context.Context, _ string) error {
		sawErr = ctx.Err()
		return nil
	}))

	result, err := tool.callTool(canceledCtx, struct{}{})
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.NoError(t, sawErr, "opener must receive a non-canceled context")
}

func TestCreateToolSet_RequiresURL(t *testing.T) {
	t.Parallel()

	_, err := CreateToolSet(latest.Toolset{Type: "open_url"}, &config.RuntimeConfig{})
	require.Error(t, err)
}

func TestCreateToolSet_OK(t *testing.T) {
	t.Parallel()

	ts, err := CreateToolSet(latest.Toolset{
		Type: "open_url",
		URL:  "https://example.com",
	}, &config.RuntimeConfig{})
	require.NoError(t, err)

	toolsList, err := ts.Tools(t.Context())
	require.NoError(t, err)
	require.Len(t, toolsList, 1)

	_, ok := ts.(tools.Instructable)
	assert.True(t, ok)
}
