//go:build !js && !docker_agent_no_bedrock

package provider

import (
	"context"

	"github.com/docker/docker-agent/pkg/config/latest"
	"github.com/docker/docker-agent/pkg/environment"
	"github.com/docker/docker-agent/pkg/model/provider/bedrock"
	"github.com/docker/docker-agent/pkg/model/provider/options"
)

// Amazon Bedrock is an optional provider. Build with `-tags docker_agent_no_bedrock` to drop
// it and its transitive dependencies (the github.com/aws/aws-sdk-go-v2 stack),
// which is the largest provider-specific dependency tree.
//
//nolint:gochecknoinits // Intentional: self-registration enables dropping this provider via build tags.
func init() {
	registerProviderFactory("amazon-bedrock", bedrockFactory)
}

func bedrockFactory(ctx context.Context, cfg *latest.ModelConfig, env environment.Provider, opts ...options.Opt) (Provider, error) {
	return bedrock.NewClient(ctx, cfg, env, opts...)
}
