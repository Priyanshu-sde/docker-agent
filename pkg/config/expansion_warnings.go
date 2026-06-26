package config

import (
	"context"
	"log/slog"
	"regexp"
	"strconv"
	"strings"

	"github.com/docker/docker-agent/pkg/config/latest"
)

// shellEnvVarRef matches a `${IDENT}` where IDENT looks like an environment
// variable name accepted by os.Expand (letters, digits, underscores; leading
// non-digit). Used to flag shell-style references that appear in JS-template
// fields, where they will be silently passed through as literals instead of
// expanded.
var shellEnvVarRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// jsEnvRef matches the JS-template form `${env.X}`. The leading `\$\{` is
// followed by optional whitespace and then `env.IDENT`; we deliberately do
// not anchor the closing brace so the match also fires for expressions like
// `${env.X || 'fallback'}`.
var jsEnvRef = regexp.MustCompile(`\$\{\s*env\.[A-Za-z_][A-Za-z0-9_]*`)

// jsEnvRefStrict matches a `${env.X}` reference with no extra JS expression
// trailing the identifier. Used to flag occurrences in the ScriptShell and
// hook fields (env, working_dir) that are forwarded verbatim to exec.Cmd
// without any expansion, so the literal `${env.X}` is passed through.
var jsEnvRefStrict = regexp.MustCompile(`\$\{\s*env\.([A-Za-z_][A-Za-z0-9_]*)\s*\}`)

// warnExpansionMismatches scans a loaded config for fields whose contents use
// the wrong variable-expansion syntax for that field. Two incompatible
// syntaxes coexist today (issue #2615):
//
//   - JS template literals (`${env.X}`) for prompt/instruction/header/command
//     fields rendered through pkg/js.
//   - Shell-style (`$VAR` / `${VAR}` / `~`) for the ScriptShell/hook fields
//     that are forwarded verbatim to exec.Cmd.
//
// Toolset path (working_dir, path) and env-value fields are not checked here:
// they flow through pkg/path.ExpandPath / pkg/environment.Expand, both of
// which now accept ${env.X} as an alias for ${X} (#2615).
//
// Mixing them up currently fails silently; we emit warnings to make the
// problem visible without changing runtime behavior.
//
// The logger is injected (rather than read from slog.Default()) so tests can
// capture warnings without racing with other goroutines that share the
// global default logger.
func warnExpansionMismatches(ctx context.Context, logger *slog.Logger, cfg *latest.Config) {
	if logger == nil {
		logger = slog.Default()
	}
	for i := range cfg.Agents {
		a := &cfg.Agents[i]
		warnJSField(ctx, logger, a.Name, "description", a.Description)
		warnJSField(ctx, logger, a.Name, "welcome_message", a.WelcomeMessage)
		warnJSField(ctx, logger, a.Name, "instruction", a.Instruction)

		for name, cmd := range a.Commands {
			warnJSField(ctx, logger, a.Name, "commands."+name+".instruction", cmd.Instruction)
			warnJSField(ctx, logger, a.Name, "commands."+name+".description", cmd.Description)
		}

		for j := range a.Toolsets {
			t := &a.Toolsets[j]
			loc := agentToolsetLocation(a.Name, t, j)

			warnJSField(ctx, logger, loc, "instruction", t.Instruction)
			for k, v := range t.Headers {
				warnJSField(ctx, logger, loc, "headers."+k, v)
			}
			for k, v := range t.Remote.Headers {
				warnJSField(ctx, logger, loc, "remote.headers."+k, v)
			}

			// APIConfig fields (api toolset): endpoint and headers go through
			// the JS expander; instruction is rendered as plain text but the
			// agent still sees it, so a `${VAR}` typo there is silently broken.
			warnJSField(ctx, logger, loc, "api_config.endpoint", t.APIConfig.Endpoint)
			warnJSField(ctx, logger, loc, "api_config.instruction", t.APIConfig.Instruction)
			for k, v := range t.APIConfig.Headers {
				warnJSField(ctx, logger, loc, "api_config.headers."+k, v)
			}

			// Toolset env values, working_dir and path are not checked: they
			// flow through pkg/environment.Expand / pkg/path.ExpandPath, both of
			// which accept ${env.X} as an alias for ${X} (#2615).
			for name, sh := range t.Shell {
				shellLoc := loc + " shell[" + name + "]"
				// ScriptShellToolConfig env and working_dir are forwarded
				// directly to exec.Cmd without expansion, so neither syntax
				// works there. Flag the JS form because that's the
				// surprising silent failure (it looks template-y).
				warnPathField(ctx, logger, shellLoc, "working_dir", sh.WorkingDir)
				for k, v := range sh.Env {
					warnPathField(ctx, logger, shellLoc, "env."+k, v)
				}
			}
		}

		warnHooksConfig(ctx, logger, a.Name, a.Hooks)
	}
}

// warnHooksConfig walks every hook list on HooksConfig and warns about
// silently-broken expansions in fields the runtime forwards verbatim
// (`env`, `working_dir`).
func warnHooksConfig(ctx context.Context, logger *slog.Logger, agentName string, h *latest.HooksConfig) {
	if h == nil {
		return
	}
	flat := []struct {
		name  string
		hooks []latest.HookDefinition
	}{
		{"session_start", h.SessionStart},
		{"user_prompt_submit", h.UserPromptSubmit},
		{"turn_start", h.TurnStart},
		{"turn_end", h.TurnEnd},
		{"before_llm_call", h.BeforeLLMCall},
		{"after_llm_call", h.AfterLLMCall},
		{"session_end", h.SessionEnd},
		{"pre_compact", h.PreCompact},
		{"subagent_stop", h.SubagentStop},
		{"on_user_input", h.OnUserInput},
		{"stop", h.Stop},
		{"notification", h.Notification},
		{"on_error", h.OnError},
		{"on_max_iterations", h.OnMaxIterations},
		{"on_agent_switch", h.OnAgentSwitch},
		{"on_session_resume", h.OnSessionResume},
		{"on_tool_approval_decision", h.OnToolApprovalDecision},
		{"before_compaction", h.BeforeCompaction},
		{"after_compaction", h.AfterCompaction},
	}
	for _, g := range flat {
		for i := range g.hooks {
			warnHookDefinition(ctx, logger, agentName, g.name, i, &g.hooks[i])
		}
	}

	matched := []struct {
		name     string
		matchers []latest.HookMatcherConfig
	}{
		{"pre_tool_use", h.PreToolUse},
		{"post_tool_use", h.PostToolUse},
		{"permission_request", h.PermissionRequest},
		{"tool_response_transform", h.ToolResponseTransform},
	}
	for _, g := range matched {
		for mi := range g.matchers {
			for i := range g.matchers[mi].Hooks {
				name := g.name + "[" + strconv.Itoa(mi) + "]"
				warnHookDefinition(ctx, logger, agentName, name, i, &g.matchers[mi].Hooks[i])
			}
		}
	}
}

func warnHookDefinition(ctx context.Context, logger *slog.Logger, agentName, group string, idx int, hook *latest.HookDefinition) {
	loc := "agent " + agentName + " hooks." + group + "[" + strconv.Itoa(idx) + "]"
	warnPathField(ctx, logger, loc, "working_dir", hook.WorkingDir)
	for k, v := range hook.Env {
		warnPathField(ctx, logger, loc, "env."+k, v)
	}
}

func agentToolsetLocation(agentName string, t *latest.Toolset, idx int) string {
	kind := t.Type
	if kind == "" {
		kind = "?"
	}
	return "agent " + agentName + " toolset[" + strconv.Itoa(idx) + "] (" + kind + ")"
}

// warnJSField warns when a JS-template field contains a `${IDENT}` reference
// that isn't a `${env.X}` expression and no `${env.X}` appears elsewhere in
// the same value. Such references are kept literal at runtime instead of
// being expanded.
func warnJSField(ctx context.Context, logger *slog.Logger, loc, field, value string) {
	if value == "" || !strings.Contains(value, "${") {
		return
	}
	if jsEnvRef.MatchString(value) {
		// Has a real ${env.X}; assume any other ${...} is intentional JS.
		return
	}
	for _, m := range shellEnvVarRef.FindAllStringSubmatch(value, -1) {
		logger.WarnContext(ctx,
			"shell-style ${VAR} in JS-expanded field will not be substituted; use ${env.VAR}",
			"location", loc,
			"field", field,
			"variable", m[1],
			"see", "https://github.com/docker/docker-agent/issues/2615",
		)
	}
}

// warnPathField warns when a verbatim-exec field (ScriptShell/hook env and
// working_dir) contains a `${env.X}` reference. Those fields are forwarded
// directly to exec.Cmd without expansion, so the reference is passed through
// literally. The variable name is captured but the surrounding value is not,
// since env values frequently contain credentials.
func warnPathField(ctx context.Context, logger *slog.Logger, loc, field, value string) {
	if value == "" || !strings.Contains(value, "${env.") {
		return
	}
	for _, m := range jsEnvRefStrict.FindAllStringSubmatch(value, -1) {
		logger.WarnContext(ctx,
			"JS-style ${env.X} in shell-expanded field will not be substituted; use ${X} or $X",
			"location", loc,
			"field", field,
			"variable", m[1],
			"see", "https://github.com/docker/docker-agent/issues/2615",
		)
	}
}
