package oci

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/config"
	"github.com/docker/docker-agent/pkg/content"
)

func TestPackageFileAsOCIToStore(t *testing.T) {
	agentFilename := filepath.Join(t.TempDir(), "test.yaml")
	testContent := `version: "2"
agents:
  root:
    model: auto
    description: A helpful AI assistant
`
	require.NoError(t, os.WriteFile(agentFilename, []byte(testContent), 0o644))
	store, err := content.NewStore(content.WithBaseDir(t.TempDir()))
	require.NoError(t, err)

	agentSource, err := config.Resolve(agentFilename, nil)
	require.NoError(t, err)

	tag := "test-app:v1.0.0"
	digest, err := PackageFileAsOCIToStore(t.Context(), agentSource, tag, store)
	require.NoError(t, err)
	assert.NotEmpty(t, digest)
	t.Cleanup(func() {
		if err := store.DeleteArtifact(digest); err != nil {
			t.Logf("Failed to clean up artifact: %v", err)
		}
	})

	img, err := store.GetArtifactImage(tag)
	require.NoError(t, err)
	assert.NotNil(t, img)

	metadata, err := store.GetArtifactMetadata(tag)
	require.NoError(t, err)

	assert.Equal(t, tag, metadata.Reference)
	assert.Equal(t, digest, metadata.Digest)

	// Verify annotations are present
	require.NotNil(t, metadata.Annotations)
	assert.Contains(t, metadata.Annotations, "org.opencontainers.image.created")
	assert.Contains(t, metadata.Annotations, "org.opencontainers.image.description")
	assert.Equal(t, "OCI artifact containing test.yaml", metadata.Annotations["org.opencontainers.image.description"])
}

func TestPackageFileAsOCIToStore_InlinesInstructionFile(t *testing.T) {
	// A config that uses instruction_file must be pushed self-contained: the
	// file contents are inlined and the now-unresolvable reference is dropped,
	// even when the config carries an explicit version (which normally makes
	// the packager preserve the raw bytes).
	dir := t.TempDir()
	agentFilename := filepath.Join(dir, "test.yaml")
	testContent := `version: "11"
agents:
  root:
    model: auto
    description: Test agent
    instruction_file: instructions/root.md
`
	require.NoError(t, os.WriteFile(agentFilename, []byte(testContent), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "instructions"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "instructions", "root.md"), []byte("You are a self-contained agent."), 0o644))

	store, err := content.NewStore(content.WithBaseDir(t.TempDir()))
	require.NoError(t, err)

	agentSource, err := config.Resolve(agentFilename, nil)
	require.NoError(t, err)

	tag := "test-instruction-file:v1.0.0"
	digest, err := PackageFileAsOCIToStore(t.Context(), agentSource, tag, store)
	require.NoError(t, err)
	assert.NotEmpty(t, digest)
	t.Cleanup(func() { _ = store.DeleteArtifact(digest) })

	img, err := store.GetArtifactImage(tag)
	require.NoError(t, err)
	layers, err := img.Layers()
	require.NoError(t, err)
	require.Len(t, layers, 1)
	reader, err := layers[0].Uncompressed()
	require.NoError(t, err)
	defer reader.Close()
	data, err := io.ReadAll(reader)
	require.NoError(t, err)

	// The instruction is inlined and the file reference is gone.
	assert.Contains(t, string(data), "You are a self-contained agent.")
	assert.NotContains(t, string(data), "instruction_file")

	// The pushed artifact loads without the original local file present.
	cfg, err := config.Load(t.Context(), config.NewBytesSource("pulled.yaml", data))
	require.NoError(t, err)
	assert.Equal(t, "You are a self-contained agent.", cfg.Agents.First().Instruction)
}

func TestPackageFileAsOCIToStoreInvalidTag(t *testing.T) {
	agentFilename := filepath.Join(t.TempDir(), "test.txt")
	require.NoError(t, os.WriteFile(agentFilename, []byte("test content"), 0o644))

	agentSource, err := config.Resolve(agentFilename, nil)
	require.NoError(t, err)

	store, err := content.NewStore(content.WithBaseDir(t.TempDir()))
	require.NoError(t, err)
	_, err = PackageFileAsOCIToStore(t.Context(), agentSource, "", store)
	require.Error(t, err)
}

func TestPackageFileAsOCIToStore_WithProviders(t *testing.T) {
	// Test that configs with providers are correctly marshalled when packaged
	// This is important because configs without version get re-marshalled
	agentFilename := filepath.Join(t.TempDir(), "test.yaml")
	testContent := `providers:
  my_gateway:
    api_type: openai_chatcompletions
    base_url: http://localhost:8080
    token_key: MY_API_KEY

agents:
  root:
    model: my_gateway/gpt-4o
    description: Test agent
`
	require.NoError(t, os.WriteFile(agentFilename, []byte(testContent), 0o644))
	store, err := content.NewStore(content.WithBaseDir(t.TempDir()))
	require.NoError(t, err)

	agentSource, err := config.Resolve(agentFilename, nil)
	require.NoError(t, err)

	tag := "test-providers:v1.0.0"
	digest, err := PackageFileAsOCIToStore(t.Context(), agentSource, tag, store)
	require.NoError(t, err)
	assert.NotEmpty(t, digest)

	t.Cleanup(func() {
		_ = store.DeleteArtifact(digest)
	})

	// Pull the artifact and verify providers are preserved
	img, err := store.GetArtifactImage(tag)
	require.NoError(t, err)

	layers, err := img.Layers()
	require.NoError(t, err)
	require.Len(t, layers, 1)

	reader, err := layers[0].Uncompressed()
	require.NoError(t, err)
	defer reader.Close()

	data, err := io.ReadAll(reader)
	require.NoError(t, err)

	// Verify the providers section is present with correct keys
	assert.Contains(t, string(data), "providers:")
	assert.Contains(t, string(data), "my_gateway:")
	assert.Contains(t, string(data), "api_type:")
	assert.Contains(t, string(data), "base_url:")
	assert.Contains(t, string(data), "token_key:")
}
