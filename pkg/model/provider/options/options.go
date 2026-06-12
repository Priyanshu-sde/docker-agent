package options

import (
	"net/http"

	"github.com/docker/docker-agent/pkg/config/latest"
	"github.com/docker/docker-agent/pkg/modelsdev"
)

type ModelOptions struct {
	gateway          string
	structuredOutput *latest.StructuredOutput
	generatingTitle  bool
	noThinking       bool
	maxTokens        int64
	providers        map[string]latest.ProviderConfig
	modelsDevStore   *modelsdev.Store
	transportWrapper func(http.RoundTripper) http.RoundTripper
}

func (c *ModelOptions) Gateway() string {
	return c.gateway
}

func (c *ModelOptions) StructuredOutput() *latest.StructuredOutput {
	return c.structuredOutput
}

func (c *ModelOptions) GeneratingTitle() bool {
	return c.generatingTitle
}

func (c *ModelOptions) MaxTokens() int64 {
	return c.maxTokens
}

func (c *ModelOptions) NoThinking() bool {
	return c.noThinking
}

func (c *ModelOptions) Providers() map[string]latest.ProviderConfig {
	return c.providers
}

func (c *ModelOptions) ModelsDevStore() *modelsdev.Store {
	return c.modelsDevStore
}

// TransportWrapper returns the HTTP transport wrapper function registered via
// WithHTTPTransportWrapper, or nil if none was set.
func (c *ModelOptions) TransportWrapper() func(http.RoundTripper) http.RoundTripper {
	return c.transportWrapper
}

type Opt func(*ModelOptions)

func WithGateway(gateway string) Opt {
	return func(cfg *ModelOptions) {
		cfg.gateway = gateway
	}
}

func WithStructuredOutput(structuredOutput *latest.StructuredOutput) Opt {
	return func(cfg *ModelOptions) {
		cfg.structuredOutput = structuredOutput
	}
}

func WithGeneratingTitle() Opt {
	return func(cfg *ModelOptions) {
		cfg.generatingTitle = true
	}
}

func WithMaxTokens(maxTokens int64) Opt {
	return func(cfg *ModelOptions) {
		cfg.maxTokens = maxTokens
	}
}

func WithNoThinking() Opt {
	return func(cfg *ModelOptions) {
		cfg.noThinking = true
	}
}

func WithProviders(providers map[string]latest.ProviderConfig) Opt {
	return func(cfg *ModelOptions) {
		cfg.providers = providers
	}
}

func WithModelsDevStore(store *modelsdev.Store) Opt {
	return func(cfg *ModelOptions) {
		cfg.modelsDevStore = store
	}
}

// WithHTTPTransportWrapper registers a function that wraps the HTTP transport
// used by provider clients (Anthropic, OpenAI, and Gemini with the Gemini API
// backend). The function receives the transport that docker-agent built
// (including OTel instrumentation, SSE decompression fix, and Desktop proxy
// support) and must return a new RoundTripper that delegates to it. The wrapper
// is applied in both direct mode and gateway/proxy mode.
//
// Call-frequency note: in direct mode the wrapper is invoked once at client
// construction time; in gateway mode it is invoked on every LLM request
// (because gateway clients are rebuilt on each call to refresh short-lived
// auth tokens). Wrappers with per-call side effects (metrics, token rotation)
// will therefore be called more frequently in gateway mode.
//
// Limitations:
//   - OpenAI clients configured with transport=websocket bypass the HTTP
//     transport layer entirely; the wrapper is not applied in that mode.
//   - Gemini clients using the Vertex AI backend (project/location config or
//     GOOGLE_GENAI_USE_VERTEXAI) rely on the genai SDK's default HTTP client;
//     the wrapper is not applied and a warning is logged.
//
// The wrapper function must return a non-nil RoundTripper; returning nil is a
// no-op (a warning is logged and the original transport is kept).
//
// Example — inject a bearer token on every outbound LLM request:
//
//	options.WithHTTPTransportWrapper(func(base http.RoundTripper) http.RoundTripper {
//	    return &bearerTransport{token: myToken, base: base}
//	})
func WithHTTPTransportWrapper(fn func(base http.RoundTripper) http.RoundTripper) Opt {
	return func(cfg *ModelOptions) {
		cfg.transportWrapper = fn
	}
}

// FromModelOptions converts a concrete ModelOptions value into a slice of
// Opt configuration functions. Later Opts override earlier ones when applied.
func FromModelOptions(m ModelOptions) []Opt {
	var out []Opt
	if g := m.Gateway(); g != "" {
		out = append(out, WithGateway(g))
	}
	if m.structuredOutput != nil {
		out = append(out, WithStructuredOutput(m.structuredOutput))
	}
	if m.generatingTitle {
		out = append(out, WithGeneratingTitle())
	}
	if m.noThinking {
		out = append(out, WithNoThinking())
	}
	if m.maxTokens != 0 {
		out = append(out, WithMaxTokens(m.maxTokens))
	}
	if len(m.providers) > 0 {
		out = append(out, WithProviders(m.providers))
	}
	if m.modelsDevStore != nil {
		out = append(out, WithModelsDevStore(m.modelsDevStore))
	}
	if m.transportWrapper != nil {
		out = append(out, WithHTTPTransportWrapper(m.transportWrapper))
	}
	return out
}
