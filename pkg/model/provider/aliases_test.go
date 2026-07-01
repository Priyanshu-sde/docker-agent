package provider

import (
	"maps"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLookupAlias(t *testing.T) {
	t.Parallel()

	// Every entry in the table is reachable.
	for name, expected := range Aliases {
		got, ok := LookupAlias(name)
		assert.True(t, ok, "alias %q should be found", name)
		assert.Equal(t, expected, got)
	}

	// Unknown name yields the zero Alias and false.
	got, ok := LookupAlias("does-not-exist")
	assert.False(t, ok)
	assert.Equal(t, Alias{}, got)

	// Lookup is case-sensitive (callers normalise themselves).
	if _, ok := LookupAlias("MISTRAL"); ok {
		t.Errorf("LookupAlias should be case-sensitive")
	}
}

func TestOpenRouterAlias(t *testing.T) {
	t.Parallel()

	alias, ok := LookupAlias("openrouter")
	require.True(t, ok)
	assert.Equal(t, Alias{
		APIType:     "openai",
		BaseURL:     "https://openrouter.ai/api/v1",
		TokenEnvVar: "OPENROUTER_API_KEY",
	}, alias)
	assert.True(t, IsKnownProvider("openrouter"))
	assert.True(t, IsCatalogProvider("openrouter"))
}

func TestBasetenAlias(t *testing.T) {
	t.Parallel()

	alias, ok := LookupAlias("baseten")
	require.True(t, ok)
	assert.Equal(t, Alias{
		APIType:     "openai",
		BaseURL:     "https://inference.baseten.co/v1",
		TokenEnvVar: "BASETEN_API_KEY",
	}, alias)
	assert.True(t, IsKnownProvider("baseten"))
	assert.True(t, IsCatalogProvider("baseten"))
}

func TestOVHcloudAlias(t *testing.T) {
	t.Parallel()

	alias, ok := LookupAlias("ovhcloud")
	require.True(t, ok)
	assert.Equal(t, Alias{
		APIType:     "openai",
		BaseURL:     "https://oai.endpoints.kepler.ai.cloud.ovh.net/v1",
		TokenEnvVar: "OVH_AI_ENDPOINTS_ACCESS_TOKEN",
	}, alias)
	assert.True(t, IsKnownProvider("ovhcloud"))
	assert.True(t, IsCatalogProvider("ovhcloud"))
}

func TestEachAlias(t *testing.T) {
	t.Parallel()

	// Iterator yields every entry exactly once.
	collected := maps.Collect(EachAlias())
	assert.Equal(t, Aliases, collected)
}

func TestEachAlias_EarlyTermination(t *testing.T) {
	t.Parallel()

	// Iterator must respect a false return from the yield function.
	require.NotEmpty(t, Aliases, "test requires the alias table to be non-empty")

	count := 0
	for range EachAlias() {
		count++
		if count == 1 {
			break
		}
	}
	assert.Equal(t, 1, count, "iteration should stop when consumer breaks out")
}
