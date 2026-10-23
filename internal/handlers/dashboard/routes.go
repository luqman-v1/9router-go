package dashboard

import (
	"net/http"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/shared"
	"9router/proxy/internal/middleware"
	"9router/proxy/internal/observ"
	"9router/proxy/internal/semanticcache"
	"github.com/go-chi/chi/v5"
)

// Default probe URLs for proxy pool health checks.
const (
	DefaultPrimaryProbeURL   = "https://www.google.com/generate_204"
	DefaultSecondaryProbeURL = "https://cloudflare.com/cdn-cgi/trace"
)

// DashboardHandler handles dashboard REST API endpoints.
type DashboardHandler struct {
	Repo              *db.Repo
	PrimaryProbeURL   string
	SecondaryProbeURL string
	TokenSaver        *shared.TokenSaverConfig
	SemanticCache     *semanticcache.Cache
	// Limiter is the process-wide rate limiter, shared with the data plane so the
	// dashboard reports the same provider windows the dispatch gate enforces. It
	// is optional: a handler built outside a boot (tests, tooling) has none, and
	// the live usage fields then read zero rather than being invented.
	Limiter *middleware.RateLimiter
}

// NewDashboardHandler initializes a DashboardHandler with the provided Repo.
func NewDashboardHandler(repo *db.Repo, ts ...*shared.TokenSaverConfig) *DashboardHandler {
	h := &DashboardHandler{
		Repo:              repo,
		PrimaryProbeURL:   DefaultPrimaryProbeURL,
		SecondaryProbeURL: DefaultSecondaryProbeURL,
	}
	if len(ts) > 0 && ts[0] != nil {
		h.TokenSaver = ts[0]
	}
	return h
}

// getURLParam retrieves a route parameter from Chi URLParam or standard PathValue.
func getURLParam(r *http.Request, key string) string {
	if v := chi.URLParam(r, key); v != "" {
		return v
	}
	if key == "key" {
		if v := chi.URLParam(r, "*"); v != "" {
			return v
		}
	}
	return r.PathValue(key)
}

// RegisterRoutes registers all dashboard REST endpoints under /api on the router.
func RegisterRoutes(r chi.Router, h *DashboardHandler) {
	r.Route("/api", func(r chi.Router) {
		// Connections
		r.Get("/connections", h.HandleGetConnections)
		r.Post("/connections", h.HandleCreateConnection)
		r.Put("/connections/{id}", h.HandleUpdateConnection)
		r.Put("/providers/{id}", h.HandleUpdateConnection)
		r.Delete("/connections/{id}", h.HandleDeleteConnection)
		r.Delete("/providers/{id}", h.HandleDeleteConnection)
		r.Post("/connections/{id}/test", h.HandleTestConnection)
		r.Post("/providers/{id}/test", h.HandleTestConnection)
		r.Get("/providers/{id}/models", h.HandleGetConnectionModels)
		// Bulk removal for the rows the import path writes; the single-row form
		// stays at DELETE /models/custom/{key}.
		r.Delete("/providers/{id}/models", h.HandleDeleteProviderModels)
		r.Get("/providers/{id}/overrides", h.HandleGetProviderOverrides)
		r.Put("/providers/{id}/overrides", h.HandleSaveProviderOverrides)
		r.Post("/providers/validate", h.HandleValidateProvider)

		// Provider Nodes (Custom Endpoints)
		r.Get("/provider-nodes", h.HandleGetProviderNodes)
		r.Post("/provider-nodes", h.HandleCreateProviderNode)
		r.Put("/provider-nodes/{id}", h.HandleUpdateProviderNode)
		r.Post("/provider-nodes/validate", h.HandleValidateProviderNode)
		r.Delete("/provider-nodes/{id}", h.HandleDeleteProviderNode)
		// Per-node capacity limits (docs/provider-limits-plan.md). The GET list
		// above carries them too; these two write the policy and lift a spent
		// token quota without touching the history the analytics reads.
		r.Put("/provider-nodes/{id}/limits", h.HandleUpdateProviderNodeLimits)
		r.Post("/provider-nodes/{id}/quota/reset", h.HandleResetProviderNodeQuota)

		// Combos
		r.Get("/combos", h.HandleGetCombos)
		r.Post("/combos", h.HandleCreateCombo)
		r.Post("/combos/auto-free", h.HandleAutoFreeCombo)
		r.Post("/combos/auto-family", h.HandleAutoFamilyCombos)
		r.Get("/combos/presets", h.HandleGetComboPresets)
		r.Post("/combos/presets", h.HandleCreateComboPresets)
		r.Put("/combos/{id}", h.HandleUpdateCombo)
		// Smart select combo: dry-run a rule list, and apply every combo's rules
		// on demand. The automatic half runs on each model write.
		r.Post("/combos/reconcile", h.HandleReconcileSmartCombos)
		r.Post("/combos/{id}/smart/preview", h.HandleComboSmartPreview)
		r.Delete("/combos/{id}", h.HandleDeleteCombo)

		// Proxy Pools
		r.Get("/proxy-pools", h.HandleGetProxyPools)
		r.Post("/proxy-pools", h.HandleCreateProxyPool)
		r.Put("/proxy-pools/{id}", h.HandleUpdateProxyPool)
		r.Delete("/proxy-pools/{id}", h.HandleDeleteProxyPool)
		r.Post("/proxy-pools/{id}/test", h.HandleTestProxyPool)

		// API Keys
		r.Get("/keys", h.HandleGetApiKeys)
		r.Post("/keys", h.HandleCreateApiKey)
		r.Delete("/keys/{id}", h.HandleDeleteApiKey)
		r.Put("/keys/{id}/toggle", h.HandleToggleApiKey)
		r.Post("/keys/{id}/rotate", h.HandleRotateApiKey)
		r.Get("/keys/{id}/models", h.HandleGetApiKeyModels)
		r.Put("/keys/{id}/models", h.HandleSetApiKeyModels)
		r.Put("/keys/{id}", h.HandleUpdateApiKey)

		// F-3 Plans: reusable policy templates the keys inherit from.
		r.Get("/plans", h.HandleListPlans)
		r.Post("/plans", h.HandleCreatePlan)
		r.Get("/plans/{id}", h.HandleGetPlan)
		r.Put("/plans/{id}", h.HandleUpdatePlan)
		r.Delete("/plans/{id}", h.HandleDeletePlan)
		r.Get("/plans/{id}/keys", h.HandlePlanKeys)

		// F-5 Credential vault: status and master-key rotation.
		r.Get("/vault/status", h.HandleGetVaultStatus)
		r.Post("/vault/rotate", h.HandleRotateVault)
		r.Get("/metrics", observ.Handler().ServeHTTP)

		// Guardrails: policy CRUD and the audit log.
		r.Get("/guardrails/policies", h.HandleGetGuardrailPolicies)
		r.Post("/guardrails/policies", h.HandleCreateGuardrailPolicy)
		r.Put("/guardrails/policies/{id}", h.HandleUpdateGuardrailPolicy)
		r.Delete("/guardrails/policies/{id}", h.HandleDeleteGuardrailPolicy)
		r.Get("/guardrails/logs", h.HandleListGuardrailLogs)

		// Models
		r.Get("/models/custom", h.HandleGetCustomModels)
		r.Get("/models/caps", h.HandleGetModelCaps)
		r.Post("/models/custom", h.HandleSaveCustomModel)
		r.Delete("/models/custom/{key}", h.HandleDeleteCustomModel)
		r.Delete("/models/custom/*", h.HandleDeleteCustomModel)
		r.Get("/models/disabled", h.HandleGetDisabledModels)
		r.Put("/models/disabled/{provider}", h.HandleSaveDisabledModels)

		// Settings
		r.Get("/settings", h.HandleGetSettings)
		r.Put("/settings", h.HandleUpdateSettings)
		r.Get("/settings/database", h.HandleExportDatabase)
		r.Post("/settings/database", h.HandleImportDatabase)
		r.Get("/settings/database/stats", h.HandleGetDatabaseStats)
		r.Post("/settings/database/prune", h.HandlePruneDatabase)
		r.Post("/settings/database/prune-usage", h.HandlePruneUsageHistory)
		r.Post("/settings/database/vacuum", h.HandleVacuumDatabase)
		r.Post("/settings/proxy-test", h.HandleProxyTest)
		r.Post("/settings/currency/refresh", h.HandleRefreshCurrencyRates)

		// Token Saver Testing Benches
		r.Post("/tokensaver/rtk/test", h.HandleTestRTK)
		r.Post("/tokensaver/caveman/test", h.HandleTestCaveman)

		// Tunnel & Tailscale
		r.Get("/tunnel/status", h.HandleTunnelStatus)
		r.Post("/tunnel/enable", h.HandleTunnelEnable)
		r.Post("/tunnel/disable", h.HandleTunnelDisable)
		r.Get("/tunnel/tailscale-check", h.HandleTailscaleCheck)
		r.Post("/tunnel/tailscale-enable", h.HandleTailscaleEnable)
		r.Post("/tunnel/tailscale-disable", h.HandleTailscaleDisable)
		// Usage & Quotas
		r.Get("/usage/providers", h.HandleGetUsageProviders)
		r.Get("/usage/{connectionId}", h.HandleGetConnectionUsage)
		// Codex reset credits. These sit one segment deeper than the
		// per-connection usage route, so chi matches them without the static
		// before-parameter ordering that /usage/providers needs.
		r.Get("/usage/{connectionId}/reset-credits", h.HandleListCodexResetCredits)
		r.Post("/usage/{connectionId}/reset-credits/consume", h.HandleConsumeCodexResetCredit)

		// Cache Analytics & Management
		r.Get("/cache", h.HandleGetCache)
		r.Delete("/cache", h.HandleDeleteCache)
		r.Get("/cache/entries", h.HandleGetCacheEntries)
		r.Delete("/cache/entries", h.HandleDeleteCacheEntry)

		// Compression Analytics
		r.Get("/analytics/compression", h.HandleGetCompressionAnalytics)
	})
}
