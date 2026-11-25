package dashboard

import (
	"context"
	"strings"

	"9router/proxy/internal/db"
	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
)

// providerIdentityKeys returns the exact spellings that identify provider
// inside combo entries ("<key>/<model>") without crossing into unrelated
// providers: the input itself, the paired node ID/prefix when provider names
// a custom node, and the built-in canonical ID/alias otherwise.
func providerIdentityKeys(repo *db.Repo, provider string) map[string]struct{} {
	keys := make(map[string]struct{}, 4)
	if provider == "" {
		return keys
	}
	keys[provider] = struct{}{}

	isCustomNode := false
	if repo != nil {
		if prefixMap, err := repo.GetProviderNodePrefixMap(); err == nil {
			if prefix := prefixMap[provider]; prefix != "" {
				keys[prefix] = struct{}{}
				isCustomNode = true
			}
			for nodeID, prefix := range prefixMap {
				if prefix == provider {
					keys[nodeID] = struct{}{}
					isCustomNode = true
				}
			}
		}
	}

	if !isCustomNode {
		canon := providers.ResolveAlias(provider)
		if canon != "" {
			keys[canon] = struct{}{}
			if alias := providers.GetProviderAlias(canon); alias != "" {
				keys[alias] = struct{}{}
			}
		}
	}
	return keys
}

// pruneDeletedModelsFromCombosBestEffort strips permanently deleted models
// from every combo's member list in one batched DB pass. Best-effort: the
// caller already persisted the delete, so a combo prune failure is logged
// rather than turning a completed delete into an error.
func (h *DashboardHandler) pruneDeletedModelsFromCombosBestEffort(ctx context.Context, provider string, modelIDs []string) {
	if h.Repo == nil || provider == "" || len(modelIDs) == 0 {
		return
	}
	provKeys := providerIdentityKeys(h.Repo, provider)
	modelSet := make(map[string]struct{}, len(modelIDs))
	for _, id := range modelIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		modelSet[id] = struct{}{}
		for pKey := range provKeys {
			if trimmed := strings.TrimPrefix(id, pKey+"/"); trimmed != id && trimmed != "" {
				modelSet[trimmed] = struct{}{}
			}
		}
	}
	if len(modelSet) == 0 {
		return
	}

	pruned, err := h.Repo.PruneComboModels(ctx, func(entry string) bool {
		p, m, ok := strings.Cut(entry, "/")
		if !ok {
			return false
		}
		_, pOK := provKeys[p]
		_, mOK := modelSet[m]
		return pOK && mOK
	})
	if err != nil {
		log.Warn("combo-sync", "prune deleted models failed", "provider", provider, "error", err)
		return
	}
	if pruned > 0 {
		log.Info("combo-sync", "pruned deleted models from combos", "provider", provider, "models", len(modelSet), "combos", pruned)
	}
}

func isKeylessProvider(provider string) bool {
	if provider == "" {
		return false
	}
	if cfg, ok := providers.KnownProviders[provider]; ok {
		return cfg.NoAuth || cfg.DefaultAPIKey != ""
	}
	if canon := providers.ResolveAlias(provider); canon != "" {
		if cfg, ok := providers.KnownProviders[canon]; ok {
			return cfg.NoAuth || cfg.DefaultAPIKey != ""
		}
	}
	return false
}

// pruneDeletedProviderFromCombosBestEffort strips every combo member belonging
// to provider once its last stored connection has been deleted. Keyless
// providers (NoAuth or DefaultAPIKey) legitimately run without connection rows
// and are never pruned here.
func (h *DashboardHandler) pruneDeletedProviderFromCombosBestEffort(ctx context.Context, provider string) {
	if h.Repo == nil || provider == "" || isKeylessProvider(provider) {
		return
	}
	provKeys := providerIdentityKeys(h.Repo, provider)
	if len(provKeys) == 0 {
		return
	}

	pruned, err := h.Repo.PruneComboModels(ctx, func(entry string) bool {
		p, _, ok := strings.Cut(entry, "/")
		if !ok {
			return false
		}
		_, pOK := provKeys[p]
		return pOK
	})
	if err != nil {
		log.Warn("combo-sync", "prune deleted provider failed", "provider", provider, "error", err)
		return
	}
	if pruned > 0 {
		log.Info("combo-sync", "pruned deleted provider from combos", "provider", provider, "combos", pruned)
	}
}
