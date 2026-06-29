package provider

import (
	"context"

	"github.com/docker/docker-agent/pkg/config/latest"
	"github.com/docker/docker-agent/pkg/environment"
	"github.com/docker/docker-agent/pkg/model/provider/options"
	"github.com/docker/docker-agent/pkg/model/provider/vertexai"
)

type providerFactory = Factory

// googleFactoryWith builds a google dispatch factory that routes Model Garden
// configs to vertex and everything else to gemini. Tests construct one per
// case instead of swapping package-level factory variables.
func googleFactoryWith(gemini, vertex providerFactory) providerFactory {
	return func(ctx context.Context, cfg *latest.ModelConfig, env environment.Provider, opts ...options.Opt) (Provider, error) {
		if vertexai.IsModelGardenConfig(cfg) {
			return vertex(ctx, cfg, env, opts...)
		}
		return gemini(ctx, cfg, env, opts...)
	}
}
