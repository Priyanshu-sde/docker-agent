package path

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		input    string
		envSetup map[string]string
		expected string
	}{
		{
			name:     "empty path",
			input:    "",
			expected: "",
		},
		{
			name:     "tilde only",
			input:    "~",
			expected: home,
		},
		{
			name:     "tilde with subpath",
			input:    "~/data/memory.db",
			expected: filepath.Join(home, "data", "memory.db"),
		},
		{
			name:     "env var",
			input:    "${HOME}/.data/memory.db",
			expected: filepath.Join(home, ".data", "memory.db"),
		},
		{
			name:     "custom env var",
			input:    "${MY_TEST_DATA_DIR}/memory.db",
			envSetup: map[string]string{"MY_TEST_DATA_DIR": "/tmp/testdata"},
			expected: "/tmp/testdata/memory.db",
		},
		{
			name:     "absolute path unchanged",
			input:    "/absolute/path/memory.db",
			expected: "/absolute/path/memory.db",
		},
		{
			name:     "relative path unchanged",
			input:    "relative/path/memory.db",
			expected: "relative/path/memory.db",
		},
		{
			name:     "tilde and env var combined",
			input:    "~/${MY_TEST_SUBDIR}/memory.db",
			envSetup: map[string]string{"MY_TEST_SUBDIR": "data"},
			expected: filepath.Join(home, "data", "memory.db"),
		},
		{
			name:     "js env ref",
			input:    "${env.HOME}/.data/memory.db",
			expected: filepath.Join(home, ".data", "memory.db"),
		},
		{
			name:     "js env ref custom var",
			input:    "${env.MY_TEST_DATA_DIR}/memory.db",
			envSetup: map[string]string{"MY_TEST_DATA_DIR": "/tmp/testdata"},
			expected: "/tmp/testdata/memory.db",
		},
		{
			name:     "js env ref with surrounding whitespace",
			input:    "${ env.MY_TEST_DATA_DIR }/memory.db",
			envSetup: map[string]string{"MY_TEST_DATA_DIR": "/tmp/testdata"},
			expected: "/tmp/testdata/memory.db",
		},
		{
			name:     "tilde and js env ref combined",
			input:    "~/${env.MY_TEST_SUBDIR}/memory.db",
			envSetup: map[string]string{"MY_TEST_SUBDIR": "data"},
			expected: filepath.Join(home, "data", "memory.db"),
		},
		{
			name:     "shell and js env refs mixed",
			input:    "${MY_TEST_DATA_DIR}/${env.MY_TEST_SUBDIR}/memory.db",
			envSetup: map[string]string{"MY_TEST_DATA_DIR": "/tmp/testdata", "MY_TEST_SUBDIR": "data"},
			expected: "/tmp/testdata/data/memory.db",
		},
		{
			name:     "undefined js env ref expands to empty",
			input:    "/base/${env.MY_TEST_UNDEFINED}/memory.db",
			expected: "/base//memory.db",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.envSetup {
				t.Setenv(k, v)
			}
			result := ExpandPath(tt.input)
			if result != tt.expected {
				t.Errorf("ExpandPath(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}
