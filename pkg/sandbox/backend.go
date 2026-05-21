package sandbox

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
)

// Backend describes how to invoke sandbox CLI commands.
// The two supported backends are "docker sandbox" and "sbx".
type Backend struct {
	// program is the executable name ("docker" or "sbx").
	program string
	// prefix is the sub-command prefix prepended to every command.
	// For "docker sandbox" this is ["sandbox"]; for "sbx" it is empty.
	prefix []string
	// extraEnv holds extra environment variables to set on every command.
	extraEnv []string
	// vmListKey is the JSON key returned by the "ls" command that holds
	// the list of sandboxes. Both supported backends now return
	// "sandboxes"; the field stays for forward-compatibility if either
	// backend ever drifts again.
	vmListKey string
	// allowHostsArgs builds the argv that adds an allow-network-host
	// rule scoped to a single sandbox. The two backends spell the same
	// operation differently — sbx exposes "sbx policy allow network
	// SANDBOX HOSTS" while docker exposes "docker sandbox network
	// proxy SANDBOX --allow-host HOST". Returning the args lets the
	// shared exec wrapper handle env / logging the same way for both.
	allowHostsArgs func(name string, hosts []string) []string
}

// NewBackend returns the appropriate backend.  When preferSbx is true
// and the "sbx" binary is on PATH, the sbx backend is used; otherwise
// it falls back to "docker sandbox".
func NewBackend(preferSbx bool) *Backend {
	if preferSbx {
		if _, err := exec.LookPath("sbx"); err == nil {
			return sbxBackend()
		}
	}
	return dockerSandboxBackend()
}

func dockerSandboxBackend() *Backend {
	return &Backend{
		program:   "docker",
		prefix:    []string{"sandbox"},
		vmListKey: "sandboxes",
		allowHostsArgs: func(name string, hosts []string) []string {
			// docker sandbox network proxy SANDBOX --allow-host HOST [--allow-host HOST ...]
			args := []string{"sandbox", "network", "proxy", name}
			for _, h := range hosts {
				args = append(args, "--allow-host", h)
			}
			return args
		},
	}
}

func sbxBackend() *Backend {
	return &Backend{
		program:   "sbx",
		prefix:    nil,
		extraEnv:  []string{"DOCKER_CLI_PLUGIN_ORIGINAL_CLI_COMMAND="},
		vmListKey: "sandboxes",
		allowHostsArgs: func(name string, hosts []string) []string {
			// sbx policy allow network SANDBOX comma,separated,hosts
			return []string{"policy", "allow", "network", name, strings.Join(hosts, ",")}
		},
	}
}

// AllowHosts adds a sandbox-scoped network allow rule for each entry
// in hosts. Hosts may carry an optional ":port" suffix (e.g.
// "api.example.com:443"). Returns a non-fatal error: callers usually
// log and continue, since a partial failure (e.g. a host already
// allowed by an earlier rule) shouldn't keep the sandbox from
// running.
//
// Empty entries are silently skipped. Entries that contain a comma
// are rejected because the sbx backend joins the list with commas
// when forwarding the rule to the policy engine; allowing them
// through unescaped would let a single value smuggle several
// distinct rules into the engine. Entries that contain a literal
// space are rejected for the same defence-in-depth reason — callers
// should pass already-split hostnames.
func (b *Backend) AllowHosts(ctx context.Context, name string, hosts []string) error {
	if name == "" {
		return nil
	}
	if b.allowHostsArgs == nil {
		return fmt.Errorf("backend %q does not support per-sandbox network allowlists", b.program)
	}

	cleaned := make([]string, 0, len(hosts))
	for _, h := range hosts {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if strings.ContainsAny(h, ", \t") {
			return fmt.Errorf("refusing to allowlist host %q: contains comma or whitespace", h)
		}
		cleaned = append(cleaned, h)
	}
	if len(cleaned) == 0 {
		return nil
	}

	args := b.allowHostsArgs(name, cleaned)
	cmd := exec.CommandContext(ctx, b.program, args...)
	b.applyEnv(cmd)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w (output: %s)", b.program, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	slog.DebugContext(ctx, "Allowed sandbox network access",
		"sandbox", name, "hosts", cleaned, "output", strings.TrimSpace(string(out)))
	return nil
}

// command builds an exec.Cmd for the given sandbox sub-command and arguments.
// For example, command(ctx, "ls", "--json") produces either
// "docker sandbox ls --json" or "sbx ls --json".
func (b *Backend) args(subCmd string, extra ...string) []string {
	args := make([]string, 0, len(b.prefix)+1+len(extra))
	args = append(args, b.prefix...)
	args = append(args, subCmd)
	args = append(args, extra...)
	return args
}

// applyEnv augments the command's environment with any backend-specific
// variables.  It must be called on every exec.Cmd created for the backend.
func (b *Backend) applyEnv(cmd *exec.Cmd) {
	if len(b.extraEnv) > 0 {
		if cmd.Env == nil {
			cmd.Env = os.Environ()
		}
		cmd.Env = append(cmd.Env, b.extraEnv...)
	}
}
