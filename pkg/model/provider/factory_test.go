package provider

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/chat"
	"github.com/docker/docker-agent/pkg/config/latest"
	"github.com/docker/docker-agent/pkg/environment"
	"github.com/docker/docker-agent/pkg/model/provider/base"
	"github.com/docker/docker-agent/pkg/model/provider/options"
	"github.com/docker/docker-agent/pkg/modelsdev"
	"github.com/docker/docker-agent/pkg/tools"
)

// fakeProvider is a Provider stub used to verify factory dispatch.
type fakeProvider struct {
	id modelsdev.ID
}

func (f *fakeProvider) ID() modelsdev.ID { return f.id }
func (f *fakeProvider) CreateChatCompletionStream(_ context.Context, _ []chat.Message, _ []tools.Tool) (chat.MessageStream, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeProvider) BaseConfig() base.Config { return base.Config{} }

func tagFactory(id string) providerFactory {
	return func(_ context.Context, _ *latest.ModelConfig, _ environment.Provider, _ ...options.Opt) (Provider, error) {
		return &fakeProvider{id: modelsdev.NewID("test", id)}, nil
	}
}

// TestCreateDirectProvider_DispatchByType verifies that resolveProviderType's
// output is mapped to the right factory entry for every supported value,
// including the OpenAI api_type aliases.
func TestCreateDirectProvider_DispatchByType(t *testing.T) {
	t.Parallel()
	r := NewRegistry(map[string]providerFactory{
		"openai":                 tagFactory("openai"),
		"openai_chatcompletions": tagFactory("openai_chatcompletions"),
		"openai_responses":       tagFactory("openai_responses"),
		"anthropic":              tagFactory("anthropic"),
		"google":                 tagFactory("google"),
		"dmr":                    tagFactory("dmr"),
		"amazon-bedrock":         tagFactory("amazon-bedrock"),
	})

	tests := []struct {
		name     string
		cfg      *latest.ModelConfig
		expectID string
	}{
		{
			name:     "openai",
			cfg:      &latest.ModelConfig{Provider: "openai", Model: "gpt-4o"},
			expectID: "openai",
		},
		{
			name:     "openai_chatcompletions via api_type override",
			cfg:      &latest.ModelConfig{Provider: "openai", Model: "gpt-4o", ProviderOpts: map[string]any{"api_type": "openai_chatcompletions"}},
			expectID: "openai_chatcompletions",
		},
		{
			name:     "openai_responses via api_type override",
			cfg:      &latest.ModelConfig{Provider: "openai", Model: "gpt-5", ProviderOpts: map[string]any{"api_type": "openai_responses"}},
			expectID: "openai_responses",
		},
		{
			name:     "anthropic",
			cfg:      &latest.ModelConfig{Provider: "anthropic", Model: "claude-sonnet-4-0"},
			expectID: "anthropic",
		},
		{
			name:     "google",
			cfg:      &latest.ModelConfig{Provider: "google", Model: "gemini-2.5-flash"},
			expectID: "google",
		},
		{
			name:     "dmr",
			cfg:      &latest.ModelConfig{Provider: "dmr", Model: "ai/llama3.2"},
			expectID: "dmr",
		},
		{
			name:     "amazon-bedrock",
			cfg:      &latest.ModelConfig{Provider: "amazon-bedrock", Model: "anthropic.claude-3-sonnet"},
			expectID: "amazon-bedrock",
		},
		{
			name:     "alias resolves to openai",
			cfg:      &latest.ModelConfig{Provider: "mistral", Model: "mistral-large-latest"},
			expectID: "openai",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p, err := r.createDirectProvider(t.Context(), tt.cfg, environment.NewNoEnvProvider())
			require.NoError(t, err)
			leaf := unwrapProvider(p)
			fp, ok := leaf.(*fakeProvider)
			require.True(t, ok, "expected fakeProvider, got %T", leaf)
			assert.Equal(t, tt.expectID, fp.id.Model)
		})
	}
}

// TestCreateDirectProvider_UnknownProviderType verifies the previously
// unreachable error branch when the resolved provider type is not registered.
func TestCreateDirectProvider_UnknownProviderType(t *testing.T) {
	t.Parallel()
	r := NewRegistry(map[string]providerFactory{
		"openai": tagFactory("openai"),
	})

	cfg := &latest.ModelConfig{Provider: "completely-unknown", Model: "x"}

	_, err := r.createDirectProvider(t.Context(), cfg, environment.NewNoEnvProvider())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown provider type")
	assert.Contains(t, err.Error(), "completely-unknown")
}

// TestCreateDirectProvider_FactoryError ensures errors returned by a factory
// are propagated unchanged to the caller.
func TestCreateDirectProvider_FactoryError(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("boom")
	r := NewRegistry(map[string]providerFactory{
		"openai": func(_ context.Context, _ *latest.ModelConfig, _ environment.Provider, _ ...options.Opt) (Provider, error) {
			return nil, sentinel
		},
	})

	_, err := r.createDirectProvider(t.Context(), &latest.ModelConfig{Provider: "openai", Model: "gpt-4o"}, environment.NewNoEnvProvider())
	require.ErrorIs(t, err, sentinel)
}

// TestCreateDirectProvider_AppliesProviderDefaults verifies that the registry
// receives the *enhanced* config (i.e. applyProviderDefaults has run) before
// dispatch — the BaseURL from a custom provider must be visible to the factory.
func TestCreateDirectProvider_AppliesProviderDefaults(t *testing.T) {
	t.Parallel()
	var got *latest.ModelConfig
	r := NewRegistry(map[string]providerFactory{
		"openai_chatcompletions": func(_ context.Context, cfg *latest.ModelConfig, _ environment.Provider, _ ...options.Opt) (Provider, error) {
			got = cfg
			return &fakeProvider{id: modelsdev.NewID("test", "captured")}, nil
		},
	})

	customProviders := map[string]latest.ProviderConfig{
		"my_gateway": {
			APIType:  "openai_chatcompletions",
			BaseURL:  "https://api.gateway.example/v1",
			TokenKey: "GW_TOKEN",
		},
	}

	cfg := &latest.ModelConfig{Provider: "my_gateway", Model: "gpt-4o"}

	_, err := r.createDirectProvider(
		t.Context(), cfg, environment.NewNoEnvProvider(),
		options.WithProviders(customProviders),
	)
	require.NoError(t, err)

	require.NotNil(t, got)
	assert.Equal(t, "https://api.gateway.example/v1", got.BaseURL, "factory should receive enhanced BaseURL")
	assert.Equal(t, "GW_TOKEN", got.TokenKey)
	assert.Equal(t, "openai_chatcompletions", got.ProviderOpts["api_type"])
}

// TestCreateDirectProvider_BypassModelsGateway verifies that a model with
// bypass_models_gateway set clears the gateway option before the leaf factory
// runs, so the provider dials its endpoint directly. Models without the flag
// keep the gateway.
func TestCreateDirectProvider_BypassModelsGateway(t *testing.T) {
	t.Parallel()

	const gateway = "https://gateway.example.com"

	tests := []struct {
		name        string
		bypass      bool
		wantGateway string
	}{
		{name: "bypass clears gateway", bypass: true, wantGateway: ""},
		{name: "no bypass keeps gateway", bypass: false, wantGateway: gateway},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var gotOpts options.ModelOptions
			r := NewRegistry(map[string]providerFactory{
				"openai": func(_ context.Context, _ *latest.ModelConfig, _ environment.Provider, opts ...options.Opt) (Provider, error) {
					for _, opt := range opts {
						opt(&gotOpts)
					}
					return &fakeProvider{id: modelsdev.NewID("test", "captured")}, nil
				},
			})

			cfg := &latest.ModelConfig{Provider: "openai", Model: "gpt-4o", BypassModelsGateway: tt.bypass}

			_, err := r.createDirectProvider(
				t.Context(), cfg, environment.NewNoEnvProvider(),
				options.WithGateway(gateway),
			)
			require.NoError(t, err)
			assert.Equal(t, tt.wantGateway, gotOpts.Gateway())
		})
	}
}

// TestNewWithModels_BypassModelsGatewayRouting verifies that a routing model
// with bypass_models_gateway propagates the bypass to its fallback and routed
// targets (the router itself makes no HTTP calls).
func TestNewWithModels_BypassModelsGatewayRouting(t *testing.T) {
	t.Parallel()

	const gateway = "https://gateway.example.com"

	tests := []struct {
		name        string
		bypass      bool
		wantGateway string
	}{
		{name: "router bypass clears gateway for children", bypass: true, wantGateway: ""},
		{name: "router without bypass keeps gateway for children", bypass: false, wantGateway: gateway},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var gateways []string
			r := NewRegistry(map[string]providerFactory{
				"openai": func(_ context.Context, _ *latest.ModelConfig, _ environment.Provider, opts ...options.Opt) (Provider, error) {
					var probe options.ModelOptions
					for _, opt := range opts {
						if opt != nil {
							opt(&probe)
						}
					}
					gateways = append(gateways, probe.Gateway())
					return &fakeProvider{id: modelsdev.NewID("openai", "captured")}, nil
				},
			})

			cfg := &latest.ModelConfig{
				Provider:            "openai",
				Model:               "gpt-4o",
				BypassModelsGateway: tt.bypass,
				Routing:             []latest.RoutingRule{{Model: "openai/gpt-4o-mini", Examples: []string{"hi"}}},
			}

			_, err := r.NewWithModels(
				t.Context(), cfg, nil, environment.NewNoEnvProvider(),
				options.WithGateway(gateway),
			)
			require.NoError(t, err)
			// One call for the fallback, one for the routed target.
			require.Len(t, gateways, 2)
			for _, g := range gateways {
				assert.Equal(t, tt.wantGateway, g)
			}
		})
	}
}
