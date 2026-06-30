package root

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/docker/docker-agent/pkg/cli"
	"github.com/docker/docker-agent/pkg/config"
	"github.com/docker/docker-agent/pkg/config/latest"
	"github.com/docker/docker-agent/pkg/environment"
	"github.com/docker/docker-agent/pkg/httpclient"
	"github.com/docker/docker-agent/pkg/model/provider"
	"github.com/docker/docker-agent/pkg/telemetry"
	"github.com/docker/docker-agent/pkg/userconfig"
)

type modelsListFlags struct {
	providerFilter string
	format         string
	all            bool
	runConfig      config.RuntimeConfig
}

// listTimeout bounds the live /v1/models request for the `models list` command
// so a slow or unreachable provider endpoint cannot stall an interactive
// listing. Matches the 5-second budget used by DMR and models-gateway
// discovery (pkg/model/provider/dmr/list.go, pkg/modelsgateway/discovery.go).
const listTimeout = 5 * time.Second

// liveFetchProviders is the allow-list of catalog aliases that may be queried
// directly at their own /v1/models endpoint when models.dev does not yet
// include them. Scoping the fetch (rather than firing for every alias absent
// from the snapshot) prevents surprising side effects like `docker agent models
// --provider ollama` issuing a real GET against localhost.
var liveFetchProviders = map[string]bool{
	"opencode-zen": true,
	"opencode-go":  true,
}

// modelRow represents a single model entry for display or serialization.
type modelRow struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Default  bool   `json:"default,omitempty"`
}

func newModelsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "models",
		Short: "List available models",
		Long: `List models available for use with --model flag.

Shows models that can be passed to 'docker agent run --model' or
'docker agent new --model'. By default shows models from providers
you have credentials for. Use --all to include all providers.`,
		GroupID: "core",
	}

	listCmd := newModelsListCmd()
	cmd.AddCommand(listCmd)

	// Default to "list" when no subcommand given.
	cmd.RunE = listCmd.RunE

	// Copy the flags from the list command so they work on the bare
	// "docker agent models --provider openai" form as well.
	cmd.Flags().AddFlagSet(listCmd.Flags())

	return cmd
}

func newModelsListCmd() *cobra.Command {
	var flags modelsListFlags

	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List available models",
		Example: `  docker agent models
  docker agent models list --provider openai
  docker agent models ls --all
  docker agent models --format json`,
		Args: cobra.NoArgs,
		RunE: flags.runModelsListCommand,
	}

	cmd.Flags().StringVarP(&flags.providerFilter, "provider", "p", "", "Filter by provider name")
	cmd.Flags().StringVar(&flags.format, "format", "table", "Output format: table, json")
	cmd.Flags().BoolVarP(&flags.all, "all", "a", false, "Include models from all providers, not just those with credentials")
	addGatewayFlags(cmd, &flags.runConfig, userconfig.Load)

	return cmd
}

func (f *modelsListFlags) runModelsListCommand(cmd *cobra.Command, args []string) (commandErr error) {
	ctx := cmd.Context()
	telemetry.TrackCommand(ctx, "models", append([]string{"list"}, args...))
	defer func() {
		telemetry.TrackCommandError(ctx, "models", append([]string{"list"}, args...), commandErr)
	}()

	out := cli.NewPrinter(cmd.OutOrStdout())
	env := f.runConfig.EnvProvider()

	// Normalize the provider filter to lowercase so case-sensitive map lookups
	// in AvailableProviders, db.Providers and IsCatalogProvider all match the
	// same way strings.EqualFold does in the outer row filter below.
	if f.providerFilter != "" {
		f.providerFilter = strings.ToLower(f.providerFilter)
	}

	// Determine which providers the user has credentials for.
	availableProviders := make(map[string]bool)
	for _, p := range config.AvailableProviders(ctx, f.runConfig.ModelsGateway, env) {
		availableProviders[p] = true
	}

	// Determine which model auto-selection would pick. DMR discovery is left
	// out here (nil lister) so listing models stays a pure, side-effect-free
	// operation; the default marker therefore reflects the static per-provider
	// default rather than a locally-pulled DMR model.
	autoModel := config.AutoModelConfig(ctx, f.runConfig.ModelsGateway, env, f.runConfig.DefaultModel, nil)

	rows := f.collectModels(ctx, env, availableProviders, autoModel)

	// Apply provider filter
	if f.providerFilter != "" {
		rows = slices.DeleteFunc(rows, func(r modelRow) bool {
			return !strings.EqualFold(r.Provider, f.providerFilter)
		})
	}

	// Sort: default first, then by provider, then by model
	slices.SortFunc(rows, func(a, b modelRow) int {
		if a.Default != b.Default {
			if a.Default {
				return -1
			}
			return 1
		}
		if c := strings.Compare(a.Provider, b.Provider); c != 0 {
			return c
		}
		return strings.Compare(a.Model, b.Model)
	})

	if len(rows) == 0 {
		out.Println("No models available.")
		out.Println("\nConfigure a provider API key or install Docker Model Runner.")
		return nil
	}

	switch f.format {
	case "json":
		return f.renderJSON(cmd, rows)
	default:
		f.renderTable(cmd, rows)
	}

	return nil
}

// collectModels returns all models from the catalog, filtered by credential
// availability unless --all is set. Default models for each available provider
// are always included even if the catalog fetch fails.
func (f *modelsListFlags) collectModels(ctx context.Context, env environment.Provider, availableProviders map[string]bool, autoModel latest.ModelConfig) []modelRow {
	seen := make(map[string]bool)
	var rows []modelRow

	// Always include the per-provider defaults so we have something even
	// if the catalog is unreachable.
	for prov, model := range config.DefaultModels {
		if !f.all && !availableProviders[prov] {
			continue
		}
		ref := prov + "/" + model
		seen[ref] = true
		rows = append(rows, modelRow{
			Provider: prov,
			Model:    model,
			Default:  prov == autoModel.Provider && model == autoModel.Model,
		})
	}

	// Fetch catalog and add all text-capable models.
	store, err := f.runConfig.ModelsDevStore()
	if err != nil {
		return rows
	}
	db, err := store.GetDatabase(ctx)
	if err != nil {
		return rows
	}

	for providerID, prov := range db.Providers {
		if !provider.IsCatalogProvider(providerID) {
			continue
		}
		if !f.all && !availableProviders[providerID] {
			continue
		}
		for modelID, model := range prov.Models {
			if !slices.Contains(model.Modalities.Output, "text") {
				continue
			}
			if isEmbeddingModel(model.Family, model.Name) {
				continue
			}

			ref := providerID + "/" + modelID
			if seen[ref] {
				continue
			}
			seen[ref] = true

			rows = append(rows, modelRow{
				Provider: providerID,
				Model:    modelID,
			})
		}
	}

	// When the user explicitly filters by provider (--provider) and that
	// provider is one of the catalog aliases that publishes a live /v1/models
	// endpoint but is not yet in the embedded models.dev snapshot, fetch the
	// catalog directly. Scoping the query to liveFetchProviders keeps the
	// listing side-effect-free for every other alias (e.g. ollama), and the
	// `availableProviders` guard (or `--all`) prevents a network call when the
	// user has not configured credentials for the alias.
	if f.providerFilter != "" && (f.all || availableProviders[f.providerFilter]) && liveFetchProviders[f.providerFilter] {
		if _, exists := db.Providers[f.providerFilter]; !exists {
			for _, m := range fetchProviderModels(ctx, f.providerFilter, env) {
				if isEmbeddingModel(m, m) {
					continue
				}
				ref := f.providerFilter + "/" + m
				if seen[ref] {
					continue
				}
				seen[ref] = true
				rows = append(rows, modelRow{Provider: f.providerFilter, Model: m})
			}
		}
	}

	return rows
}

// openAIModelsResponse is the standard OpenAI-compatible models list format.
type openAIModelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// fetchProviderModels fetches the model list from a provider's own /v1/models
// endpoint. Only works for alias providers with a predefined BaseURL.
//
// The request:
//   - Uses httpclient.NewHTTPClient so it carries the Cagent User-Agent and
//     OpenTelemetry tracing like every other outbound call.
//   - Sends Bearer auth using the alias's declared TokenEnvVar, so an
//     authenticated user does not silently receive an empty list from a
//     provider that requires the API key on /v1/models.
//   - Bounded by listTimeout (5s) so a slow endpoint cannot stall `models list`.
func fetchProviderModels(ctx context.Context, providerName string, env environment.Provider) []string {
	alias, ok := provider.LookupAlias(providerName)
	if !ok || alias.BaseURL == "" {
		return nil
	}

	modelsURL := strings.TrimRight(alias.BaseURL, "/") + "/models"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, http.NoBody)
	if err != nil {
		slog.WarnContext(ctx, "failed to create request for provider models", "url", modelsURL, "error", err)
		return nil
	}

	// Send the alias's declared API key so providers that require it on
	// /v1/models authenticate the request instead of returning 401/403.
	if alias.TokenEnvVar != "" {
		if token, _ := env.Get(ctx, alias.TokenEnvVar); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}

	ctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()

	return dispatchModelsRequest(ctx, req, httpclient.NewHTTPClient(ctx))
}

// fetchModelsFromURL fetches and parses an OpenAI-compatible models list from
// the given URL using the supplied client. It is a thin wrapper around
// dispatchModelsRequest for tests that inject an httptest server's client.
func fetchModelsFromURL(ctx context.Context, url string, client *http.Client) []string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		slog.WarnContext(ctx, "failed to create request for provider models", "url", url, "error", err)
		return nil
	}
	return dispatchModelsRequest(ctx, req, client)
}

// dispatchModelsRequest sends req via client and parses the OpenAI-style model
// list returned. Empty IDs are skipped; duplicates are preserved (the caller
// — collectModels — already deduplicates by ref). The function logs and
// returns nil on any failure so the listing command degrades gracefully.
func dispatchModelsRequest(ctx context.Context, req *http.Request, client *http.Client) []string {
	url := req.URL.String()

	resp, err := client.Do(req)
	if err != nil {
		slog.WarnContext(ctx, "failed to fetch provider models", "url", url, "error", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.WarnContext(ctx, "provider models endpoint returned non-200", "url", url, "status", resp.StatusCode)
		return nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.WarnContext(ctx, "failed to read provider models response", "url", url, "error", err)
		return nil
	}

	var result openAIModelsResponse
	if err := json.Unmarshal(body, &result); err != nil {
		slog.WarnContext(ctx, "failed to parse provider models response", "url", url, "error", err)
		return nil
	}

	models := make([]string, 0, len(result.Data))
	for _, m := range result.Data {
		if m.ID == "" {
			continue
		}
		models = append(models, m.ID)
	}
	return models
}

func isEmbeddingModel(family, name string) bool {
	fl := strings.ToLower(family)
	nl := strings.ToLower(name)
	return strings.Contains(fl, "embed") || strings.Contains(nl, "embed")
}

func (f *modelsListFlags) renderTable(cmd *cobra.Command, rows []modelRow) {
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 3, ' ', 0)
	fmt.Fprintln(w, "PROVIDER\tMODEL\tDEFAULT")
	for _, r := range rows {
		def := ""
		if r.Default {
			def = "*"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", r.Provider, r.Model, def)
	}
	w.Flush()
}

func (f *modelsListFlags) renderJSON(cmd *cobra.Command, rows []modelRow) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(rows)
}
