package tui

import (
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pickerDirs creates a directory tree for picker tests:
//
//	root/
//	  alpha/        (git repo)
//	  beta/
//	  .hidden/
//	  file.txt
func pickerDirs(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "alpha", ".git"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(root, "beta"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(root, ".hidden"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "file.txt"), nil, 0o644))
	return root
}

func keyPress(s string) tea.KeyPressMsg {
	if len(s) == 1 {
		return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
	}
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	}
	panic("unknown key " + s)
}

// press feeds one key to the picker, ignoring its outputs.
func press(p *dirPicker, key string) {
	_, _, _ = p.Update(keyPress(key)) //nolint:dogsled // navigation side effects only
}

func labels(entries []pickerEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.label)
	}
	return out
}

func TestDirPickerListsDirectoriesOnly(t *testing.T) {
	t.Parallel()

	root := pickerDirs(t)
	p := newDirPicker(root)

	// Hidden dirs and files are excluded; "use this directory" and ".."
	// always lead.
	assert.Equal(t, []string{"use this directory", "..", "alpha", "beta"}, labels(p.filtered))
	assert.True(t, p.filtered[2].git, "alpha is a git repo")
	assert.False(t, p.filtered[3].git, "beta is not")
}

func TestDirPickerNavigation(t *testing.T) {
	t.Parallel()

	root := pickerDirs(t)
	p := newDirPicker(root)

	// Descend into alpha (down twice: use-dir -> .. -> alpha).
	press(p, "down")
	press(p, "down")
	chosen, done, _ := p.Update(keyPress("enter"))
	assert.Empty(t, chosen)
	assert.False(t, done)
	assert.Equal(t, filepath.Join(root, "alpha"), p.dir)

	// Backspace with an empty filter walks back up.
	press(p, "backspace")
	assert.Equal(t, root, p.dir)

	// Enter on "use this directory" picks it.
	chosen, done, _ = p.Update(keyPress("enter"))
	assert.Equal(t, root, chosen)
	assert.True(t, done)
}

func TestDirPickerFilterAndCancel(t *testing.T) {
	t.Parallel()

	root := pickerDirs(t)
	p := newDirPicker(root)

	// Typing filters subdirectories but keeps the fixed rows.
	for _, r := range "bet" {
		press(p, string(r))
	}
	assert.Equal(t, []string{"use this directory", "..", "beta"}, labels(p.filtered))

	// Esc cancels without choosing.
	chosen, done, _ := p.Update(keyPress("esc"))
	assert.Empty(t, chosen)
	assert.True(t, done)
}

func TestPickerStartDir(t *testing.T) {
	t.Parallel()

	root := pickerDirs(t)
	assert.Equal(t, root, pickerStartDir(root))
	// Missing or file hints fall back to a usable directory.
	assert.NotEqual(t, filepath.Join(root, "gone"), pickerStartDir(filepath.Join(root, "gone")))
	assert.NotEqual(t, filepath.Join(root, "file.txt"), pickerStartDir(filepath.Join(root, "file.txt")))
}
