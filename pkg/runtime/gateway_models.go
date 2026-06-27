package runtime

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/docker/docker-agent/pkg/config/latest"
	"github.com/docker/docker-agent/pkg/modelsdev"
	"github.com/docker/docker-agent/pkg/modelsgateway"
)

// gatewayModelsTTL is how long a gateway /v1/models response (or failure)
// is reused before the gateway is queried again. It keeps the model picker
// snappy on repeated opens while still picking up gateway changes
// eventually.
const gatewayModelsTTL = 5 * time.Minute

// gatewayModelsCache memoizes the result of the gateway model discovery,
// including failures, so an unsupported or slow gateway is not re-queried
// on every picker open. The mutex only guards the cached fields; the
// network fetch itself runs outside the lock, coalesced by the
// singleflight group so concurrent callers share one in-flight request
// instead of stalling on each other.
type gatewayModelsCache struct {
	mu        sync.Mutex
	sf        singleflight.Group
	ids       []string
	err       error
	fetchedAt time.Time
}

// listGatewayModels returns the model IDs served by the configured models
// gateway, using the runtime's cache when fresh.
func (r *LocalRuntime) listGatewayModels(ctx context.Context) ([]string, error) {
	now := time.Now
	if r.now != nil {
		now = r.now
	}

	c := &r.gatewayModels

	readFresh := func() (ids []string, ok bool, err error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		if !c.fetchedAt.IsZero() && now().Sub(c.fetchedAt) < gatewayModelsTTL {
			return c.ids, true, c.err
		}
		return nil, false, nil
	}

	if ids, ok, err := readFresh(); ok {
		slog.DebugContext(ctx, "Gateway model discovery cache hit", "models", len(ids), "error", err)
		return ids, err
	}

	start := time.Now()
	v, err, _ := c.sf.Do("models", func() (any, error) {
		// Double-check the cache now that we hold the in-flight slot: a
		// caller that read a stale cache right before a concurrent
		// singleflight completed would otherwise trigger a redundant
		// fetch immediately after the cache was populated.
		if ids, ok, err := readFresh(); ok {
			return ids, err
		}

		ids, err := modelsgateway.ListModels(ctx, r.modelSwitcherCfg.ModelsGateway, r.modelSwitcherCfg.EnvProvider)
		c.mu.Lock()
		c.ids, c.err, c.fetchedAt = ids, err, now()
		c.mu.Unlock()
		return ids, err
	})
	if err != nil {
		slog.DebugContext(ctx, "Gateway model discovery fetch completed", "duration", time.Since(start), "error", err)
		return nil, err
	}
	ids := v.([]string)
	slog.DebugContext(ctx, "Gateway model discovery fetch completed", "duration", time.Since(start), "models", len(ids))
	return ids, nil
}

// buildGatewayChoices builds ModelChoice entries from the models served by
// the configured gateway, deduplicated against the explicitly configured
// models. The second return value reports whether discovery succeeded;
// when false (gateway unreachable, /v1/models unsupported, or an empty
// list that gives no usable signal) callers should fall back to the
// models.dev catalog.
func (r *LocalRuntime) buildGatewayChoices(ctx context.Context) ([]ModelChoice, bool) {
	ids, err := r.listGatewayModels(ctx)
	if err != nil {
		slog.DebugContext(ctx, "Gateway model discovery failed, falling back to catalog", "error", err)
		return nil, false
	}
	if len(ids) == 0 {
		slog.DebugContext(ctx, "Gateway returned no models, falling back to catalog")
		return nil, false
	}

	existingRefs := make(map[string]bool, len(r.modelSwitcherCfg.Models)*2)
	for name, cfg := range r.modelSwitcherCfg.Models {
		existingRefs[name] = true
		if cfg.Provider != "" && cfg.Model != "" {
			existingRefs[cfg.Provider+"/"+cfg.Model] = true
		}
	}

	choices := make([]ModelChoice, 0, len(ids))
	for _, id := range ids {
		prov, model, ok := strings.Cut(id, "/")
		if !ok {
			// Bare IDs (no provider prefix) are served through the
			// gateway's OpenAI-compatible endpoint, so route them
			// through the openai provider.
			prov, model = "openai", id
		}
		if _, err := latest.ParseModelRef(prov + "/" + model); err != nil {
			continue
		}

		// Resolve catalog metadata before the embedding filter so the
		// catalog Family (e.g. "text-embedding") is consulted even when
		// the model ID itself doesn't contain "embed".
		var meta *modelsdev.Model
		if r.modelsStore != nil {
			if m, err := r.modelsStore.GetModel(ctx, modelsdev.NewID(prov, model)); err == nil {
				meta = m
			}
		}
		family := ""
		if meta != nil {
			family = meta.Family
		}
		if isEmbeddingModel(family, model) {
			continue
		}

		ref := prov + "/" + model
		if existingRefs[ref] {
			continue
		}
		existingRefs[ref] = true

		choice := ModelChoice{
			Name:      model,
			Ref:       ref,
			Provider:  prov,
			Model:     model,
			IsCatalog: true,
		}
		if meta != nil {
			if meta.Name != "" {
				choice.Name = meta.Name
			}
			applyCatalogMetadata(&choice, meta)
		}
		choices = append(choices, choice)
	}

	slog.DebugContext(ctx, "Built gateway model choices", "count", len(choices))
	return choices, true
}
