package config

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/docker/docker-agent/pkg/config/latest"
	"github.com/docker/docker-agent/pkg/config/types"
)

// captureWarnings runs fn with an isolated *slog.Logger and returns the
// emitted text. The logger is not registered as the process default, so
// parallel tests sharing slog.Default() are unaffected.
func captureWarnings(t *testing.T, fn func(ctx context.Context, logger *slog.Logger)) string {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	fn(t.Context(), logger)
	return buf.String()
}

func TestWarnExpansionMismatches_ShellSyntaxInJSField(t *testing.T) {
	t.Parallel()

	cfg := &latest.Config{
		Agents: []latest.AgentConfig{{
			Name:           "root",
			Description:    "Hi ${USER}",
			WelcomeMessage: "Welcome ${HOME}",
			Commands: types.Commands{
				"deploy": types.Command{Instruction: "Deploy ${PROJECT}"},
			},
		}},
	}

	out := captureWarnings(t, func(ctx context.Context, logger *slog.Logger) {
		warnExpansionMismatches(ctx, logger, cfg)
	})

	assert.Contains(t, out, "USER")
	assert.Contains(t, out, "HOME")
	assert.Contains(t, out, "PROJECT")
	assert.Contains(t, out, "shell-style")
}

func TestWarnExpansionMismatches_LowerCaseShellIdent(t *testing.T) {
	t.Parallel()

	cfg := &latest.Config{
		Agents: []latest.AgentConfig{{
			Name:        "root",
			Description: "Hi ${user}",
		}},
	}

	out := captureWarnings(t, func(ctx context.Context, logger *slog.Logger) {
		warnExpansionMismatches(ctx, logger, cfg)
	})

	assert.Contains(t, out, "user")
	assert.Contains(t, out, "shell-style")
}

func TestWarnExpansionMismatches_JSEnvInPathAndEnvNoLongerWarns(t *testing.T) {
	t.Parallel()

	// working_dir and path flow through pkg/path.ExpandPath, and toolset env
	// values flow through pkg/environment.Expand; both now accept ${env.X}, so
	// none of these must warn anymore (#2615).
	cfg := &latest.Config{
		Agents: []latest.AgentConfig{{
			Name: "root",
			Toolsets: []latest.Toolset{{
				Type:       "mcp",
				Command:    "x",
				WorkingDir: "${env.HOME}/work",
				Env: map[string]string{
					"MEM_DIR": "${env.MEM_DIR}/db",
				},
			}, {
				Type: "memory",
				Path: "${env.MEM_DIR}/db",
			}},
		}},
	}

	out := captureWarnings(t, func(ctx context.Context, logger *slog.Logger) {
		warnExpansionMismatches(ctx, logger, cfg)
	})

	assert.NotContains(t, out, "WARN")
}

func TestWarnExpansionMismatches_DoesNotLeakValueInExecField(t *testing.T) {
	t.Parallel()

	const secretToken = "supers3cret-token-DO-NOT-LOG"
	cfg := &latest.Config{
		Agents: []latest.AgentConfig{{
			Name: "root",
			Toolsets: []latest.Toolset{{
				Type: "script",
				Shell: map[string]latest.ScriptShellToolConfig{
					"build": {
						Cmd: "make",
						// Realistic shape: secret prefix followed by an
						// unsupported JS-style expansion in a verbatim-exec env
						// value. We must surface the variable name so users can
						// fix the typo, but we must not echo the secret.
						Env: map[string]string{
							"TOKEN": secretToken + "/${env.MEM_DIR}/db",
						},
					},
				},
			}},
		}},
	}

	out := captureWarnings(t, func(ctx context.Context, logger *slog.Logger) {
		warnExpansionMismatches(ctx, logger, cfg)
	})

	assert.Contains(t, out, "MEM_DIR")
	assert.NotContains(t, out, secretToken, "warning must not include the field's full value")
}

func TestWarnExpansionMismatches_NoFalsePositives(t *testing.T) {
	t.Parallel()

	cfg := &latest.Config{
		Agents: []latest.AgentConfig{{
			Name:           "root",
			Description:    "Hello ${env.USER || 'guest'}",
			WelcomeMessage: "",
			Instruction:    "Use ${env.PROJECT} carefully",
			Commands: types.Commands{
				"greet": types.Command{Instruction: "Hello ${env.USER}"},
			},
			Toolsets: []latest.Toolset{{
				Type:       "mcp",
				Command:    "x",
				WorkingDir: "~/work/${PROJECT}",
				Headers: map[string]string{
					"Authorization": "Bearer ${env.TOKEN}",
				},
				Env: map[string]string{
					"FOO": "${BAR}",
				},
			}, {
				Type: "memory",
				Path: "$HOME/db",
			}},
		}},
	}

	out := captureWarnings(t, func(ctx context.Context, logger *slog.Logger) {
		warnExpansionMismatches(ctx, logger, cfg)
	})

	if strings.Contains(out, "WARN") {
		t.Errorf("unexpected warnings emitted: %s", out)
	}
}

func TestWarnExpansionMismatches_HeaderMixedWithEnv(t *testing.T) {
	t.Parallel()

	// When a value already references ${env.X}, we treat any other ${...} as
	// intentional JS so we don't second-guess legitimate template literals.
	cfg := &latest.Config{
		Agents: []latest.AgentConfig{{
			Name: "root",
			Toolsets: []latest.Toolset{{
				Type: "openapi",
				URL:  "https://x",
				Headers: map[string]string{
					"X": "${env.A} ${B}",
				},
			}},
		}},
	}

	out := captureWarnings(t, func(ctx context.Context, logger *slog.Logger) {
		warnExpansionMismatches(ctx, logger, cfg)
	})

	assert.NotContains(t, out, "WARN")
}

func TestWarnExpansionMismatches_APIConfigHeaders(t *testing.T) {
	t.Parallel()

	cfg := &latest.Config{
		Agents: []latest.AgentConfig{{
			Name: "root",
			Toolsets: []latest.Toolset{{
				Type: "api",
				APIConfig: latest.APIToolConfig{
					Endpoint: "https://api.example.com/${USER_ID}",
					Headers: map[string]string{
						"Authorization": "Bearer ${TOKEN}",
					},
				},
			}},
		}},
	}

	out := captureWarnings(t, func(ctx context.Context, logger *slog.Logger) {
		warnExpansionMismatches(ctx, logger, cfg)
	})

	assert.Contains(t, out, "USER_ID")
	assert.Contains(t, out, "TOKEN")
	assert.Contains(t, out, "shell-style")
}

func TestWarnExpansionMismatches_ScriptShellTool(t *testing.T) {
	t.Parallel()

	cfg := &latest.Config{
		Agents: []latest.AgentConfig{{
			Name: "root",
			Toolsets: []latest.Toolset{{
				Type: "script",
				Shell: map[string]latest.ScriptShellToolConfig{
					"build": {
						Cmd:        "make",
						WorkingDir: "${env.WORK}/repo",
						Env: map[string]string{
							"FOO": "${env.BAR}",
						},
					},
				},
			}},
		}},
	}

	out := captureWarnings(t, func(ctx context.Context, logger *slog.Logger) {
		warnExpansionMismatches(ctx, logger, cfg)
	})

	assert.Contains(t, out, "WORK")
	assert.Contains(t, out, "BAR")
	assert.Contains(t, out, "shell[build]")
}

func TestWarnExpansionMismatches_Hooks(t *testing.T) {
	t.Parallel()

	cfg := &latest.Config{
		Agents: []latest.AgentConfig{{
			Name: "root",
			Hooks: &latest.HooksConfig{
				PreToolUse: []latest.HookMatcherConfig{{
					Matcher: "*",
					Hooks: []latest.HookDefinition{{
						Type:       "command",
						Command:    "echo",
						WorkingDir: "${env.HOME}/hooks",
						Env: map[string]string{
							"X": "${env.Y}",
						},
					}},
				}},
			},
		}},
	}

	out := captureWarnings(t, func(ctx context.Context, logger *slog.Logger) {
		warnExpansionMismatches(ctx, logger, cfg)
	})

	assert.Contains(t, out, "HOME")
	assert.Contains(t, out, "hooks.")
}
