// Package worktree creates throwaway git worktrees so an agent can work in
// isolation from the user's checkout. The worktree shares the repository's
// object store but has its own working directory and branch, letting the
// user keep using the original checkout while the agent makes changes.
package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/docker/docker-agent/pkg/paths"
	"github.com/docker/docker-agent/pkg/worktree/namesgenerator"
)

// ErrNotGitRepository means the requested directory is not inside a git worktree.
var ErrNotGitRepository = errors.New("not a git repository")

// ErrInvalidName means the requested worktree name cannot be safely used as a
// directory and branch component.
var ErrInvalidName = errors.New("invalid worktree name")

// Worktree describes a git worktree created for an agent session.
type Worktree struct {
	// Dir is the absolute path of the worktree's working directory.
	Dir string
	// Branch is the branch checked out in the worktree.
	Branch string
	// Name is the worktree's name (the part after the "worktree-" branch prefix).
	Name string
	// SourceDir is the root of the repository the worktree was branched
	// from. The worktree lives under the data directory, far from the
	// original checkout, so setup hooks need this to copy untracked files
	// (.env, local config) git won't carry over.
	SourceDir string
}

// Create creates a new git worktree for the repository containing dir and
// returns it. The worktree lives under the data directory and checks out a
// freshly created branch so the agent's changes stay isolated from the user's
// checkout.
//
// When name is empty, a friendly random name (e.g. "focused_turing") is
// generated. The branch is named "worktree-<name>" and the worktree is stored
// under <dataDir>/worktrees/<name>.
//
// Returns [ErrNotGitRepository] when dir is not inside a git worktree, and
// [ErrInvalidName] when an explicit name is not a safe path/branch component.
func Create(ctx context.Context, dir, name string) (*Worktree, error) {
	root, err := repoRoot(ctx, dir)
	if err != nil {
		return nil, err
	}

	if name == "" {
		name = namesgenerator.GetRandomName(0)
	} else if err := validateName(name); err != nil {
		return nil, err
	}

	branch := "worktree-" + name
	dest := filepath.Join(paths.GetDataDir(), "worktrees", name)

	if _, err := os.Stat(dest); err == nil {
		return nil, fmt.Errorf("%w: worktree %q already exists at %s", ErrInvalidName, name, dest)
	}

	if err := git(ctx, root, "worktree", "add", "-b", branch, dest); err != nil {
		return nil, fmt.Errorf("creating git worktree: %w", err)
	}

	return &Worktree{Dir: dest, Branch: branch, Name: name, SourceDir: root}, nil
}

// validateName rejects names that would escape the worktrees directory or
// produce an invalid git branch. Names must be a single path segment made of
// safe characters, which also keeps the derived "worktree-<name>" branch valid.
func validateName(name string) error {
	if name != strings.TrimSpace(name) {
		return fmt.Errorf("%w: %q has surrounding whitespace", ErrInvalidName, name)
	}
	if strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("%w: %q must not contain path separators", ErrInvalidName, name)
	}
	if name == "." || name == ".." {
		return fmt.Errorf("%w: %q is not allowed", ErrInvalidName, name)
	}
	// filepath.Base collapses separators and ".."; if the cleaned segment
	// differs, the input was not a plain single path component.
	if filepath.Base(name) != name {
		return fmt.Errorf("%w: %q must be a single path segment", ErrInvalidName, name)
	}
	return nil
}

// repoRoot returns the worktree root of the git repository containing dir,
// or [ErrNotGitRepository] when dir is not inside one.
func repoRoot(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", ErrNotGitRepository
		}
		return "", err
	}
	return filepath.Clean(strings.TrimSpace(stdout.String())), nil
}

func git(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}
