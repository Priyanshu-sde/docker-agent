package path

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ExpandHomeDir expands a leading home-directory reference in a path.
// It expands "~", "~/...", and "~\\..." on Windows. Other tilde forms,
// such as "~user/...", are returned unchanged.
func ExpandHomeDir(path string) (string, error) {
	if !isHomeDirPath(path) {
		return path, nil
	}
	homeDir, err := userHomeDir()
	if err != nil || homeDir == "" {
		return "", errors.New("failed to get user home directory")
	}
	if path == "~" {
		return filepath.Clean(homeDir), nil
	}
	return filepath.Join(homeDir, path[2:]), nil
}

// userHomeDir returns the user's home directory, preferring the HOME
// environment variable (used cross-platform, including in tests) before
// falling back to os.UserHomeDir(), which on Windows reads USERPROFILE
// instead of HOME.
func userHomeDir() (string, error) {
	if home := os.Getenv("HOME"); home != "" {
		return home, nil
	}
	return os.UserHomeDir()
}

func isHomeDirPath(path string) bool {
	return path == "~" || strings.HasPrefix(path, "~/") || (filepath.Separator == '\\' && strings.HasPrefix(path, `~\`))
}
