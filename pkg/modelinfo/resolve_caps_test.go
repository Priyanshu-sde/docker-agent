package modelinfo

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/modelsdev"
)

// visionStore returns a store that catalogues a single vision+pdf model under
// openai/gpt-4o and nothing else.
func visionStore() *modelsdev.Store {
	return modelsdev.NewDatabaseStore(&modelsdev.Database{Providers: map[string]modelsdev.Provider{
		"openai": {Models: map[string]modelsdev.Model{
			"gpt-4o": {Modalities: modelsdev.Modalities{Input: []string{"text", "image", "pdf"}}},
		}},
	}})
}

func TestResolveCaps_OverrideWins(t *testing.T) {
	t.Parallel()

	// An override is authoritative even when the store would say otherwise (or
	// is nil): no models.dev lookup happens.
	cases := []struct {
		name      string
		override  *CapsOverride
		store     *modelsdev.Store
		id        modelsdev.ID
		wantImage bool
		wantPDF   bool
	}{
		{
			name:      "image only, uncatalogued provider",
			override:  &CapsOverride{Image: true},
			store:     visionStore(),
			id:        modelsdev.NewID("ollama", "llava"), // absent from store
			wantImage: true,
			wantPDF:   false,
		},
		{
			name:      "image and pdf, nil store",
			override:  &CapsOverride{Image: true, PDF: true},
			store:     nil,
			id:        modelsdev.NewID("my-proxy", "gpt-4o"),
			wantImage: true,
			wantPDF:   true,
		},
		{
			name:      "explicit text-only override masks a catalogued vision model",
			override:  &CapsOverride{}, // both false
			store:     visionStore(),
			id:        modelsdev.NewID("openai", "gpt-4o"), // catalogued as vision
			wantImage: false,
			wantPDF:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mc := ResolveCaps(t.Context(), tc.store, tc.id, tc.override)
			assert.Equal(t, tc.wantImage, mc.Supports("image/jpeg"))
			assert.Equal(t, tc.wantPDF, mc.Supports("application/pdf"))
		})
	}
}

func TestResolveCaps_NilOverrideFallsBackToModelsDev(t *testing.T) {
	t.Parallel()

	store := visionStore()

	// Catalogued vision model resolves to vision caps.
	hit := ResolveCaps(t.Context(), store, modelsdev.NewID("openai", "gpt-4o"), nil)
	assert.True(t, hit.Supports("image/jpeg"))
	assert.True(t, hit.Supports("application/pdf"))

	// Uncatalogued model degrades to text-only (the #2741 default without an override).
	miss := ResolveCaps(t.Context(), store, modelsdev.NewID("ollama", "llava"), nil)
	assert.False(t, miss.Supports("image/jpeg"))
	assert.False(t, miss.Supports("application/pdf"))
	assert.True(t, miss.Supports("text/plain"))
}

// TestLoadCaps_MissDiagnosticDedup verifies the Option C diagnostic: a
// models.dev miss is logged once per model id, not on every lookup.
//
// It swaps the default slog logger and is deliberately NOT parallel: Go runs
// non-parallel tests in a sequential phase before parallel ones start, so no
// other test logs into the buffer concurrently. The assertion further filters
// by a unique model id to stay robust regardless.
func TestLoadCaps_MissDiagnosticDedup(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	store := visionStore()
	// Unique id so the package-global dedup map cannot hide the first warning
	// behind another test's lookup of the same id.
	id := modelsdev.NewID("dedup-probe-provider", "dedup-probe-model")

	for range 3 {
		mc := LoadCaps(t.Context(), store, id)
		require.False(t, mc.Supports("image/jpeg"))
	}

	lines := 0
	for line := range strings.SplitSeq(buf.String(), "\n") {
		if strings.Contains(line, "dedup-probe-model") {
			lines++
		}
	}
	assert.Equal(t, 1, lines, "miss diagnostic must be logged exactly once per model id, got %d", lines)
	assert.Contains(t, buf.String(), "not found in models.dev")
	assert.Contains(t, buf.String(), "capabilities", "diagnostic should point at the config override")
}
