//go:build !js

package provider

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/docker/docker-agent/pkg/config/latest"
	"github.com/docker/docker-agent/pkg/environment"
	"github.com/docker/docker-agent/pkg/model/provider/dmr"
	"github.com/docker/docker-agent/pkg/model/provider/options"
	"github.com/docker/docker-agent/pkg/model/provider/rulebased"
)

// createRuleBasedRouter creates a rule-based routing provider.
func createRuleBasedRouter(ctx context.Context, cfg *latest.ModelConfig, models map[string]latest.ModelConfig, env environment.Provider, opts ...options.Opt) (Provider, error) {
	return rulebased.NewClient(ctx, cfg, models, env, resolveRoutedModel, opts...)
}

// resolveRoutedModel is the rulebased.ProviderFactory used by
// createRuleBasedRouter. It resolves a routing target — which is either a name
// from the models map or an inline "provider/model" spec — and returns the
// provider for it. Routing targets cannot themselves have routing rules.
//
// Defined as a package-level function (rather than an inline closure) so the
// recursion-prevention, parse-error and factory-error paths can be unit-tested
// directly without going through rulebased.NewClient.
func resolveRoutedModel(
	ctx context.Context,
	modelSpec string,
	models map[string]latest.ModelConfig,
	env environment.Provider,
	factoryOpts ...options.Opt,
) (rulebased.Provider, error) {
	// Check if modelSpec is a reference to a model in the models map.
	if modelCfg, exists := models[modelSpec]; exists {
		// Prevent infinite recursion - referenced models cannot have routing rules.
		if len(modelCfg.Routing) > 0 {
			return nil, fmt.Errorf("model %q has routing rules and cannot be used as a routing target", modelSpec)
		}
		return createDirectProvider(ctx, &modelCfg, env, factoryOpts...)
	}

	// Otherwise, treat as an inline model spec (e.g., "openai/gpt-4o").
	inlineCfg, parseErr := latest.ParseModelRef(modelSpec)
	if parseErr != nil {
		return nil, fmt.Errorf("invalid model spec %q: expected 'provider/model' format or a model reference", modelSpec)
	}
	return createDirectProvider(ctx, &inlineCfg, env, factoryOpts...)
}

// createDirectProvider creates a provider without routing (direct model access).
func createDirectProvider(ctx context.Context, cfg *latest.ModelConfig, env environment.Provider, opts ...options.Opt) (Provider, error) {
	var globalOptions options.ModelOptions
	for _, opt := range opts {
		opt(&globalOptions)
	}

	// Apply defaults from custom providers (from config) or built-in aliases
	enhancedCfg := applyProviderDefaults(cfg, globalOptions.Providers())

	providerType := resolveProviderType(enhancedCfg)

	factory, ok := providerFactories[providerType]
	if !ok {
		slog.ErrorContext(ctx, "Unknown provider type", "type", providerType)
		return nil, unknownProviderError(providerType)
	}
	return factory(ctx, enhancedCfg, env, opts...)
}

// unknownProviderError builds the error returned when no factory is registered
// for providerType. The provider may be genuinely unknown, or it may have been
// compiled out via a `no_<provider>` build tag (openai/anthropic/google/bedrock
// are optional). The message hints at the latter so trimmed builds are easy to
// diagnose.
func unknownProviderError(providerType string) error {
	return fmt.Errorf("unknown provider type %q (it may not be compiled into this build)", providerType)
}

// providerFactory builds a Provider from a fully-resolved ModelConfig.
// Tests may swap entries in providerFactories to exercise dispatch logic
// without spinning up real provider clients.
type providerFactory func(ctx context.Context, cfg *latest.ModelConfig, env environment.Provider, opts ...options.Opt) (Provider, error)

// providerFactories maps a resolved provider type (the value returned by
// resolveProviderType) to its constructor.
//
// Entries are contributed by per-provider files via registerProviderFactory in
// their init(). The optional providers (openai, anthropic, google,
// amazon-bedrock) each live behind a `no_<provider>` build tag so embedders can
// drop them — and their transitive SDK dependencies — from a build. The map is
// package-private but modifiable; tests must restore the original entries with
// t.Cleanup.
var providerFactories = map[string]providerFactory{}

// registerProviderFactory records f under name in providerFactories. It is
// called from the init() of each per-provider file; duplicate registration of
// the same name panics to surface wiring mistakes at startup.
func registerProviderFactory(name string, f providerFactory) {
	if _, exists := providerFactories[name]; exists {
		panic("provider: duplicate factory registration for " + name)
	}
	providerFactories[name] = f
}

//nolint:gochecknoinits // Intentional: providers self-register so optional ones can be dropped via build tags.
func init() {
	registerProviderFactory("dmr", dmrFactory)
}

func dmrFactory(ctx context.Context, cfg *latest.ModelConfig, _ environment.Provider, opts ...options.Opt) (Provider, error) {
	return dmr.NewClient(ctx, cfg, opts...)
}
