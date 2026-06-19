//go:build !js && !docker_agent_no_anthropic

package provider

import (
	"context"

	"github.com/docker/docker-agent/pkg/config/latest"
	"github.com/docker/docker-agent/pkg/environment"
	"github.com/docker/docker-agent/pkg/model/provider/anthropic"
	"github.com/docker/docker-agent/pkg/model/provider/options"
)

// Anthropic is an optional provider. Build with `-tags docker_agent_no_anthropic` to drop
// it and its transitive dependencies (github.com/anthropics/anthropic-sdk-go).
//
// Note: the google provider's Vertex Model Garden support imports the
// anthropic package directly, so `docker_agent_no_anthropic` only removes the dependency
// when the google provider is also dropped (`-tags docker_agent_no_google`).
//
//nolint:gochecknoinits // Intentional: self-registration enables dropping this provider via build tags.
func init() {
	registerProviderFactory("anthropic", anthropicFactory)
}

func anthropicFactory(ctx context.Context, cfg *latest.ModelConfig, env environment.Provider, opts ...options.Opt) (Provider, error) {
	return anthropic.NewClient(ctx, cfg, env, opts...)
}
