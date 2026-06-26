package dmr

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testQwenBlobURL = "https://production.cloudfront.docker.com/registry-v2/docker/registry/v2/blobs/sha256/b5/b505f0cf69207567fdc6acec5a6d36303673a7da8cddf030f041677c85681729/data?Expires=1&Signature=x"

func TestCorruptPartial(t *testing.T) {
	digest := "b505f0cf69207567fdc6acec5a6d36303673a7da8cddf030f041677c85681729"

	t.Run("non-416 failure is ignored", func(t *testing.T) {
		_, _, ok := corruptPartial("Error: MANIFEST_UNKNOWN - Model not found")
		assert.False(t, ok)
	})

	t.Run("416 but no matching incomplete file", func(t *testing.T) {
		t.Setenv("DOCKER_CONFIG", t.TempDir())
		detail := "writing blob: ... GET " + testQwenBlobURL + ": 416 Requested Range Not Satisfiable"
		_, _, ok := corruptPartial(detail)
		assert.False(t, ok)
	})

	t.Run("416 with a matching incomplete file is detected", func(t *testing.T) {
		cfg := t.TempDir()
		t.Setenv("DOCKER_CONFIG", cfg)
		blobDir := filepath.Join(cfg, "models", "blobs", "sha256")
		require.NoError(t, os.MkdirAll(blobDir, 0o755))
		partial := filepath.Join(blobDir, digest+".incomplete")
		require.NoError(t, os.WriteFile(partial, []byte("0123456789"), 0o644))

		detail := "writing blob: read first byte: ... GET " + testQwenBlobURL + ": 416 Requested Range Not Satisfiable"
		path, size, ok := corruptPartial(detail)
		require.True(t, ok)
		assert.Equal(t, partial, path)
		assert.Equal(t, int64(10), size)
	})
}

func TestBuildPullErrorMessage_CorruptPartial(t *testing.T) {
	t.Parallel()

	partial := "/home/u/.docker/models/blobs/sha256/b505f0cf.incomplete"
	msg := buildPullErrorMessage("ai/qwen3", "... 416 Requested Range Not Satisfiable", partial, errors.New("exit status 1"))

	assert.Contains(t, msg, "Remove the corrupted partial download")
	assert.Contains(t, msg, "rm "+partial)
	assert.Contains(t, msg, "docker model pull ai/qwen3")
	// The generic "docker model rm" hint is replaced by the targeted one.
	assert.NotContains(t, msg, "docker model rm ai/qwen3")
}

func TestHumanizeBytes(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "512 B", humanizeBytes(512))
	assert.Equal(t, "1.0 KiB", humanizeBytes(1024))
	assert.Equal(t, "6.2 GiB", humanizeBytes(6677640642))
}

func TestCleanPullStderr(t *testing.T) {
	t.Parallel()

	t.Run("empty input", func(t *testing.T) {
		t.Parallel()
		assert.Empty(t, cleanPullStderr(""))
		assert.Empty(t, cleanPullStderr("   \n\n  \n"))
	})

	t.Run("keeps the 416 failure line", func(t *testing.T) {
		t.Parallel()
		raw := "Downloaded 90.68MB of 91.74MB\nFailed to pull model: Error: writing blob: ... 416 Requested Range Not Satisfiable\n"
		got := cleanPullStderr(raw)
		assert.Contains(t, got, "416 Requested Range Not Satisfiable")
	})

	t.Run("collapses carriage-return progress rewrites", func(t *testing.T) {
		t.Parallel()
		raw := "Downloaded 1MB\rDownloaded 50MB\rDownloaded 91MB"
		got := cleanPullStderr(raw)
		assert.Equal(t, "Downloaded 91MB", got)
		assert.NotContains(t, got, "Downloaded 1MB")
		assert.NotContains(t, got, "Downloaded 50MB")
	})

	t.Run("strips ANSI escape sequences", func(t *testing.T) {
		t.Parallel()
		raw := "\x1b[32mpulling\x1b[0m\n\x1b[1mError:\x1b[0m boom"
		got := cleanPullStderr(raw)
		assert.NotContains(t, got, "\x1b")
		assert.Contains(t, got, "Error: boom")
	})

	t.Run("keeps only the last few lines", func(t *testing.T) {
		t.Parallel()
		var lines []string
		for i := range 25 {
			lines = append(lines, fmt.Sprintf("line-%02d", i))
		}
		got := cleanPullStderr(strings.Join(lines, "\n"))
		assert.LessOrEqual(t, len(strings.Split(got, "\n")), maxPullStderrLines)
		// The last line survives, early lines are dropped.
		assert.Contains(t, got, "line-24")
		assert.NotContains(t, got, "line-00")
		assert.NotContains(t, got, "line-19")
	})
}

func TestPullFailedError(t *testing.T) {
	t.Parallel()

	t.Run("renders model, detail and remediation", func(t *testing.T) {
		t.Parallel()
		err := &PullFailedError{
			Model:  "ai/qwen3",
			Detail: "Error: writing blob: ... 416 Requested Range Not Satisfiable",
			Cause:  errors.New("exit status 1"),
		}
		msg := err.Error()

		assert.Contains(t, msg, "failed to pull model ai/qwen3")
		assert.Contains(t, msg, "416 Requested Range Not Satisfiable")
		assert.Contains(t, msg, "docker model rm ai/qwen3")
		assert.Contains(t, msg, "docker model pull ai/qwen3")
		assert.Contains(t, msg, "docker model ls")
		// The new message must not reintroduce the old opaque wrapper.
		assert.NotContains(t, msg, "failed to get models:")
	})

	t.Run("empty detail falls back to the cause and stays actionable", func(t *testing.T) {
		t.Parallel()
		err := &PullFailedError{
			Model: "ai/qwen3",
			Cause: errors.New("exit status 1"),
		}
		msg := err.Error()

		assert.Contains(t, msg, "failed to pull model ai/qwen3")
		assert.Contains(t, msg, "exit status 1")
		assert.Contains(t, msg, "docker model rm ai/qwen3")
		assert.NotEmpty(t, strings.TrimSpace(msg))
	})

	t.Run("empty detail and nil cause is still non-empty", func(t *testing.T) {
		t.Parallel()
		err := &PullFailedError{Model: "ai/qwen3"}
		msg := err.Error()
		assert.Contains(t, msg, "failed to pull model ai/qwen3")
		assert.Contains(t, msg, "docker model rm ai/qwen3")
	})

	t.Run("errors.As matches and Unwrap returns the cause", func(t *testing.T) {
		t.Parallel()
		cause := errors.New("exit status 1")
		var err error = &PullFailedError{Model: "ai/qwen3", Cause: cause}

		var pfe *PullFailedError
		require.ErrorAs(t, err, &pfe)
		assert.Equal(t, "ai/qwen3", pfe.Model)
		assert.Equal(t, cause, errors.Unwrap(err))
	})

	t.Run("summary is a concise one-liner", func(t *testing.T) {
		t.Parallel()
		err := &PullFailedError{Model: "ai/qwen3", Detail: "noisy\nmultiline\ndetail"}
		summary := err.ModelPullErrorSummary()
		assert.Equal(t, "failed to pull model ai/qwen3", summary)
		assert.NotContains(t, summary, "\n")
	})
}
