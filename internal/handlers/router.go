package handlers

import (
	"9router/proxy/internal/constants"
	"9router/proxy/internal/db"
	"9router/proxy/internal/guardrails"
	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/handlers/dashboard"
	"9router/proxy/internal/handlers/media"
	"9router/proxy/internal/handlers/oauth"
	"9router/proxy/internal/handlers/shared"
	"9router/proxy/internal/handlers/sso"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/middleware"
	"9router/proxy/internal/observ"
	"9router/proxy/web"
	json "encoding/json/v2"
	"github.com/go-chi/chi/v5"
	"net/http"
	"net/http/pprof"
	"os"
	"strings"
	"time"
)

// Re-export TokenSaverConfig for root compatibility
type TokenSaverConfig = shared.TokenSaverConfig

// NewTokenSaverConfig re-exports shared.NewTokenSaverConfig.
func NewTokenSaverConfig(rtk, caveman, ponytail bool) *TokenSaverConfig {
	return shared.NewTokenSaverConfig(rtk, caveman, ponytail)
}

// SetupRoutes mounts all domain handlers on the provided router. It returns the
// engine's chat handler, which owns the semantic cache the engine actually
// reads and writes. The caller must pass that same handler to
// SetupDashboardRoutes: a second NewChatHandler builds a second
// PersistentStore over the same SQLite file with its own in-memory LRU, so the
// dashboard would report zero entries while the engine served hits, and
// clearing entries from the dashboard would leave the engine still serving them.
func SetupRoutes(r interface {
	Get(pattern string, handlerFn http.HandlerFunc)
	Post(pattern string, handlerFn http.HandlerFunc)
	Put(pattern string, handlerFn http.HandlerFunc)
	Patch(pattern string, handlerFn http.HandlerFunc)
	Delete(pattern string, handlerFn http.HandlerFunc)
	HandleFunc(pattern string, handlerFn http.HandlerFunc)
}, repo *db.Repo, ts *TokenSaverConfig) *chat.ChatHandler {
	chatH := chat.NewChatHandler(repo, ts)
	mediaH := media.NewMediaHandler(repo, ts, chatH)
	oauthH := oauth.NewOAuthHandler(repo)

	dashH := dashboard.NewDashboardHandler(repo, ts)
	if chatH != nil {
		dashH.SemanticCache = chatH.SemanticCache
	}
	// Chat & Models Domain (no version here: the four version GETs are
	// public in SetupServerRouter, upstream PUBLIC_API_PATHS parity).
	r.Get("/changelog", chatH.HandleChangelog)
	r.Get("/api/changelog", chatH.HandleChangelog)
	r.Get("/models", chatH.HandleModels)
	r.Get("/models/info", chatH.HandleModelsInfo)
	r.Get("/models/{kind}", chatH.HandleModelsByKind)
	r.Get("/models/*", chatH.HandleModelLookup)
	r.Get("/v1/models", chatH.HandleModels)
	r.Get("/v1/models/*", chatH.HandleModelLookup)
	r.Get("/api/v1/models", chatH.HandleModels)
	r.Get("/api/v1/models/*", chatH.HandleModelLookup)
	r.Get("/api/models", chatH.HandleModels)
	r.Get("/api/models/*", chatH.HandleModelLookup)
	r.Get("/api/models/catalog-sync", chatH.HandleCatalogSyncStatus)
	r.Post("/api/models/catalog-sync", chatH.HandleCatalogSyncTrigger)
	r.Get("/api/models", chatH.HandleModels)
	r.Post("/chat/completions", chatH.HandleChatCompletions)
	r.Post("/messages", chatH.HandleMessages)
	r.Post("/messages/count_tokens", chatH.HandleCountTokens)
	r.Post("/api/chat", chatH.HandleOllamaChat)

	// Media, Audio, Video & Web Tools Domain
	r.Post("/embeddings", mediaH.HandleEmbeddings)
	// /v1/responses needs model resolution, combos, account fallback and usage
	// logging, none of which the media passthrough has, so it lives with the
	// chat handlers — the same place /v1/messages does.
	r.Post("/responses", chatH.HandleResponses)
	r.Post("/responses/compact", chatH.HandleResponsesCompact)
	r.Post("/images/generations", mediaH.HandleImages)
	r.Post("/audio/speech", mediaH.HandleAudioSpeech)
	r.Get("/audio/voices", mediaH.HandleAudioVoices)
	r.Get("/api/media-providers/tts/voices", mediaH.HandleAudioVoices)
	r.Get("/api/media-providers/tts/inworld/voices", mediaH.HandleAudioVoices)
	r.Get("/api/media-providers/tts/minimax/voices", mediaH.HandleAudioVoices)
	r.Get("/api/media-providers/tts/deepgram/voices", mediaH.HandleAudioVoices)
	r.Get("/api/media-providers/tts/elevenlabs/voices", mediaH.HandleAudioVoices)
	r.Post("/audio/transcriptions", mediaH.HandleAudioTranscriptions)
	r.Post("/videos/generations", mediaH.HandleVideoGenerations)
	r.Post("/videos/edits", mediaH.HandleVideoEdits)
	r.Post("/videos/extensions", mediaH.HandleVideoExtensions)
	r.Get("/videos/{id}", mediaH.HandleVideoGet)
	r.Post("/search", mediaH.HandleSearch)
	r.Post("/scrape", mediaH.HandleScrape)
	r.Post("/systemone", mediaH.HandleSystemone)

	// The relay-deploy endpoints live in the dashboard group below, mounted
	// under /api/proxy-pools/*-deploy behind RequireDashboardAuth (upstream
	// parity). They were registered here under RequireApiKey, so the dashboard
	// SPA — which authenticates with the session cookie and sends no engine
	// key — got 401 "Authentication required" from every Deploy button.

	// CLI Tools Status Domain (dashboard batch status for installed CLI tools)
	// The /api/cli-tools/all-statuses alias is registered in SetupServerRouter
	// under RequireDashboardAuth instead — it is a dashboard read. Registering it
	// here too would win the match under RequireApiKey and keep it unreachable
	// from the session cookie the SPA actually sends.
	r.Get("/cli-tools/all-statuses", media.NewCLIToolsHandler().HandleAllStatuses)

	// Headroom Management Domain (token-compression proxy lifecycle + dashboard proxy)
	headroomH := media.NewHeadroomHandler(repo)
	r.Post("/headroom/start", headroomH.HandleHeadroomStart)
	r.Post("/headroom/stop", headroomH.HandleHeadroomStop)
	r.Post("/headroom/restart", headroomH.HandleHeadroomRestart)
	r.Get("/headroom/status", headroomH.HandleHeadroomStatus)
	r.Get("/headroom/extras", headroomH.HandleHeadroomExtras)
	r.Post("/headroom/extras", headroomH.HandleHeadroomExtras)
	r.Delete("/headroom/extras", headroomH.HandleHeadroomExtras)
	r.HandleFunc("/headroom/proxy", headroomH.HandleHeadroomProxy)
	r.HandleFunc("/headroom/proxy/*", headroomH.HandleHeadroomProxy)

	r.Post("/api/headroom/start", headroomH.HandleHeadroomStart)
	r.Post("/api/headroom/stop", headroomH.HandleHeadroomStop)
	r.Post("/api/headroom/restart", headroomH.HandleHeadroomRestart)
	r.Get("/api/headroom/status", headroomH.HandleHeadroomStatus)
	r.Get("/api/headroom/extras", headroomH.HandleHeadroomExtras)
	r.Post("/api/headroom/extras", headroomH.HandleHeadroomExtras)
	r.Delete("/api/headroom/extras", headroomH.HandleHeadroomExtras)
	r.HandleFunc("/api/headroom/proxy", headroomH.HandleHeadroomProxy)
	r.HandleFunc("/api/headroom/proxy/*", headroomH.HandleHeadroomProxy)
	// OAuth & Import Tokens Domain
	mountOAuthRoutes(r, oauthH)

	// Usage Real-time SSE Stream & Stats Domain (dashboard topology animation + recent requests)
	r.Get("/usage/stream", HandleUsageStream(repo))
	r.Get("/api/usage/stream", HandleUsageStream(repo))
	r.Get("/usage/stats", HandleUsageStats(repo))
	r.Get("/api/usage/stats", HandleUsageStats(repo))
	r.Get("/api/usage/request-details", HandleRequestDetails(repo))
	r.Get("/api/usage/request-details/{id}", HandleRequestDetail(repo))
	r.Get("/api/usage/providers", dashH.HandleGetUsageProviders)
	r.Get("/api/usage/{connectionId}", dashH.HandleGetConnectionUsage)
	r.Get("/api/usage/{connectionId}/reset-credits", dashH.HandleListCodexResetCredits)
	r.Post("/api/usage/{connectionId}/reset-credits/consume", dashH.HandleConsumeCodexResetCredit)

	// Debug Tracing Domain (p50/p95 latency per provider+model)
	r.Get("/debug/traces", HandleDebugTraces)
	return chatH
}

// SetupDashboardRoutes mounts the dashboard REST API. It is wrapped in
// RequireDashboardAuth by the server router, which lets a cookie-authenticated
// browser session, a valid API key or the local CLI token through when login is
// enabled (upstream dashboardGuard).
func SetupDashboardRoutes(r chi.Router, repo *db.Repo, chatH *chat.ChatHandler) {
	var ts *TokenSaverConfig
	if chatH != nil {
		ts = chatH.TokenSaver
	}
	dashH := dashboard.NewDashboardHandler(repo, ts)
	if chatH != nil {
		dashH.SemanticCache = chatH.SemanticCache
	}
	ssoH := sso.NewHandler(repo)

	r.Get("/api/connections", dashH.HandleGetConnections)
	r.Get("/api/providers", dashH.HandleGetProvidersClient)
	r.Get("/api/providers/client", dashH.HandleGetProvidersClient)
	r.Post("/api/connections", dashH.HandleCreateConnection)
	r.Post("/api/providers/validate", dashH.HandleValidateProvider)
	r.Put("/api/connections/{id}", dashH.HandleUpdateConnection)
	r.Put("/api/providers/{id}", dashH.HandleUpdateConnection)
	r.Delete("/api/connections/{id}", dashH.HandleDeleteConnection)
	r.Delete("/api/providers/{id}", dashH.HandleDeleteConnection)
	r.Post("/api/connections/{id}/test", dashH.HandleTestConnection)
	r.Post("/api/connections/{id}/reorder", dashH.HandleReorderConnection)
	r.Post("/api/providers/{id}/reorder", dashH.HandleReorderConnection)
	r.Post("/api/providers/{id}/test", dashH.HandleTestConnection)
	r.Get("/api/providers/suggested-models", HandleSuggestedModels)
	// Usage & Quota Endpoints (dashboard quota tracker, usage stats, and topology stream)
	r.Get("/api/usage/stream", HandleUsageStream(repo))
	r.Get("/usage/stream", HandleUsageStream(repo))
	r.Get("/api/usage/stats", HandleUsageStats(repo))
	r.Get("/usage/stats", HandleUsageStats(repo))
	r.Get("/api/usage/request-details", HandleRequestDetails(repo))
	r.Get("/api/usage/request-details/{id}", HandleRequestDetail(repo))
	r.Get("/api/usage/providers", dashH.HandleGetUsageProviders)
	r.Get("/api/usage/{connectionId}", dashH.HandleGetConnectionUsage)

	r.Get("/api/usage/{connectionId}/reset-credits", dashH.HandleListCodexResetCredits)
	r.Post("/api/usage/{connectionId}/reset-credits/consume", dashH.HandleConsumeCodexResetCredit)

	// Cache Analytics & Management
	r.Get("/api/cache", dashH.HandleGetCache)
	r.Delete("/api/cache", dashH.HandleDeleteCache)
	r.Get("/api/cache/entries", dashH.HandleGetCacheEntries)
	r.Delete("/api/cache/entries", dashH.HandleDeleteCacheEntry)

	// Compression Analytics
	r.Get("/api/analytics/compression", dashH.HandleGetCompressionAnalytics)

	r.Get("/api/provider-nodes", dashH.HandleGetProviderNodes)
	r.Post("/api/provider-nodes", dashH.HandleCreateProviderNode)
	r.Put("/api/provider-nodes/{id}", dashH.HandleUpdateProviderNode)
	r.Delete("/api/provider-nodes/{id}", dashH.HandleDeleteProviderNode)
	r.Post("/api/provider-nodes/validate", dashH.HandleValidateProviderNode)
	r.Get("/api/providers/{id}/models", dashH.HandleGetConnectionModels)
	r.Get("/api/providers/{id}/overrides", dashH.HandleGetProviderOverrides)
	r.Put("/api/providers/{id}/overrides", dashH.HandleSaveProviderOverrides)

	r.Get("/api/combos", dashH.HandleGetCombos)
	r.Post("/api/combos", dashH.HandleCreateCombo)
	r.Post("/api/combos/auto-free", dashH.HandleAutoFreeCombo)
	r.Post("/api/combos/auto-family", dashH.HandleAutoFamilyCombos)
	r.Put("/api/combos/{id}", dashH.HandleUpdateCombo)
	r.Delete("/api/combos/{id}", dashH.HandleDeleteCombo)

	r.Get("/api/proxy-pools", dashH.HandleGetProxyPools)
	r.Post("/api/proxy-pools", dashH.HandleCreateProxyPool)
	r.Put("/api/proxy-pools/{id}", dashH.HandleUpdateProxyPool)
	r.Delete("/api/proxy-pools/{id}", dashH.HandleDeleteProxyPool)
	r.Post("/api/proxy-pools/{id}/test", dashH.HandleTestProxyPool)

	// Relay deploy endpoints. Dashboard reads/writes, not engine traffic: the
	// SPA reaches them with its session cookie, exactly like the pool CRUD
	// above. chi prefers the static segment over {id}, so these three cannot
	// be shadowed by the parameterised routes registered first.
	relayH := media.NewMediaHandler(repo, nil, nil)
	r.Post("/api/proxy-pools/vercel-deploy", relayH.HandleVercelDeploy)
	r.Post("/api/proxy-pools/deno-deploy", relayH.HandleDenoDeploy)
	r.Post("/api/proxy-pools/cloudflare-deploy", relayH.HandleCloudflareDeploy)

	r.Get("/api/keys", dashH.HandleGetApiKeys)
	r.Post("/api/keys", dashH.HandleCreateApiKey)
	r.Delete("/api/keys/{id}", dashH.HandleDeleteApiKey)
	r.Put("/api/keys/{id}/toggle", dashH.HandleToggleApiKey)
	r.Post("/api/keys/{id}/rotate", dashH.HandleRotateApiKey)
	r.Get("/api/keys/{id}/models", dashH.HandleGetApiKeyModels)
	r.Put("/api/keys/{id}/models", dashH.HandleSetApiKeyModels)
	r.Put("/api/keys/{id}", dashH.HandleUpdateApiKey)
	r.Get("/api/metrics", observ.Handler().ServeHTTP)
	r.Get("/api/vault/status", dashH.HandleGetVaultStatus)
	r.Get("/api/guardrails/policies", dashH.HandleGetGuardrailPolicies)
	r.Post("/api/guardrails/policies", dashH.HandleCreateGuardrailPolicy)
	r.Put("/api/guardrails/policies/{id}", dashH.HandleUpdateGuardrailPolicy)
	r.Delete("/api/guardrails/policies/{id}", dashH.HandleDeleteGuardrailPolicy)
	r.Get("/api/guardrails/logs", dashH.HandleListGuardrailLogs)
	r.Post("/api/vault/rotate", dashH.HandleRotateVault)
	r.Get("/api/models/custom", dashH.HandleGetCustomModels)
	r.Get("/api/models/caps", dashH.HandleGetModelCaps)
	r.Post("/api/models/custom", dashH.HandleSaveCustomModel)
	r.Delete("/api/models/custom/{key}", dashH.HandleDeleteCustomModel)
	r.Get("/api/models/disabled", dashH.HandleGetDisabledModels)
	// mediaH needs the chat handler every chat-lane probe is forwarded through,
	// so it is built with chatH — not the nil-chat relay handler below, whose
	// probes would panic instead of reaching the provider.
	mediaH := media.NewMediaHandler(repo, nil, chatH)
	r.Post("/api/models/test", mediaH.HandleTestModel)
	r.Get("/api/media-providers/tts/voices", mediaH.HandleAudioVoices)
	r.Get("/api/media-providers/tts/inworld/voices", mediaH.HandleAudioVoices)
	r.Get("/api/media-providers/tts/minimax/voices", mediaH.HandleAudioVoices)
	r.Get("/api/media-providers/tts/deepgram/voices", mediaH.HandleAudioVoices)
	r.Get("/api/media-providers/tts/elevenlabs/voices", mediaH.HandleAudioVoices)
	r.Put("/api/models/disabled/{provider}", dashH.HandleSaveDisabledModels)
	r.Get("/api/models/alias", dashH.HandleGetModelAliases)
	r.Get("/api/models/deprecations", dashH.HandleGetModelDeprecations)
	r.Post("/api/models/sync", dashH.HandleSyncProviderModels)
	r.Put("/api/models/alias", dashH.HandleSetModelAlias)
	r.Delete("/api/models/alias", dashH.HandleDeleteModelAlias)

	r.Get("/api/settings", dashH.HandleGetSettings)
	r.Put("/api/settings", dashH.HandleUpdateSettings)
	r.Patch("/api/settings", dashH.HandleUpdateSettings)
	r.Put("/settings", dashH.HandleUpdateSettings)
	r.Patch("/settings", dashH.HandleUpdateSettings)

	// Settings backup/restore + outbound proxy diagnostics (profile page)
	r.Get("/api/settings/database", dashH.HandleExportDatabase)
	r.Post("/api/settings/database", dashH.HandleImportDatabase)
	r.Post("/api/settings/proxy-test", dashH.HandleProxyTest)

	// Token saver testing benches & filter catalog
	r.Get("/api/tokensaver/rtk/filters", dashH.HandleGetRTKFilters)
	r.Post("/api/tokensaver/rtk/test", dashH.HandleTestRTK)
	r.Post("/api/tokensaver/caveman/test", dashH.HandleTestCaveman)

	// Headroom token-compression proxy management (dashboard parity)
	headroomH := media.NewHeadroomHandler(repo)
	r.Get("/api/headroom/status", headroomH.HandleHeadroomStatus)
	r.Post("/api/headroom/start", headroomH.HandleHeadroomStart)
	r.Post("/api/headroom/stop", headroomH.HandleHeadroomStop)
	r.Post("/api/headroom/restart", headroomH.HandleHeadroomRestart)
	r.Get("/api/headroom/extras", headroomH.HandleHeadroomExtras)
	r.Post("/api/headroom/extras", headroomH.HandleHeadroomExtras)
	r.Delete("/api/headroom/extras", headroomH.HandleHeadroomExtras)
	r.HandleFunc("/api/headroom/proxy", headroomH.HandleHeadroomProxy)
	r.HandleFunc("/api/headroom/proxy/*", headroomH.HandleHeadroomProxy)

	// Tunnel & Tailscale
	r.Get("/api/tunnel/status", dashH.HandleTunnelStatus)
	r.Post("/api/tunnel/enable", dashH.HandleTunnelEnable)
	r.Post("/api/tunnel/disable", dashH.HandleTunnelDisable)
	r.Get("/api/tunnel/tailscale-check", dashH.HandleTailscaleCheck)
	r.Post("/api/tunnel/tailscale-enable", dashH.HandleTailscaleEnable)
	r.Post("/api/tunnel/tailscale-disable", dashH.HandleTailscaleDisable)

	// Single Sign-On settings checks + SP metadata (profile page)
	r.Post("/api/auth/oidc/test", ssoH.HandleOidcTest)
	r.Post("/api/auth/saml/test", ssoH.HandleSamlTest)
	r.Get("/api/auth/saml/metadata", ssoH.HandleSamlMetadata)

	// Mount OAuth routes under dashboard auth so web dashboard can initiate and exchange tokens
	oauthH := oauth.NewOAuthHandler(repo)
	mountOAuthRoutes(r, oauthH)
}

// SetupConsoleLogRoutes mounts operational log APIs behind a full dashboard
// session or local CLI token. Client API keys are intentionally rejected.
func SetupConsoleLogRoutes(r chi.Router, repo *db.Repo) {
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireConsoleLogAuth(repo))
		r.Get("/api/translator/console-logs", HandleConsoleLogsGet)
		r.Delete("/api/translator/console-logs", HandleConsoleLogsDelete)
		r.Get("/api/translator/console-logs/stream", HandleConsoleLogsStream)
		r.Get("/api/translator/console-logs/level", HandleConsoleLogsLevelGet)
		r.Put("/api/translator/console-logs/level", HandleConsoleLogsLevelPut)
	})
}

func mountOAuthRoutes(r interface {
	Get(pattern string, handlerFn http.HandlerFunc)
	Post(pattern string, handlerFn http.HandlerFunc)
}, oauthH *oauth.OAuthHandler) {
	r.Post("/api/oauth/{provider}/import", oauthH.HandleOAuthImport)
	r.Get("/api/oauth/kiro/social-authorize", oauthH.HandleOAuthKiroSocialAuthorize)
	r.Post("/api/oauth/kiro/social-exchange", oauthH.HandleOAuthKiroSocialExchange)
	r.Post("/api/oauth/kiro/import", oauthH.HandleKiroImport)
	r.Post("/api/oauth/kiro/import-cli-proxy", oauthH.HandleKiroImportCliProxy)
	r.Get("/api/oauth/kiro/auto-import", oauthH.HandleKiroAutoImport)
	r.Post("/api/oauth/kiro/api-key", oauthH.HandleKiroAPIKey)
	r.Post("/api/oauth/codex/bulk-import", oauthH.HandleOAuthCodexBulkImport)
	r.Post("/api/oauth/grok-cli/bulk-import", oauthH.HandleOAuthGrokCliBulkImport)
	r.Post("/api/oauth/freebuff/initiate", oauthH.HandleFreebuffInitiate)
	r.Post("/api/oauth/freebuff/poll", oauthH.HandleFreebuffPoll)
	r.Get("/api/oauth/freebuff/session", oauthH.HandleFreebuffSessionStatus)
	r.Post("/api/oauth/freebuff/session/switch", oauthH.HandleFreebuffSessionSwitch)
	r.Get("/api/oauth/antigravity/authorize", oauthH.HandleAntigravityAuthorize)
	r.Post("/api/oauth/antigravity/exchange", oauthH.HandleAntigravityExchange)
	r.Get("/api/oauth/cline/authorize", oauthH.HandleClineAuthorize)
	r.Post("/api/oauth/cline/exchange", oauthH.HandleClineExchange)
	r.Get("/api/oauth/pkce/authorize", oauthH.HandlePKCEAuthorize)
	r.Post("/api/oauth/pkce/exchange", oauthH.HandlePKCEExchange)
	// Codex's OAuth client only accepts its registered loopback redirect URI,
	// so the callback is served by a dedicated fixed-port listener rather than
	// the dashboard's /callback page. See internal/handlers/oauth/codex_proxy.go.
	r.Get("/api/oauth/codex/start-proxy", oauthH.HandleCodexStartProxy)
	r.Get("/api/oauth/codex/poll-status", oauthH.HandleCodexPollStatus)
	r.Get("/api/oauth/codex/stop-proxy", oauthH.HandleCodexStopProxy)
	r.Get("/api/oauth/authcode/authorize", oauthH.HandleAuthCodeAuthorize)
	r.Post("/api/oauth/authcode/exchange", oauthH.HandleAuthCodeExchange)
	r.Get("/api/oauth/trae/authorize", oauthH.HandleTraeAuthorize)
	r.Post("/api/oauth/trae/exchange", oauthH.HandleTraeExchange)
	r.Get("/api/oauth/windsurf/authorize", oauthH.HandleWindsurfAuthorize)
	r.Post("/api/oauth/windsurf/exchange", oauthH.HandleWindsurfExchange)
	r.Get("/api/oauth/zed/authorize", oauthH.HandleZedAuthorize)
	r.Post("/api/oauth/zed/exchange", oauthH.HandleZedExchange)
	r.Post("/api/oauth/device/start", oauthH.HandleDeviceStart)
	r.Post("/api/oauth/device/poll", oauthH.HandleDevicePoll)
	r.Post("/api/oauth/cursor/import", oauthH.HandleCursorImport)
	r.Get("/api/oauth/cursor/auto-import", oauthH.HandleCursorAutoImport)
	r.Get("/api/oauth/kimchi/authorize", oauthH.HandleKimchiAuthorize)
	r.Post("/api/oauth/kimchi/exchange", oauthH.HandleKimchiExchange)
	r.Post("/api/oauth/gitlab/pat", oauthH.HandleGitlabPAT)
	r.Post("/api/oauth/iflow/cookie", oauthH.HandleIflowCookie)
	r.Get("/api/oauth/xiaomi-mimo/authorize", oauthH.HandleMimoAuthorize)
	r.Post("/api/oauth/xiaomi-mimo/exchange", oauthH.HandleMimoExchange)
}

// SetupServerRouter mounts public endpoints (/health, /api/hello) and
// API-key protected routes (all engine + admin routes) on the chi router.
func SetupServerRouter(r chi.Router, repo *db.Repo, ts *TokenSaverConfig) {
	// Public (unauthenticated) endpoints
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
		w.Write([]byte(`{"status":"ok"}`))
	})
	// Version info is public (upstream PUBLIC_API_PATHS): the Sidebar polls
	// /api/version on every dashboard page including /login, before any
	// session or API key exists.
	versionH := chat.NewChatHandler(repo, ts)
	r.Get("/version", versionH.HandleVersion)
	r.Get("/api/version", versionH.HandleVersion)
	r.Get("/api/version/status", versionH.HandleVersionStatus)
	r.Get("/api/version/check", versionH.HandleCheckUpdate)
	// Embedded Native Dashboard SPA
	webH := web.Handler()
	oauthH := oauth.NewOAuthHandler(repo)
	// Public OAuth landing page: providers redirect browsers here after login.
	// Browsers carry neither the dashboard session nor the engine API key.
	r.Get("/callback", oauthH.HandleCallbackPage)
	r.Get("/", webH.ServeHTTP)
	r.Get("/login", webH.ServeHTTP)
	// Dashboard pages require the login session when requireLogin is on; the
	// guard redirects browsers to /login (upstream dashboardGuard).
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireDashboardPage(repo))
		r.Get("/dashboard", webH.ServeHTTP)
		r.Get("/dashboard/*", webH.ServeHTTP)
	})
	r.Get("/media", webH.ServeHTTP)
	r.Get("/media-providers/*", webH.ServeHTTP)
	r.Get("/media-providers/web", webH.ServeHTTP)
	r.Get("/connections", webH.ServeHTTP)
	r.Get("/combos", webH.ServeHTTP)
	r.Get("/analytics", webH.ServeHTTP)
	r.Get("/terminal", webH.ServeHTTP)
	r.Get("/keys", webH.ServeHTTP)
	r.Get("/settings", webH.ServeHTTP)
	r.Get("/providers", webH.ServeHTTP)
	r.Get("/usage", webH.ServeHTTP)
	r.Get("/quota", webH.ServeHTTP)
	r.HandleFunc("/assets/*", webH.ServeHTTP)
	r.HandleFunc("/providers/*", webH.ServeHTTP)
	r.HandleFunc("/icons/*", webH.ServeHTTP)
	r.HandleFunc("/favicon.ico", webH.ServeHTTP)
	r.HandleFunc("/favicon.svg", webH.ServeHTTP)
	r.HandleFunc("/icons.svg", webH.ServeHTTP)
	r.HandleFunc("/sw.js", webH.ServeHTTP)
	r.HandleFunc("/manifest.webmanifest", webH.ServeHTTP)
	r.HandleFunc("/manifest.json", webH.ServeHTTP)
	r.HandleFunc("/api/hello", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			w.Write([]byte(`{"status":"ok","message":"hello"}`))
		}
	})
	// Dashboard login session. status/login/logout/require-login are public so
	// the login page can load before a cookie exists (upstream PUBLIC_API_PATHS).
	dashH := dashboard.NewDashboardHandler(repo, ts)
	if versionH != nil {
		dashH.SemanticCache = versionH.SemanticCache
	}
	r.Get("/api/auth/status", dashH.HandleAuthStatus)
	r.Post("/api/auth/login", dashH.HandleAuthLogin)
	r.Post("/api/auth/logout", dashH.HandleAuthLogout)
	r.Get("/api/settings/require-login", dashH.HandleRequireLogin)
	r.Get("/api/tunnel/status", dashH.HandleTunnelStatus)

	// Admin-only operations (health reset, shutdown, update) - strictly requires session cookie or CLI token.
	// Standard client API keys are rejected, matching upstream ALWAYS_PROTECTED.
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAdminAuth())

		// Health reset endpoint — dashboard calls this via headroom proxy
		r.Post("/admin/health/reset", func(w http.ResponseWriter, r *http.Request) {
			provider := r.URL.Query().Get("provider")
			model := r.URL.Query().Get("model")
			if err := repo.ResetProviderHealth(provider, model); err != nil {
				handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
				return
			}
			w.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
			json.MarshalWrite(w, map[string]string{"status": "ok"})
		})

		chatH := chat.NewChatHandler(repo, ts)
		r.Post("/api/version/update", chatH.HandleTriggerUpdate)
		r.Post("/api/version/auto-update", chatH.HandleToggleAutoUpdate)
		r.Post("/api/version/shutdown", HandleShutdown)

		// Profiling endpoints (pprof) — disabled by default in production for security;
		// enable explicitly via PPROF_ENABLED=true. Gated behind RequireAdminAuth
		// (issue #126) so debug surface is never exposed publicly.
		if strings.EqualFold(strings.TrimSpace(os.Getenv("PPROF_ENABLED")), "true") {
			r.HandleFunc("/debug/pprof/", pprof.Index)
			r.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
			r.HandleFunc("/debug/pprof/profile", pprof.Profile)
			r.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
			r.HandleFunc("/debug/pprof/trace", pprof.Trace)
			r.HandleFunc("/debug/pprof/*", pprof.Index)
		}
	})

	// API-key protected domain routes. The rate limiter sits after
	// RequireApiKey so it reads the per-key RPM/TPM/concurrency limits off the
	// authenticated key. Guardrails sit after both: a policy is scoped by API
	// key, and a caller the limiter already rejected must not be able to make
	// the gateway scan content either.
	// The limiter's window is a startup read: every in-memory sliding window is
	// sized against it, so changing it has to restart the gateway.
	window := time.Duration(repo.GetRateLimitDefaults().WindowSeconds) * time.Second
	rateLimiter := middleware.NewRateLimiter(window)
	// The fallback itself is read per request, so an operator can raise or drop
	// a global default without a restart. Only the window, which the live
	// windows already depend on, is pinned at boot.
	defaults := func() middleware.Limits {
		d := repo.GetRateLimitDefaults()
		if !d.Enabled {
			return middleware.Limits{}
		}
		return middleware.Limits{RPM: d.RPM, TPM: d.TPM, Concurrency: d.Concurrency}
	}
	guardrailStore := guardrails.NewStore(repo.RawDB())
	// One switch for both taps. It reads the settings row per request, which is
	// a single indexed read on an already-loaded connection, and buys an
	// operator an off switch that does not require deleting their policies.
	guardrailSwitch := guardrails.Switch(func() bool { return repo.GetGuardrailsEnabled() })
	var engineChatH *chat.ChatHandler
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireApiKey(repo))
		r.Use(middleware.RequireRateLimit(rateLimiter, defaults))
		r.Use(guardrails.Inbound(guardrailStore, guardrailAudit(repo), guardrailSwitch))
		engineChatH = SetupRoutes(r, repo, ts)
	})

	// Dashboard management API: login-gated when requireLogin is on, but still
	// reachable with a valid API key or the local CLI token (upstream parity).
	// It reads the cache through the engine's chat handler: versionH was built
	// before any traffic, so the cache on that instance never fills.
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireDashboardAuth(repo))
		SetupDashboardRoutes(r, repo, engineChatH)
	})

	// CLI Tools status is a dashboard read: the SPA calls it with the session
	// cookie, never an LLM API key. It was registered inside SetupRoutes, which
	// is mounted under RequireApiKey, so every dashboard call 401'd and the view
	// bounced to the endpoint tab. Reachable with a session, an API key, or the
	// local CLI token — same policy as the rest of the dashboard group.
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireDashboardAuth(repo))
		r.Get("/api/cli-tools/all-statuses", media.NewCLIToolsHandler().HandleAllStatuses)
	})

	SetupConsoleLogRoutes(r, repo)
}
