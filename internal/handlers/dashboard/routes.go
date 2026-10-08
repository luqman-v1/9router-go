package dashboard

import (
	"net/http"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/shared"
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
		r.Get("/providers/{id}/overrides", h.HandleGetProviderOverrides)
		r.Put("/providers/{id}/overrides", h.HandleSaveProviderOverrides)
		r.Post("/providers/validate", h.HandleValidateProvider)

		// Provider Nodes (Custom Endpoints)
		r.Get("/provider-nodes", h.HandleGetProviderNodes)
		r.Post("/provider-nodes", h.HandleCreateProviderNode)
		r.Put("/provider-nodes/{id}", h.HandleUpdateProviderNode)
		r.Post("/provider-nodes/validate", h.HandleValidateProviderNode)
		r.Delete("/provider-nodes/{id}", h.HandleDeleteProviderNode)

		// Combos
		r.Get("/combos", h.HandleGetCombos)
		r.Post("/combos", h.HandleCreateCombo)
		r.Post("/combos/auto-free", h.HandleAutoFreeCombo)
		r.Post("/combos/auto-family", h.HandleAutoFamilyCombos)
		r.Put("/combos/{id}", h.HandleUpdateCombo)
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
		r.Put("/keys/{id}", h.HandleUpdateApiKey)

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
		r.Get("/models/disabled", h.HandleGetDisabledModels)
		r.Put("/models/disabled/{provider}", h.HandleSaveDisabledModels)

		// Settings
		r.Get("/settings", h.HandleGetSettings)
		r.Put("/settings", h.HandleUpdateSettings)
		r.Get("/settings/database", h.HandleExportDatabase)
		r.Post("/settings/database", h.HandleImportDatabase)
		r.Post("/settings/proxy-test", h.HandleProxyTest)

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
