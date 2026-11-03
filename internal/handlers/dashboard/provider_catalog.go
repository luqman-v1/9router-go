package dashboard

import (
	"net/http"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/providers"
)

// HandleGetProviderCatalog serves the provider registry the dashboard renders
// its catalog from, grouped by the section each provider belongs to.
//
// This is the single source of truth for provider display metadata. The
// dashboard used to ship a second copy of it in TypeScript, which meant a
// provider added to one and not the other either vanished from the UI or
// rendered as a bare id. Keeping it in Go also means the same metadata is
// available to any other consumer (the changelog, tooling) without a re-sync.
//
// The payload is the whole registry rather than the providers an operator has
// connected: the catalog is what the pickers and the "add provider" flow
// browse, and those need providers that have no connection yet.
func (h *DashboardHandler) HandleGetProviderCatalog(w http.ResponseWriter, r *http.Request) {
	byCategory := providers.GetCatalogByCategory()

	// Emit in the order the dashboard renders the sections, and include the
	// label and the member count per section so the client does not have to
	// derive either.
	type section struct {
		Key       string                   `json:"key"`
		Label     string                   `json:"label"`
		Count     int                      `json:"count"`
		Providers []providers.ProviderMeta `json:"providers"`
	}
	sections := make([]section, 0, len(providers.CategoryOrder))
	for _, cat := range providers.CategoryOrder {
		metas := byCategory[cat]
		sections = append(sections, section{
			Key:       string(cat),
			Label:     providers.GetCategoryLabel(cat),
			Count:     len(metas),
			Providers: metas,
		})
	}

	// The no-auth set is served alongside the sections rather than folded into
	// one, because it answers a different question: "which providers work
	// without me creating a connection?" — not "what section does this belong
	// to?" A provider appears in both, deliberately.
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"sections":   sections,
		"noAuth":     providers.NoAuthProviders(),
		"kinds":      providers.ServiceKindLabels(),
		"categories": providers.CategoryOrder,
	})
}
