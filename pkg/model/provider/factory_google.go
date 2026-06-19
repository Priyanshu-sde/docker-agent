//go:build !js && !docker_agent_no_google

package provider

import (
	"context"

	"github.com/docker/docker-agent/pkg/config/latest"
	"github.com/docker/docker-agent/pkg/environment"
	"github.com/docker/docker-agent/pkg/model/provider/gemini"
	"github.com/docker/docker-agent/pkg/model/provider/options"
	"github.com/docker/docker-agent/pkg/model/provider/vertexai"
)

// Google is an optional provider. Build with `-tags docker_agent_no_google` to drop it and
// its transitive dependencies (google.golang.org/genai, the Vertex AI / cloud
// auth stack, and — via Vertex Model Garden — the anthropic and openai SDKs).
//
//nolint:gochecknoinits // Intentional: self-registration enables dropping this provider via build tags.
func init() {
	registerProviderFactory("google", googleFactory)
}

func googleFactory(ctx context.Context, cfg *latest.ModelConfig, env environment.Provider, opts ...options.Opt) (Provider, error) {
	// Route non-Gemini models on Vertex AI (Model Garden) through the
	// vertexai package, which picks the right endpoint per publisher.
	if vertexai.IsModelGardenConfig(cfg) {
		return vertexClientFactory(ctx, cfg, env, opts...)
	}
	return geminiClientFactory(ctx, cfg, env, opts...)
}

// geminiClientFactory and vertexClientFactory are the inner constructors used
// by googleFactory. They are package-level variables (rather than direct
// references to gemini.NewClient / vertexai.NewClient) so that tests can swap
// them with fakes via t.Cleanup and assert that googleFactory routes correctly
// based on vertexai.IsModelGardenConfig — without spinning up real clients.
var (
	geminiClientFactory providerFactory = func(ctx context.Context, cfg *latest.ModelConfig, env environment.Provider, opts ...options.Opt) (Provider, error) {
		return gemini.NewClient(ctx, cfg, env, opts...)
	}
	vertexClientFactory providerFactory = func(ctx context.Context, cfg *latest.ModelConfig, env environment.Provider, opts ...options.Opt) (Provider, error) {
		return vertexai.NewClient(ctx, cfg, env, opts...)
	}
)
