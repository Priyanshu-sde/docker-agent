package root

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/docker/cli/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/docker/docker-agent/pkg/config"
	"github.com/docker/docker-agent/pkg/desktop"
	"github.com/docker/docker-agent/pkg/environment"
	"github.com/docker/docker-agent/pkg/paths"
	"github.com/docker/docker-agent/pkg/sandbox"
	"github.com/docker/docker-agent/pkg/sandbox/kit"
	"github.com/docker/docker-agent/pkg/skills"
)

// runInSandbox delegates the current command to a Docker sandbox.
// It ensures a sandbox exists (creating or recreating as needed), then
// executes docker agent inside it via the sandbox exec command.
func runInSandbox(ctx context.Context, cmd *cobra.Command, args []string, runConfig *config.RuntimeConfig, template string, preferSbx, noKit bool) error {
	if environment.InSandbox() {
		return fmt.Errorf("already running inside a Docker sandbox (VM %s)", os.Getenv("SANDBOX_VM_ID"))
	}

	backend := sandbox.NewBackend(preferSbx)

	if err := backend.CheckAvailable(ctx); err != nil {
		return err
	}

	var agentRef string
	if len(args) > 0 {
		agentRef = args[0]
	}

	configDir := paths.GetConfigDir()
	dockerAgentArgs := dockerAgentArgs(cmd, args, configDir)

	stopTokenWriter := sandbox.StartTokenWriterIfNeeded(ctx, configDir, runConfig.ModelsGateway)
	defer stopTokenWriter()

	// Resolve wd to an absolute path so that it matches the absolute
	// workspace paths returned by `docker sandbox ls --json`.
	wd, err := filepath.Abs(cmp.Or(runConfig.WorkingDir, "."))
	if err != nil {
		return fmt.Errorf("resolving workspace path: %w", err)
	}

	envProvider := environment.NewDefaultProvider()

	extras := []string{sandbox.ExtraWorkspace(wd, agentRef)}

	var kitResult *kit.Result
	if !noKit && agentRef != "" {
		kitResult, err = kit.Build(ctx, kit.Options{
			AgentRef:    agentRef,
			EnvProvider: envProvider,
			HostCwd:     wd,
			Workspace:   wd,
		})
		if err != nil {
			slog.WarnContext(ctx, "docker-agent kit build failed; continuing without kit", "error", err)
		} else {
			kitResult.PrintSummary(cmd.OutOrStdout())
			extras = append(extras, kitResult.HostDir)
			// We deliberately keep the kit on disk between runs:
			// the docker sandbox we reuse across runs holds a hard
			// reference to the kit's bind-mount path — deleting the
			// dir would leave the sandbox un-startable. The kit lives
			// in the cache dir keyed on a content hash, so the next
			// run for the same agent overwrites it in place; disk
			// usage is bounded by the number of distinct agents the
			// user has run.
		}
	}

	printModelsGateway(cmd.OutOrStdout(), runConfig.ModelsGateway)

	name, err := backend.Ensure(ctx, wd, extras, template, configDir)
	if err != nil {
		return err
	}

	// Sandbox templates ship with a default-deny network policy that
	// allows the major model providers (api.anthropic.com, api.openai.com,
	// ...) but blocks every *.docker.com host. When the agent is
	// configured to talk to the Docker AI Gateway, allowlist that
	// hostname for this sandbox so the inner can actually reach it.
	allowGatewayHost(ctx, backend, name, runConfig.ModelsGateway)

	// Resolve env vars the agent needs and forward them into the sandbox.
	// Docker Desktop proxies well-known API keys automatically; this handles
	// any additional vars (e.g. MCP tool secrets).
	envFlags, envVars := sandbox.EnvForAgent(ctx, agentRef, envProvider)

	// Forward the gateway via the docker-agent process env (not as an
	// inline `-e KEY=VALUE` argument) so a gateway URL that happens to
	// carry credentials never leaks into the slog'd `docker sandbox
	// exec` argv.
	if gateway := runConfig.ModelsGateway; gateway != "" {
		envFlags = append(envFlags, "-e", envModelsGateway)
		envVars = append(envVars, envModelsGateway+"="+gateway)

		// Forward a *fresh* Docker Desktop token. We deliberately bypass
		// envProvider here: that chain consults the OS environment first,
		// where any pre-existing DOCKER_TOKEN value is by definition stale
		// (the gateway issues short-lived JWTs that expire roughly
		// hourly). Going straight to the Docker Desktop backend gives us
		// the same fresh token that [sandbox.StartTokenWriterIfNeeded]
		// will keep refreshing in the background; seeding it as an env
		// var lets the inner agent's startup check
		// ([config.CheckRequiredEnvVars]) succeed even on existing sandbox
		// images that read sandbox-tokens.json from the wrong path because
		// of the persistent-pre-run bug fixed in pkg/cli/flags.go.
		//
		// Like the gateway above, the token is forwarded by name only —
		// it would otherwise show up in the slog'd argv as plaintext.
		if token := desktop.GetToken(ctx); token != "" {
			envFlags = append(envFlags, "-e", environment.DockerDesktopTokenEnv)
			envVars = append(envVars, environment.DockerDesktopTokenEnv+"="+token)
		}
	}

	// Point the in-sandbox resolvers at the staged kit. We use the
	// `-e KEY=VALUE` form so the value is set directly inside the
	// container; we deliberately do not append it to envVars (which
	// would set it on the host docker CLI process too — a path that
	// only makes sense inside the sandbox).
	if kitResult != nil {
		envFlags = append(envFlags, "-e", skills.KitDirEnv+"="+kit.MountPath)
	}

	dockerCmd := backend.BuildExecCmd(ctx, name, wd, dockerAgentArgs, envFlags, envVars)
	slog.DebugContext(ctx, "Executing in sandbox", "name", name, "args", dockerCmd.Args)

	if err := dockerCmd.Run(); err != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			return cli.StatusError{StatusCode: exitErr.ExitCode()}
		}
		return fmt.Errorf("docker sandbox exec failed: %w", err)
	}

	return nil
}

func dockerAgentArgs(cmd *cobra.Command, args []string, configDir string) []string {
	skip := map[string]bool{
		"sandbox":    true,
		"sbx":        true,
		"config-dir": true,
		"no-kit":     true,
	}

	var dockerAgentArgs []string
	hasYolo := false
	cmd.Flags().Visit(func(f *pflag.Flag) {
		if skip[f.Name] {
			return
		}

		if f.Name == "yolo" {
			hasYolo = true
		}

		if f.Value.Type() == "bool" {
			dockerAgentArgs = append(dockerAgentArgs, "--"+f.Name)
		} else {
			dockerAgentArgs = append(dockerAgentArgs, "--"+f.Name, f.Value.String())
		}
	})
	if !hasYolo {
		dockerAgentArgs = append(dockerAgentArgs, "--yolo")
	}

	dockerAgentArgs = append(dockerAgentArgs, args...)
	dockerAgentArgs = append(dockerAgentArgs, "--config-dir", configDir)

	return dockerAgentArgs
}

// allowGatewayHost extracts the host[:port] from gatewayURL and asks
// the sandbox backend to add a per-sandbox allow-network rule for it.
// The default sandbox proxy denies *.docker.com, so without this the
// inner agent's first request to the gateway returns
// "403 Blocked by network policy".
//
// Best-effort: a malformed URL or a backend that doesn't support
// per-sandbox policies is logged at debug level and the run
// proceeds. The user will then see a network-policy 403 from the
// inner and we surface that diagnostic verbatim.
func allowGatewayHost(ctx context.Context, backend *sandbox.Backend, name, gatewayURL string) {
	if gatewayURL == "" {
		return
	}
	host := gatewayHostPort(gatewayURL)
	if host == "" {
		slog.DebugContext(ctx, "Could not extract host from models-gateway URL; not allowlisting",
			"gateway", gatewayURL)
		return
	}
	if err := backend.AllowHosts(ctx, name, []string{host}); err != nil {
		slog.WarnContext(ctx, "Failed to allowlist models-gateway host in sandbox; the inner agent may see HTTP 403",
			"sandbox", name, "host", host, "error", err)
	}
}

// gatewayHostPort returns the "host" or "host:port" portion of a
// gateway URL, or "" if rawURL doesn't carry a usable authority.
//
// We accept both fully formed URLs (https://example.com:443/proxy),
// scheme-relative authorities (//example.com), and bare authorities
// (example.com:443) so users can configure the gateway either way.
// IPv6 brackets, ports, paths, queries and fragments are stripped
// from the returned value; malformed inputs (no host, scheme without
// host, opaque non-HTTP schemes, etc.) return "".
func gatewayHostPort(rawURL string) string {
	if rawURL == "" {
		return ""
	}

	candidate := rawURL
	switch {
	case strings.HasPrefix(rawURL, "//"):
		// Protocol-relative authority — attach a fake scheme so url.Parse
		// recognises the host.
		candidate = "http:" + rawURL
	case hasURLScheme(rawURL):
		// Fully formed URL.
	case strings.Contains(rawURL, "://"):
		// Looks like a URL but the scheme part is bogus — reject.
		return ""
	case looksOpaque(rawURL):
		// e.g. "mailto:foo@example.com" — not a network endpoint.
		return ""
	default:
		// Bare authority. Strip trailing path / query / fragment up
		// front so url.Parse doesn't mistake "example.com:443/proxy"
		// for an opaque scheme.
		if i := strings.IndexAny(rawURL, "/?#"); i >= 0 {
			candidate = "http://" + rawURL[:i]
		} else {
			candidate = "http://" + rawURL
		}
	}

	u, err := url.Parse(candidate)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Host
}

// hasURLScheme reports whether s looks like "scheme://..." with a
// non-empty scheme. We deliberately don't accept "scheme:opaque"
// (no //) because the gateway URL is always an HTTP(S) endpoint.
func hasURLScheme(s string) bool {
	i := strings.Index(s, "://")
	if i <= 0 {
		return false
	}
	return isSchemeChars(s[:i])
}

// looksOpaque reports whether s looks like an opaque-form URI
// ("scheme:rest", no //) such as "mailto:foo" or "data:...". These
// don't carry a network authority and must not be re-parsed as bare
// host strings.
func looksOpaque(s string) bool {
	i := strings.Index(s, ":")
	if i <= 0 {
		return false
	}
	// Reject anything with a port-looking right side, so values like
	// "example.com:443" are treated as bare authorities, not opaque.
	rhs := s[i+1:]
	if rhs != "" && rhs[0] >= '0' && rhs[0] <= '9' {
		return false
	}
	return isSchemeChars(s[:i])
}

// isSchemeChars reports whether s is a syntactically valid URL scheme
// (RFC 3986: ALPHA *( ALPHA / DIGIT / "+" / "-" / "." ), starting with ALPHA).
func isSchemeChars(s string) bool {
	if s == "" {
		return false
	}
	first := s[0]
	if (first < 'a' || first > 'z') && (first < 'A' || first > 'Z') {
		return false
	}
	for _, r := range s[1:] {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '+' || r == '-' || r == '.':
			continue
		default:
			return false
		}
	}
	return true
}

// displayGatewayURL returns a credential-safe rendering of a gateway
// URL: any user/password embedded in the authority is replaced with
// "***" so it never lands in stdout, slog output, or screenshots.
// Inputs that don't parse cleanly (or carry no userinfo) are returned
// unchanged.
//
// We rebuild the string by hand instead of calling u.String() because
// url.User on the latter URL-escapes the masking characters into
// %2A%2A%2A, which is technically correct but uglier than "***" in a
// human-facing line.
func displayGatewayURL(rawURL string) string {
	if rawURL == "" {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.User == nil {
		return rawURL
	}

	var b strings.Builder
	if u.Scheme != "" {
		b.WriteString(u.Scheme)
		b.WriteString("://")
	}
	b.WriteString("***@")
	b.WriteString(u.Host)
	b.WriteString(u.RequestURI())
	if u.Fragment != "" {
		b.WriteByte('#')
		b.WriteString(u.Fragment)
	}
	return b.String()
}

// printModelsGateway prints which AI gateway (if any) the inner agent
// will route model requests through. Surfacing this between the kit
// summary and the sandbox-create line makes it obvious — at a glance
// — whether an "HTTP 403" the user might see later originated from
// the proxy's network policy (gateway host not yet allow-listed) or
// from the gateway server itself.
//
// An unset gateway means we did not configure routing through one;
// the inner agent will fall back to whatever the docker-agent process
// resolves from its own environment (DOCKER_TOKEN, OS env vars,
// Docker Model Runner, etc.).
func printModelsGateway(w io.Writer, gateway string) {
	if gateway == "" {
		fmt.Fprintln(w, "Models gateway: none configured")
		return
	}
	display := displayGatewayURL(gateway)
	host := gatewayHostPort(gateway)
	if host == "" || host == gateway {
		fmt.Fprintf(w, "Models gateway: %s\n", display)
		return
	}
	fmt.Fprintf(w, "Models gateway: %s (allowlisting %s in the sandbox proxy)\n", display, host)
}
