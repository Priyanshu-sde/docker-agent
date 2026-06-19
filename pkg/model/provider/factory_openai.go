//go:build !js && !docker_agent_no_openai

package provider

import (
	"context"

	"github.com/docker/docker-agent/pkg/config/latest"
	"github.com/docker/docker-agent/pkg/environment"
	"github.com/docker/docker-agent/pkg/model/provider/openai"
	"github.com/docker/docker-agent/pkg/model/provider/options"
)

// OpenAI is an optional provider. Build with `-tags docker_agent_no_openai` to drop it and
// its transitive dependencies (github.com/openai/openai-go).
//
//nolint:gochecknoinits // Intentional: self-registration enables dropping this provider via build tags.
func init() {
	registerProviderFactory("openai", openaiFactory)
	registerProviderFactory("openai_chatcompletions", openaiFactory)
	registerProviderFactory("openai_responses", openaiFactory)
}

func openaiFactory(ctx context.Context, cfg *latest.ModelConfig, env environment.Provider, opts ...options.Opt) (Provider, error) {
	return openai.NewClient(ctx, cfg, env, opts...)
}
