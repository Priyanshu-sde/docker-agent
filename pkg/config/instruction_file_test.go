package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeConfigDir creates a temp directory holding an agent config file named
// agent.yaml with the given body, plus any extra files (path relative to the
// directory -> contents). It returns the directory path.
func writeConfigDir(t *testing.T, configYAML string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte(configYAML), 0o644))
	for name, content := range files {
		full := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
	}
	return dir
}

func TestInstructionFileResolved(t *testing.T) {
	t.Parallel()

	cfgYAML := `agents:
  root:
    model: openai/gpt-4o
    description: test agent
    instruction_file: instructions/root.md
`
	dir := writeConfigDir(t, cfgYAML, map[string]string{
		"instructions/root.md": "You are a helpful assistant.\n",
	})

	cfg, err := Load(t.Context(), NewFileSource(filepath.Join(dir, "agent.yaml")))
	require.NoError(t, err)

	agent := cfg.Agents.First()
	assert.Equal(t, "You are a helpful assistant.\n", agent.Instruction)
	// The reference is cleared after resolution so the in-memory config is
	// self-contained and round-trips through marshalling.
	assert.Empty(t, agent.InstructionFile)
}

func TestInstructionFileMissing(t *testing.T) {
	t.Parallel()

	cfgYAML := `agents:
  root:
    model: openai/gpt-4o
    description: test agent
    instruction_file: instructions/missing.md
`
	dir := writeConfigDir(t, cfgYAML, nil)

	_, err := Load(t.Context(), NewFileSource(filepath.Join(dir, "agent.yaml")))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "root")
	assert.Contains(t, err.Error(), "instruction_file")
	assert.Contains(t, err.Error(), "instructions/missing.md")
}

func TestInstructionFileRejectsTraversal(t *testing.T) {
	t.Parallel()

	cfgYAML := `agents:
  root:
    model: openai/gpt-4o
    description: test agent
    instruction_file: ../secret.md
`
	dir := writeConfigDir(t, cfgYAML, nil)
	// A file outside the config directory that traversal would try to reach.
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(dir), "secret.md"), []byte("secret"), 0o644))

	_, err := Load(t.Context(), NewFileSource(filepath.Join(dir, "agent.yaml")))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "local relative path")
	assert.Contains(t, err.Error(), "../secret.md")
}

func TestInstructionFileRejectsAbsolutePath(t *testing.T) {
	t.Parallel()

	abs := filepath.Join(t.TempDir(), "outside.md")
	require.NoError(t, os.WriteFile(abs, []byte("secret"), 0o644))

	cfgYAML := `agents:
  root:
    model: openai/gpt-4o
    description: test agent
    instruction_file: ` + abs + `
`
	dir := writeConfigDir(t, cfgYAML, nil)

	_, err := Load(t.Context(), NewFileSource(filepath.Join(dir, "agent.yaml")))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "local relative path")
}

func TestInstructionFileMutuallyExclusive(t *testing.T) {
	t.Parallel()

	cfgYAML := `agents:
  root:
    model: openai/gpt-4o
    description: test agent
    instruction: inline prompt
    instruction_file: instructions/root.md
`
	dir := writeConfigDir(t, cfgYAML, map[string]string{
		"instructions/root.md": "from file",
	})

	_, err := Load(t.Context(), NewFileSource(filepath.Join(dir, "agent.yaml")))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

func TestInstructionFileParentlessSource(t *testing.T) {
	t.Parallel()

	cfgYAML := `agents:
  root:
    model: openai/gpt-4o
    description: test agent
    instruction_file: instructions/root.md
`
	// A bytes source has no directory to resolve the reference against.
	_, err := Load(t.Context(), NewBytesSource("config.yaml", []byte(cfgYAML)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "local file-based configs")
}

func TestInstructionFileRejectsSymlinkEscape(t *testing.T) {
	t.Parallel()

	cfgYAML := `agents:
  root:
    model: openai/gpt-4o
    description: test agent
    instruction_file: instructions/root.md
`
	dir := writeConfigDir(t, cfgYAML, nil)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "instructions"), 0o755))

	outside := filepath.Join(filepath.Dir(dir), "outside.md")
	require.NoError(t, os.WriteFile(outside, []byte("secret"), 0o644))

	link := filepath.Join(dir, "instructions", "root.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	_, err := Load(t.Context(), NewFileSource(filepath.Join(dir, "agent.yaml")))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "instruction_file")
}

// TestInstructionFileEmptyStringIgnored verifies that an explicit empty
// instruction_file is treated as unset rather than triggering resolution.
func TestInstructionFileEmptyStringIgnored(t *testing.T) {
	t.Parallel()

	cfgYAML := `agents:
  root:
    model: openai/gpt-4o
    description: test agent
    instruction: inline prompt
    instruction_file: ""
`
	dir := writeConfigDir(t, cfgYAML, nil)

	cfg, err := Load(t.Context(), NewFileSource(filepath.Join(dir, "agent.yaml")))
	require.NoError(t, err)
	assert.Equal(t, "inline prompt", cfg.Agents.First().Instruction)
}
