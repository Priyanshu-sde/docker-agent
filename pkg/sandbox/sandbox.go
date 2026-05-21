// Package sandbox provides Docker sandbox lifecycle management including
// creation, detection, argument building, and environment forwarding.
package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/docker/docker-agent/pkg/config"
	"github.com/docker/docker-agent/pkg/environment"
)

// CheckAvailable returns a user-friendly error when Docker is not
// installed or the sandbox feature is not supported.
func (b *Backend) CheckAvailable(ctx context.Context) error {
	if _, err := exec.LookPath(b.program); err != nil {
		return fmt.Errorf("--sandbox requires Docker Desktop: %w\n\nInstall Docker Desktop from https://docs.docker.com/get-docker/", err)
	}

	cmd := exec.CommandContext(ctx, b.program, b.args("version")...)
	b.applyEnv(cmd)
	if err := cmd.Run(); err != nil {
		return errors.New("--sandbox requires Docker Desktop with sandbox support\n\n" +
			"Make sure Docker Desktop is running and up to date.\n" +
			"For more information, see https://docs.docker.com/ai/sandboxes/")
	}

	return nil
}

// Existing holds the name and workspaces of an existing Docker sandbox.
type Existing struct {
	Name       string   `json:"name"`
	Workspaces []string `json:"workspaces"`
}

// HasWorkspace reports whether the sandbox has dir mounted as a workspace.
func (s *Existing) HasWorkspace(dir string) bool {
	return slices.ContainsFunc(s.Workspaces, func(ws string) bool {
		// Workspaces may have a ":ro" suffix.
		return strings.TrimSuffix(ws, ":ro") == dir
	})
}

// ForWorkspace returns the existing sandbox whose primary workspace
// matches wd, or nil if none exists.
func (b *Backend) ForWorkspace(ctx context.Context, wd string) *Existing {
	cmd := exec.CommandContext(ctx, b.program, b.args("ls", "--json")...)
	b.applyEnv(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil
	}

	// Both supported backends now return {"sandboxes": [...]}. Older
	// docker sandbox versions wrapped the list under "vms" instead;
	// fall back to that and warn so a user on an outdated CLI still
	// gets sandbox reuse instead of accumulating duplicates while
	// silently being told to upgrade.
	listJSON, ok := raw[b.vmListKey]
	if !ok {
		if legacy, hasLegacy := raw["vms"]; hasLegacy && b.vmListKey != "vms" {
			slog.WarnContext(ctx,
				`sandbox ls --json returned the legacy "vms" key; please upgrade Docker Desktop / sbx for full feature support`,
				"backend", b.program)
			listJSON = legacy
		} else {
			return nil
		}
	}

	var entries []Existing
	if err := json.Unmarshal(listJSON, &entries); err != nil {
		return nil
	}

	for _, entry := range entries {
		if len(entry.Workspaces) > 0 && entry.Workspaces[0] == wd {
			return &entry
		}
	}
	return nil
}

// Ensure makes sure a sandbox exists for the given workspace,
// creating or recreating it as needed. extras is a list of additional
// host directories to mount read-only (kit dir, agent yaml dir, ...).
// Each entry is made absolute and cleaned; duplicates and entries that
// resolve to wd are filtered out. When template is non-empty it is
// passed to `docker sandbox create -t`. Returns the sandbox name.
func (b *Backend) Ensure(ctx context.Context, wd string, extras []string, template, configDir string) (string, error) {
	wd, err := absClean(wd)
	if err != nil {
		return "", fmt.Errorf("resolving workspace path: %w", err)
	}
	absConfigDir, err := absClean(configDir)
	if err != nil {
		return "", fmt.Errorf("resolving config dir: %w", err)
	}
	configDir = absConfigDir

	extras, err = cleanExtras(extras, wd)
	if err != nil {
		return "", err
	}

	existing := b.ForWorkspace(ctx, wd)

	// If the sandbox exists with the right mounts, reuse it.
	if existing != nil &&
		hasAllWorkspaces(existing, extras) &&
		existing.HasWorkspace(configDir) {
		slog.DebugContext(ctx, "Reusing existing sandbox", "name", existing.Name)
		return existing.Name, nil
	}

	// Remove a stale sandbox whose mounts don't match. Errors are
	// logged but not fatal: docker sandbox create has its own
	// detection of name conflicts and will pick a fresh name (e.g.
	// foo-1) on collision.
	if existing != nil {
		slog.DebugContext(ctx, "Removing existing sandbox to change workspace mounts", "name", existing.Name)
		rmCmd := exec.CommandContext(ctx, b.program, b.args("rm", existing.Name)...)
		b.applyEnv(rmCmd)
		if rmOut, rmErr := rmCmd.CombinedOutput(); rmErr != nil {
			slog.DebugContext(ctx, "Failed to remove existing sandbox",
				"name", existing.Name, "error", rmErr, "output", strings.TrimSpace(string(rmOut)))
		}
	}

	createExtra := []string{}
	if template != "" {
		createExtra = append(createExtra, "-t", template)
	}
	createExtra = append(createExtra, "cagent", wd)
	for _, e := range extras {
		createExtra = append(createExtra, e+":ro")
	}
	// Mount config directory read-only so the sandbox can
	// read the token file and access user config.
	createExtra = append(createExtra, configDir+":ro")

	createArgs := b.args("create", createExtra...)
	slog.DebugContext(ctx, "Creating sandbox", "args", createArgs)

	createCmd := exec.CommandContext(ctx, b.program, createArgs...)
	b.applyEnv(createCmd)
	createCmd.Stdin = os.Stdin
	createCmd.Stdout = os.Stdout
	createCmd.Stderr = os.Stderr

	if err := createCmd.Run(); err != nil {
		return "", fmt.Errorf("sandbox create failed: %w", err)
	}

	// Read back the sandbox name that was just created.
	created := b.ForWorkspace(ctx, wd)
	if created == nil {
		return "", errors.New("sandbox was created but could not be found")
	}

	return created.Name, nil
}

// absClean normalises a path so that two textually different but
// equivalent paths (e.g. "./foo" and "foo", "a//b" and "a/b") collapse
// before reaching the workspace dedup / reuse comparisons. Returns an
// error if filepath.Abs fails.
func absClean(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

// cleanExtras drops empty entries, normalises every other entry with
// [absClean], removes duplicates, and filters out anything that
// resolves to wd — wd is already mounted read-write, so a second
// mount of the same path would shadow it.
//
// wd is expected to already be canonical (caller passes Ensure's
// resolved wd).
func cleanExtras(extras []string, wd string) ([]string, error) {
	dedup := map[string]bool{wd: true}
	cleaned := make([]string, 0, len(extras))
	for _, e := range extras {
		if e == "" {
			continue
		}
		abs, err := absClean(e)
		if err != nil {
			return nil, fmt.Errorf("resolving extra workspace %q: %w", e, err)
		}
		if dedup[abs] {
			continue
		}
		dedup[abs] = true
		cleaned = append(cleaned, abs)
	}
	return cleaned, nil
}

// hasAllWorkspaces reports whether every entry of extras is mounted
// in s. Empty extras returns true.
func hasAllWorkspaces(s *Existing, extras []string) bool {
	for _, e := range extras {
		if !s.HasWorkspace(e) {
			return false
		}
	}
	return true
}

// BuildExecCmd assembles the sandbox exec command.
func (b *Backend) BuildExecCmd(ctx context.Context, name, wd string, cagentArgs, envFlags, envVars []string) *exec.Cmd {
	execExtra := []string{"-it", "-w", wd}
	execExtra = append(execExtra, envFlags...)

	// Improve the rendering of the TUI
	execExtra = append(execExtra,
		"-e", "TERM=xterm-256color",
		"-e", "COLORTERM=truecolor",
		"-e", "LANG=en_US.UTF-8",
		name, "docker-agent", "run",
	)
	execExtra = append(execExtra, cagentArgs...)

	args := b.args("exec", execExtra...)

	cmd := exec.CommandContext(ctx, b.program, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), envVars...)
	b.applyEnv(cmd)

	return cmd
}

// StartTokenWriterIfNeeded starts a background goroutine that refreshes
// DOCKER_TOKEN into a shared file when a models gateway is configured.
// Returns a stop function that is safe to call multiple times (and is a
// no-op when no writer was started).
func StartTokenWriterIfNeeded(ctx context.Context, dir, modelsGateway string) func() {
	if modelsGateway == "" {
		return func() {}
	}

	tokenPath := environment.SandboxTokensFilePath(dir)
	w := environment.NewSandboxTokenWriter(
		tokenPath,
		environment.NewDockerDesktopProvider(),
		time.Minute,
	)
	w.Start(ctx)

	return w.Stop
}

// proxyManagedEnvVars lists the environment variables that Docker Desktop
// automatically proxies into sandboxes. We don't need to forward these.
var proxyManagedEnvVars = []string{
	"OPENAI_API_KEY",
	"ANTHROPIC_API_KEY",
	"GOOGLE_API_KEY",
	"MISTRAL_API_KEY",
	"XAI_API_KEY",
	"NEBIUS_API_KEY",
}

// EnvForAgent loads the agent config and gathers the environment
// variables it requires. It returns:
//   - flags: `-e KEY` args for docker sandbox exec (name only, no value)
//   - envVars: `KEY=VALUE` entries to set on the exec process environment
//
// Variables that Docker Desktop already proxies are skipped.
func EnvForAgent(ctx context.Context, agentRef string, env environment.Provider) (flags, envVars []string) {
	if agentRef == "" {
		return nil, nil
	}

	names, err := gatherAgentEnvVars(ctx, agentRef, env)
	if err != nil {
		slog.DebugContext(ctx, "Failed to gather agent env vars for sandbox", "error", err)
		return nil, nil
	}

	for _, name := range names {
		if slices.Contains(proxyManagedEnvVars, name) {
			continue
		}
		val, ok := env.Get(ctx, name)
		if !ok || val == "" {
			continue
		}
		flags = append(flags, "-e", name)
		envVars = append(envVars, name+"="+val)
	}

	return flags, envVars
}

// gatherAgentEnvVars resolves the agent config and returns the list of
// environment variable names required by its models and tools.
func gatherAgentEnvVars(ctx context.Context, agentRef string, env environment.Provider) ([]string, error) {
	source, err := config.Resolve(agentRef, env)
	if err != nil {
		return nil, fmt.Errorf("resolving agent: %w", err)
	}

	cfg, err := config.Load(ctx, source)
	if err != nil {
		return nil, fmt.Errorf("loading agent config: %w", err)
	}

	var names []string
	names = append(names, config.GatherEnvVarsForModels(cfg)...)

	toolNames, err := config.GatherEnvVarsForTools(ctx, cfg)
	if err != nil {
		slog.DebugContext(ctx, "Failed to gather tool env vars", "error", err)
	}
	names = append(names, toolNames...)

	return names, nil
}
