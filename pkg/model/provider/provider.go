// Package provider builds and dispatches to LLM provider clients.
//
// The package is organised across several files:
//
//   - provider.go (this file): the public Provider interfaces and the entry
//     points [New] and [NewWithModels] that callers use to construct a
//     provider from a model config.
//   - aliases.go: the built-in provider alias table (OpenAI-compatible
//     gateways such as ollama, mistral, xai, ...) and the helpers that expose
//     it to other packages without leaking the underlying map.
//   - defaults.go: pure config-merging logic that fills in defaults from
//     custom providers, built-in aliases, and model-specific rules
//     (thinking budget, interleaved thinking, ...).
//   - factory.go: shared dispatch from a resolved provider type to the
//     concrete client constructor, plus the always-available dmr provider
//     and the rule-based router.
//   - factory_<name>.go: one file per optional provider (openai, anthropic,
//     google, amazon-bedrock); each registers itself with the dispatch table
//     and is gated by a build tag (see "Build tags" below).
//
// # Build tags
//
// The openai, anthropic, google and amazon-bedrock providers are optional.
// Each lives behind a negative build tag so a project embedding docker-agent
// can compile a provider out — together with its transitive SDK dependencies —
// to shrink the binary and dependency graph. All providers are included by
// default; pass the relevant tag(s) to opt out.
//
// Build tags are global to a build, not scoped per module: a tag set by the
// top-level project applies to every dependency too. The tags are therefore
// prefixed with "docker_agent_" so an embedding project can use its own build
// tags (even a plain "no_openai") without accidentally toggling these
// providers.
//
// The available tags are:
//
//   - docker_agent_no_openai: drop the OpenAI provider (github.com/openai/openai-go).
//   - docker_agent_no_anthropic: drop the Anthropic provider
//     (github.com/anthropics/anthropic-sdk-go). The google provider's Vertex
//     Model Garden support also imports the anthropic package, so the
//     dependency is only fully removed when combined with docker_agent_no_google.
//   - docker_agent_no_google: drop the Google provider (google.golang.org/genai, the
//     Vertex AI / cloud auth stack, and — via Vertex Model Garden — the
//     anthropic and openai SDKs). Vertex AI is unsupported either way under
//     js/wasm.
//   - docker_agent_no_bedrock: drop the Amazon Bedrock provider
//     (the github.com/aws/aws-sdk-go-v2 stack), the largest provider-specific
//     dependency tree.
//
// For example, to build without Bedrock and OpenAI:
//
//	go build -tags 'docker_agent_no_bedrock docker_agent_no_openai' ...
//
// Requesting a model whose provider was compiled out fails at construction
// time with a clear "not compiled into this build" error rather than at
// compile time. The dmr provider and the rule-based router are always
// compiled in (except under js/wasm, which has its own slim factory).
package provider

import (
	"context"
	"log/slog"

	"github.com/docker/docker-agent/pkg/chat"
	"github.com/docker/docker-agent/pkg/config/latest"
	"github.com/docker/docker-agent/pkg/environment"
	"github.com/docker/docker-agent/pkg/model/provider/base"
	"github.com/docker/docker-agent/pkg/model/provider/options"
	"github.com/docker/docker-agent/pkg/modelsdev"
	"github.com/docker/docker-agent/pkg/rag/types"
	"github.com/docker/docker-agent/pkg/tools"
)

// Provider defines the interface for model providers.
type Provider interface {
	// ID returns the provider-qualified model identity. Returning a
	// [modelsdev.ID] (rather than a bare string) prevents callers from
	// silently forgetting to namespace the model when it crosses an API
	// boundary; use [modelsdev.ID.String] when a textual representation
	// is required.
	ID() modelsdev.ID
	// CreateChatCompletionStream creates a streaming chat completion request.
	// It returns a stream that can be iterated over to get completion chunks.
	CreateChatCompletionStream(
		ctx context.Context,
		messages []chat.Message,
		tools []tools.Tool,
	) (chat.MessageStream, error)
	// BaseConfig returns the base configuration of this provider.
	BaseConfig() base.Config
}

// EmbeddingProvider defines the interface for providers that support embeddings.
type EmbeddingProvider interface {
	Provider
	// CreateEmbedding generates an embedding vector for the given text with usage tracking.
	CreateEmbedding(ctx context.Context, text string) (*base.EmbeddingResult, error)
}

// BatchEmbeddingProvider defines the interface for providers that support batch embeddings.
type BatchEmbeddingProvider interface {
	EmbeddingProvider
	// CreateBatchEmbedding generates embedding vectors for multiple texts with usage tracking.
	// Returns embeddings in the same order as input texts.
	CreateBatchEmbedding(ctx context.Context, texts []string) (*base.BatchEmbeddingResult, error)
}

// RerankingProvider defines the interface for providers that support reranking.
// Reranking models score query-document pairs to assess relevance.
type RerankingProvider interface {
	Provider
	// Rerank scores documents by relevance to the query.
	// Returns relevance scores in the same order as input documents.
	// Scores are typically in [0, 1] range where higher means more relevant.
	// criteria: Optional domain-specific guidance for relevance scoring (appended to base prompt)
	// documents: Array of types.Document with content and metadata
	Rerank(ctx context.Context, query string, documents []types.Document, criteria string) ([]float64, error)
}

// New creates a new provider from a model config.
// This is a convenience wrapper for NewWithModels with no models map.
func New(ctx context.Context, cfg *latest.ModelConfig, env environment.Provider, opts ...options.Opt) (Provider, error) {
	return NewWithModels(ctx, cfg, nil, env, opts...)
}

// NewWithModels creates a new provider from a model config with access to the full models map.
// The models map is used to resolve model references in routing rules.
func NewWithModels(ctx context.Context, cfg *latest.ModelConfig, models map[string]latest.ModelConfig, env environment.Provider, opts ...options.Opt) (Provider, error) {
	slog.DebugContext(ctx, "Creating model provider", "type", cfg.Provider, "model", cfg.Model)

	// Check if this model has routing rules - if so, create a rule-based router
	if len(cfg.Routing) > 0 {
		return createRuleBasedRouter(ctx, cfg, models, env, opts...)
	}

	return createDirectProvider(ctx, cfg, env, opts...)
}
