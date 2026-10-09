package chat

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
	internalproxy "9router/proxy/internal/proxy"
	"9router/proxy/internal/translator"
)

// CredentialFallbacks maps search/tool providers to the primary chat provider whose API key can be reused.
var CredentialFallbacks = map[string]string{
	"ollama-search": "ollama",
	"zai-search":    "glm",
	"cline":         "clinepass",
	"clinepass":     "cline",
}

func (h *ChatHandler) ResolveProviderProxyPoolID(provider string) string {
	if h.Repo == nil {
		return ""
	}
	settings, err := h.Repo.GetSettings()
	if err != nil || settings == nil || settings.ProviderStrategies == nil {
		return ""
	}
	var singleID, rotate string
	for _, p := range providerStrategyKeys(provider) {
		if strat, ok := settings.ProviderStrategies[p]; ok {
			if singleID == "" && strat.ProxyPoolID != "" && strat.ProxyPoolID != "__none__" {
				singleID = strat.ProxyPoolID
			}
			if rotate == "" && db.IsProxyPoolRotation(strat.ProxyRotateStrategy) {
				rotate = strat.ProxyRotateStrategy
			}
		}
	}
	// Pool rotation is a NoAuth-provider feature: the provider card offers it
	// only inside its `isNoAuth` block, and there `rotateStrategy` holds a POOL
	// rotation. On a keyed provider the same key holds ACCOUNT rotation, so
	// honouring it here would send that provider's egress through pools its
	// operator never configured.
	if rotate != "" && providers.IsNoAuthProvider(provider) {
		if id := h.rotatedActiveProxyPool(provider, rotate); id != "" {
			return id
		}
	}
	return singleID
}

// poolRotationCursors holds one round-robin cursor per provider. A single
// shared counter let one provider's traffic advance another's, so a provider
// with a different number of active pools skipped positions.
var poolRotationCursors sync.Map // map[string]*atomic.Uint64

// rotatedActiveProxyPool picks the next pool for a provider under a rotation
// strategy. The cursor is that provider's own round-robin position, so two
// providers rotating at once advance independently.
//
// A pool with no URL is excluded upstream: it cannot serve a request, and
// selecting it would hand a share of the traffic to a pool the resolver then
// refuses.
func (h *ChatHandler) rotatedActiveProxyPool(provider, strategy string) string {
	if h.Repo == nil {
		return ""
	}
	ids := h.Repo.ActivePoolIDs()
	if len(ids) == 0 {
		return ""
	}
	if strings.ToLower(strings.TrimSpace(strategy)) == "random" {
		return ids[rand.IntN(len(ids))]
	}
	// Keyed on the resolved alias so `oc` and `opencode` share one cursor: they
	// are the same provider and must advance together.
	cursor, _ := poolRotationCursors.LoadOrStore(providers.ResolveAlias(provider), new(atomic.Uint64))
	idx := cursor.(*atomic.Uint64).Add(1) - 1
	return ids[idx%uint64(len(ids))]
}

// providerStrategyKeys lists the settings keys a provider's pool may be stored
// under, most specific first: the id as given, its dashboard alias, and the
// keys of the provider that shares its upstream account pool.
func providerStrategyKeys(provider string) []string {
	keys := []string{provider}
	seen := map[string]bool{provider: true}
	add := func(key string) {
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		keys = append(keys, key)
	}
	add(providers.GetProviderAlias(provider))
	if canon := providers.ResolveAlias(provider); canon != "" {
		add(canon)
		add(providers.GetProviderAlias(canon))
	}
	// cline and clinepass are one upstream account pool, and the dashboard
	// publishes cline as "cl" while clinepass keeps its own id, so both keys
	// must be reachable from either side.
	if counterpart := proxyPoolCounterpart(provider); counterpart != "" {
		add(counterpart)
		add(providers.GetProviderAlias(counterpart))
	}
	return keys
}

// connRotationStrategy resolves which connection-rotation strategy applies to a
// provider, for the code paths that rotate accounts.
//
// A NoAuth provider is the case that needs care: its card writes pool rotation
// to `rotateStrategy` and account rotation to `fallbackStrategy`, and both land
// in the same stored entry. Reading RotateStrategy there would let the operator
// rotate pools and silently start rotating connections too, so for those
// providers only `fallbackStrategy` is trusted. Keyed providers keep the
// original read, where `rotateStrategy` genuinely is the connection strategy.
func connRotationStrategy(settings *db.SettingsData, provider string) db.ProviderStrategy {
	strat := db.ProviderStrategy{}
	hasStrat := false
	if settings != nil && settings.ProviderStrategies != nil {
		if s, ok := settings.ProviderStrategies[provider]; ok {
			strat, hasStrat = s, true
		}
	}
	if providers.IsNoAuthProvider(provider) {
		strat.RotateStrategy = strat.ConnRotateStrategy
	}
	if settings != nil && (!hasStrat || strat.RotateStrategy == "") {
		if settings.FallbackStrategy != "" && settings.FallbackStrategy != "fill-first" {
			strat.RotateStrategy = settings.FallbackStrategy
			strat.StickyLimit = settings.StickyRoundRobinLimit
		}
	}
	return strat
}

// proxyPoolCounterpart returns the provider that shares a pool with this one
// upstream (cline and clinepass are the same upstream account pool), so an
// assignment made on one applies to the other.
func proxyPoolCounterpart(provider string) string {
	switch provider {
	case "cline":
		return "clinepass"
	case "clinepass":
		return "cline"
	}
	return ""
}

// GetBestConnection retrieves the highest-priority active connection for a provider.
// When connectionID is non-empty, it fetches that specific connection directly.
func (h *ChatHandler) GetBestConnection(provider string, connectionID string, excludeIDs []string, model string) (*models.ProviderConnection, *ConnectionData, error) {
	return h.getBestConnection(provider, connectionID, excludeIDs, model)
}

// GetBestConnectionWithContext is the context-aware variant of GetBestConnection.
func (h *ChatHandler) GetBestConnectionWithContext(ctx context.Context, provider string, connectionID string, excludeIDs []string, model string) (*models.ProviderConnection, *ConnectionData, error) {
	return h.getBestConnectionWithContext(ctx, provider, connectionID, excludeIDs, model)
}

func (h *ChatHandler) getBestConnection(provider string, connectionID string, excludeIDs []string, model string) (*models.ProviderConnection, *ConnectionData, error) {
	return h.getBestConnectionWithContext(context.Background(), provider, connectionID, excludeIDs, model)
}

func (h *ChatHandler) getBestConnectionWithContext(ctx context.Context, provider string, connectionID string, excludeIDs []string, model string) (*models.ProviderConnection, *ConnectionData, error) {
	if model != "" && !h.Repo.IsProviderAvailable(provider, model) {
		log.Warn("health", "unhealthy provider", "provider", provider, "model", model)
	}

	var conn *models.ProviderConnection
	var err error
	isProbe := handlerutil.IsProbeContext(ctx)

	if connectionID != "" {
		conn, err = h.Repo.GetProviderConnectionByID(connectionID)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to fetch connection %s: %w", connectionID, err)
		}
		if conn == nil {
			return nil, nil, fmt.Errorf("connection %s not found", connectionID)
		}
		// A pinned connection must still belong to the requested provider:
		// callers forward x-connection-id straight from the client, so without
		// this check a connection for provider A could serve provider B and
		// send A's credentials to B's upstream (upstream getProviderCredentials
		// always scopes the lookup to the provider).
		if conn.Provider != provider {
			return nil, nil, fmt.Errorf("connection %s belongs to provider %s, not %s", connectionID, conn.Provider, provider)
		}
		// Upstream resolves the pin inside availableConnections
		// (src/sse/services/auth.js:143-148), so a disabled or model-locked
		// row never matches and the request falls through to the configured
		// strategy. Honouring the pin unconditionally made the dashboard's
		// enable/disable toggle a no-op for every pinned request.
		ineligible, reason := h.pinnedConnectionIneligible(conn, excludeIDs, model)
		if ineligible && isProbe && conn.IsActive == 1 && !slices.Contains(excludeIDs, conn.ID) {
			if strings.HasPrefix(reason, "account cooldown") || strings.HasPrefix(reason, "model lock") || reason == "quota cache" {
				// Probes bypass cooldown / locks to test actual upstream reachability,
				// but must still honor model enablement, disabled accounts, and strict assignment.
				ineligible = false
			}
		}
		if ineligible {
			log.Warn("connections", "pinned connection ineligible, falling back to strategy",
				"provider", provider, "conn", conn.ID, "reason", reason)
			connectionID = ""
			conn = nil
		}
	}

	if conn == nil {
		connections, queryErr := h.Repo.GetProviderConnections(provider, true)
		if queryErr != nil {
			return nil, nil, fmt.Errorf("failed to query connections for %s: %w", provider, queryErr)
		}
		if len(connections) == 0 {
			if fallbackProvider, ok := CredentialFallbacks[provider]; ok {
				fallbackConns, fallbackErr := h.Repo.GetProviderConnections(fallbackProvider, true)
				if fallbackErr == nil && len(fallbackConns) > 0 {
					connections = fallbackConns
				}
			}
		}
		if len(connections) == 0 {
			if cfg, ok := providers.KnownProviders[provider]; ok && cfg.NoAuth {
				// Inject virtual connection for no-auth provider with optional proxy pool strategy from settings
				connData := &ConnectionData{
					AccessToken: "public",
				}
				connData.ProxyPoolID = h.ResolveProviderProxyPoolID(provider)
				publicName := "Public"
				conn := &models.ProviderConnection{
					ID:       "noauth",
					Provider: provider,
					Name:     &publicName,
					IsActive: 1,
				}
				return conn, connData, nil
			}
			return nil, nil, fmt.Errorf("no active connections for provider: %s", provider)
		}

		settings, settingsErr := h.Repo.GetSettings()
		connections = filterConnectionsForModel(provider, connections, model, settings)

		if len(connections) == 0 {
			return nil, nil, fmt.Errorf("no connection assigned to model %s for provider %s under strict assignment", model, provider)
		}

		// A codex account only serves the models it was granted. The pin is
		// read before the rotation so a full strategy sweep cannot land on an
		// account that cannot serve the request (upstream filters
		// availableConnections before the pin and before the strategy —
		// src/sse/services/auth.js:86-89, :100-148).
		connections = filterConnectionsByEnabledModels(provider, connections, model)
		if len(connections) == 0 {
			return nil, nil, fmt.Errorf("no connection for provider %s has model %s enabled", provider, model)
		}

		// Rotate only connections eligible for the requested model.
		if len(connections) > 1 && settingsErr == nil && settings != nil {
			strat := connRotationStrategy(settings, provider)
			if strat.RotateStrategy != "" && strat.RotateStrategy != "none" {
				connections = h.applyConnectionStrategy(connections, strat)
			}
		}

		excludeSet := make(map[string]bool, len(excludeIDs))
		for _, id := range excludeIDs {
			excludeSet[id] = true
		}

		conn = nil
		var cooldownUntil time.Time
		var inCooldown []*models.ProviderConnection
		now := time.Now()
		for _, c := range connections {
			if excludeSet[c.ID] {
				continue
			}
			// The strategy sweep reorders the whole candidate list, so the
			// account filter runs again on the row actually reached.
			if !codexAccountServesModel(c, model) {
				continue
			}
			// Per-model lock and quota cache first: an account parked for
			// this model carries no cooldown to shorten, so it must not enter
			// the forced pool either (h.connectionModelBlocked).
			if !isProbe {
				if h.connectionModelBlocked(c, provider, model) {
					continue
				}
				// Account-scoped cooldown. An account whose quota is spent, or
				// whose OAuth grant the provider already rejected, is skipped
				// before it is selected — round-robin otherwise kept handing out
				// dead accounts until a live 429/401 locked them, and a rejected
				// grant cost a token-endpoint call on every request until the IP
				// was rate limited. Upstream parity: filterAvailableAccounts.
				if until, ok := db.ConnectionBlockedUntil(c.Data); ok && until.After(now) {
					inCooldown = append(inCooldown, c)
					if cooldownUntil.IsZero() || until.Before(cooldownUntil) {
						cooldownUntil = until
					}
					continue
				}
			}
			conn = c
			break
		}
		if conn == nil && forceFallbackEnabled(settings) {
			// The operator asked for traffic to keep flowing while every
			// account is cooling down, so take the account that frees up
			// first instead of failing the client turn outright
			// (upstream decolua/9router PR #130).
			forced, until, ok := h.forceMinCooldownConnection(provider, inCooldown, excludeSet, model, settings)
			if ok {
				conn = forced
				log.Warn("connections", "all accounts in cooldown, forcing the soonest to reset",
					"provider", provider, "conn", forced.ID, "reset", until.UTC().Format(time.RFC3339))
			}
		}
		if conn == nil {
			// Every candidate was in cooldown, so say when the first one comes
			// back instead of a bare "all excluded" the caller cannot act on.
			if !cooldownUntil.IsZero() {
				return nil, nil, fmt.Errorf(
					"no available connections for provider: %s (all in cooldown, earliest reset %s)",
					provider, cooldownUntil.UTC().Format(time.RFC3339))
			}
			return nil, nil, fmt.Errorf("no available connections for provider: %s (all excluded)", provider)
		}
	}

	var connData ConnectionData
	if conn.Data != "" {
		if err := json.Unmarshal([]byte(conn.Data), &connData); err != nil {
			return nil, nil, fmt.Errorf("failed to parse connection data: %w", err)
		}
	}
	// The dashboard's proxy assignment endpoint (upstream parity) stores the
	// binding in providerSpecificData.proxyPoolId, while the top-level field is
	// what this resolver reads. Accept both so a pool bound from either writer
	// takes effect.
	if connData.ProxyPoolID == "" {
		if poolID, ok := connData.ProviderSpecificData["proxyPoolId"].(string); ok && poolID != "" && poolID != "__none__" {
			connData.ProxyPoolID = poolID
		}
	}

	// providerSpecificData.baseUrl is where the dashboard stores a per-connection
	// endpoint override (the Ollama Local host field, an Azure endpoint, the
	// compatible-node URL). ConnectionData.BaseURL only carries the top-level
	// baseUrl key, so an override written by the newer writer was parsed and then
	// discarded — the request silently fell back to the registry default. Upstream
	// reads the same nested field (open-sse/config/providers.js
	// resolveOllamaLocalHost), so hydrate it here rather than in each caller.
	if connData.BaseURL == "" {
		if override, ok := connData.ProviderSpecificData["baseUrl"].(string); ok {
			if trimmed := strings.TrimSpace(override); trimmed != "" {
				connData.BaseURL = trimmed
			}
		}
	}
	// No per-connection binding: fall back to the pool assigned to the
	// provider itself. It used to be read only for the synthesized no-auth
	// connection, so a stored connection carrying its own key went out
	// directly — the assignment the dashboard shows was never dialled.
	h.applyProviderProxyPool(&connData, provider)

	return conn, &connData, nil
}

// connectionModelBlocked reports whether a connection is parked for this
// specific model — an active per-model lock or a provider quota cache. An
// empty model means nothing model-scoped applies (upstream parity: the lock
// filter only runs for a model-bearing request).
func (h *ChatHandler) connectionModelBlocked(c *models.ProviderConnection, provider, model string) bool {
	if model == "" {
		return false
	}
	lockKey := canonicalLockModel(provider, model)
	if locked, _ := h.Repo.IsConnectionModelLocked(c.ID, lockKey); locked {
		return true
	}
	if lockKey != model {
		if locked, _ := h.Repo.IsConnectionModelLocked(c.ID, model); locked {
			return true
		}
	}
	return quotaCacheBlocked(provider, c.ID, model)
}

// applyProviderProxyPool binds a provider-level pool to a connection that has
// none of its own. An explicit per-connection binding always wins.
func (h *ChatHandler) applyProviderProxyPool(connData *ConnectionData, provider string) {
	if connData == nil || connData.ProxyPoolID != "" {
		return
	}
	if poolID := h.ResolveProviderProxyPoolID(provider); poolID != "" {
		connData.ProxyPoolID = poolID
	}
}

// pinnedConnectionIneligible reports whether a client-pinned connection must
// not serve the request, mirroring the availability filter upstream applies
// before it looks for a preferred connection (src/sse/services/auth.js:100-148):
// disabled rows, explicitly excluded rows, active model locks, and connections
// the provider's strict model assignment does not bind to this model.
func (h *ChatHandler) pinnedConnectionIneligible(conn *models.ProviderConnection, excludeIDs []string, model string) (bool, string) {
	if conn == nil {
		return true, "nil connection"
	}
	if conn.IsActive != 1 {
		return true, "disabled"
	}
	if slices.Contains(excludeIDs, conn.ID) {
		return true, "excluded"
	}
	if until, ok := db.ConnectionBlockedUntil(conn.Data); ok && until.After(time.Now()) {
		return true, "account cooldown until " + until.UTC().Format(time.RFC3339)
	}
	if model == "" {
		return false, ""
	}
	lockKey := canonicalLockModel(conn.Provider, model)
	if locked, _ := h.Repo.IsConnectionModelLocked(conn.ID, lockKey); locked {
		return true, "model lock " + lockKey
	}
	if lockKey != model {
		if locked, _ := h.Repo.IsConnectionModelLocked(conn.ID, model); locked {
			return true, "model lock " + model
		}
	}
	if quotaCacheBlocked(conn.Provider, conn.ID, model) {
		return true, "quota cache"
	}

	if !codexAccountServesModel(conn, model) {
		return true, "model not enabled on account"
	}

	settings, err := h.Repo.GetSettings()
	if err != nil || settings == nil {
		return false, ""
	}
	filtered := filterConnectionsForModel(conn.Provider, []*models.ProviderConnection{conn}, model, settings)
	if len(filtered) == 0 {
		return true, "strict model assignment"
	}
	return false, ""
}

// codexAccountServesModel reports whether a codex connection can serve model.
//
// Upstream skips a codex account whose providerSpecificData.enabledModels is a
// non-empty array that does not contain the requested model
// (src/sse/services/auth.js:86-89). The account is subscribed to a subset of
// the catalog, and the ones it is not are rejected by OpenAI with a 400 — so
// this is the one provider the pin gates, never a model-name guess about any
// other provider (AGENTS.md §3.B).
//
// enabledModels is read from providerSpecificData first and from the
// top-level field second, exactly like the /v1/models build
// (models_list.go buildModelsList), because both writers are in the wild.
// An empty or absent list leaves the account unrestricted, and an absent model
// (model == "") skips the check as upstream does.
func codexAccountServesModel(conn *models.ProviderConnection, model string) bool {
	if conn == nil || conn.Provider != "codex" || model == "" {
		return true
	}
	enabled := parseConnectionModelData(conn).EnabledModels()
	if len(enabled) == 0 {
		return true
	}
	return slices.Contains(enabled, model)
}

// filterConnectionsByEnabledModels drops the codex accounts that cannot serve
// the requested model, keeping every other provider's rows untouched.
func filterConnectionsByEnabledModels(provider string, conns []*models.ProviderConnection, model string) []*models.ProviderConnection {
	if provider != "codex" || model == "" {
		return conns
	}
	kept := make([]*models.ProviderConnection, 0, len(conns))
	for _, c := range conns {
		if codexAccountServesModel(c, model) {
			kept = append(kept, c)
		}
	}
	return kept
}

// quotaCacheBlocked reports whether a provider's in-memory quota cache says
// this connection must not serve the request. Each provider keeps its own
// cache, so this dispatches per provider and never crosses them (AGENTS.md
// §3.A strict provider isolation).
//
// Antigravity quota is per model, so it needs the model. Codex quota is
// account-level — one 5h and one 7d window covers every model the account can
// serve — so an exhausted window blocks the whole connection, not one model
// on it.
func quotaCacheBlocked(provider, connectionID, model string) bool {
	switch provider {
	case "antigravity":
		return IsAntigravityModelBlocked(connectionID, model)
	case "codex":
		return IsCodexConnectionExhausted(connectionID)
	}
	return false
}

// GetProviderConfig returns the upstream configuration for a provider.
func (h *ChatHandler) GetProviderConfig(provider string, connData *ConnectionData) (*providers.ProviderConfig, error) {
	return h.getProviderConfig(provider, connData)
}

func (h *ChatHandler) getProviderConfig(provider string, connData *ConnectionData) (*providers.ProviderConfig, error) {
	var baseCfg *providers.ProviderConfig

	if connData != nil && connData.BaseURL != "" {
		// The override is completed to a chat-completions route: the dashboard
		// stores a bare host for a self-hosted endpoint (the Ollama Local host
		// field is typed as "http://192.168.1.10:11434"), and a host alone names
		// no route, so the POST used to land on the server root. A URL that
		// already names a route is passed through untouched.
		baseURL := chatCompletionsURL(connData.BaseURL)
		if cfg, ok := providers.KnownProviders[provider]; ok {
			cloned := cfg
			cloned.BaseURL = baseURL
			baseCfg = &cloned
		} else {
			baseCfg = &providers.ProviderConfig{
				BaseURL:    baseURL,
				AuthHeader: constants.HeaderAuthorization,
				AuthScheme: constants.AuthSchemeBearer,
			}
		}
	} else if cfg, ok := providers.KnownProviders[provider]; ok {
		// Clone config so per-request headers don't mutate global registry
		cloned := cfg
		baseCfg = &cloned
	} else {
		node, nodeData, err := h.Repo.GetProviderNodeByID(provider)
		if err != nil {
			return nil, fmt.Errorf("failed to look up provider node %s: %w", provider, err)
		}
		if node != nil && nodeData != nil && nodeData.BaseURL != "" {
			baseCfg = &providers.ProviderConfig{
				BaseURL:    chatCompletionsURL(nodeData.BaseURL),
				AuthHeader: constants.HeaderAuthorization,
				AuthScheme: constants.AuthSchemeBearer,
			}
		}
	}

	if baseCfg == nil {
		return nil, fmt.Errorf("provider %q has no baseUrl in connection data and is not in KnownProviders", provider)
	}

	// Check if this connection uses an Edge Relay Proxy Pool (Vercel, Cloudflare, Deno)
	if connData != nil {
		var relayURL string
		var noProxy string

		if connData.ProxyPoolID != "" {
			if pool, err := h.Repo.GetProxyPool(connData.ProxyPoolID); err == nil && pool != nil && pool.IsActive {
				if pool.IsEdgeRelay() {
					relayURL = pool.NextURL()
					noProxy = pool.NoProxy
					logProxyOnce(connData.ProxyPoolID, relayURL, pool.Type)
				}
			}
		}
		if relayURL == "" && connData.ProviderSpecificData != nil {
			if u, ok := connData.ProviderSpecificData["vercelRelayUrl"].(string); ok && u != "" {
				relayURL = u
				if np, ok := connData.ProviderSpecificData["connectionNoProxy"].(string); ok {
					noProxy = np
				}
			}
		}

		if relayURL != "" && !internalproxy.ShouldBypassNoProxy(baseCfg.BaseURL, noProxy) {
			cloned := *baseCfg
			cloned.StaticHeaders = internalproxy.BuildEdgeRelayHeaders(baseCfg.BaseURL, cloned.StaticHeaders)
			cloned.BaseURL = relayURL
			return h.applyProviderOverrides(provider, &cloned), nil
		}
	}

	return h.applyProviderOverrides(provider, baseCfg), nil
}

// chatCompletionsURL completes a configured endpoint into the chat-completions
// route, and leaves one that already names a route untouched.
//
// A configured endpoint arrives in one of two shapes: the full route, or a bare
// host — the compatible-node baseUrl and the Ollama Local host field both take
// this form, and a dotted IP is the common case
// ("http://192.168.1.10:11434"). A bare host names no route upstream, so the
// gateway's /v1 is what makes it a chat endpoint.
//
// Detection is by shape, not by guessing whether the last path segment looks
// like a filename: an early version did that and exempted exactly the dotted-IP
// host it existed to fix. A URL whose path is only a version prefix (/v1, and
// the /v1beta Google uses) is that prefix plus the route; a path that carries
// any segment beyond it is taken to be an endpoint the operator named, and is
// left alone rather than having /v1 appended behind it.
func chatCompletionsURL(baseURL string) string {
	url := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if url == "" {
		return baseURL
	}
	for _, route := range []string{
		"/chat/completions", "/messages", "/responses", "/completions",
		"/chat", "/embeddings", "/api/chat", "/api/generate",
	} {
		if strings.HasSuffix(url, route) {
			return url
		}
	}
	// Split off the scheme and authority, then decide from the path alone: no
	// path at all, or only a version prefix, means nothing named an endpoint and
	// the route is appended to the prefix that is there.
	_, rest, found := strings.Cut(url, "://")
	if !found {
		return url + "/v1/chat/completions"
	}
	_, path, _ := strings.Cut(rest, "/")
	switch trimmed := strings.Trim(path, "/"); {
	case trimmed == "":
		return url + "/v1/chat/completions"
	case isVersionPrefix(trimmed):
		// Already versioned ("https://host/v1"): the lane is the prefix plus the
		// route, not a second prefix.
		return url + "/chat/completions"
	default:
		// A path carrying a segment beyond the prefix names an endpoint the
		// operator chose; appending behind it would only produce a 404.
		return url
	}
}

// isVersionPrefix reports whether a path segment is only an API version, as in
// "/v1", "/v2", or Gemini's "/v1beta". Anything else names an endpoint of its
// own and must not be treated as a prefix.
func isVersionPrefix(segment string) bool {
	rest, ok := strings.CutPrefix(segment, "v")
	if !ok {
		return false
	}
	digits, _ := strings.CutSuffix(rest, "beta")
	return isDigits(digits)
}

// isDigits reports whether s is non-empty and every character is an ASCII digit.
func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// applyProviderOverrides merges the operator's stored header overrides for a
// provider into a per-request config.
//
// This is the single injection point on purpose: every executor builds its
// outbound headers from cfg.StaticHeaders, so merging here reaches all of
// them without each one learning the feature. It is also after the relay
// rewrite, so a relay's own headers are on the table the override wins over —
// which is what upstream's executor-level merge does too.
func (h *ChatHandler) applyProviderOverrides(provider string, cfg *providers.ProviderConfig) *providers.ProviderConfig {
	if cfg == nil || h.Repo == nil {
		return cfg
	}
	stored, err := h.Repo.GetProviderOverride(ProviderOverrideKey(provider))
	if err != nil || stored == nil || len(stored.Headers) == 0 {
		return cfg
	}
	merged := *cfg
	merged.StaticHeaders = providers.MergeHeaderOverrides(cfg.StaticHeaders, stored.Headers)
	return &merged
}

// ProviderOverrideKey resolves the settings key one provider's overrides are
// stored under. The dashboard writes the canonical registry id (upstream keys
// everything by `resolveProviderAlias(id)`), while the request path knows a
// connection's provider as the alias a model id carries — `cc/claude-opus-4-5`
// arrives as `claude`, the alias, not the canonical id. Both sides therefore
// canonicalize, which is what makes one entry answer for both spellings.
//
// A custom provider node's id has no alias mapping, so it resolves to itself.
func ProviderOverrideKey(provider string) string {
	if provider == "" {
		return provider
	}
	return providers.ResolveAlias(provider)
}

// ExtractAPIKey gets the API key from a connection's data.
func ExtractAPIKey(connData *ConnectionData) string {
	return extractAPIKey(connData)
}

func extractAPIKey(connData *ConnectionData) string {
	if connData.APIKey != "" {
		return connData.APIKey
	}
	return connData.AccessToken
}

// resolveProviderAuthToken picks which credential a provider must actually send.
// Kiro is the only provider where both fields can be present and the right one
// is not the API key: upstream (open-sse/executors/kiro.js buildHeaders) uses the
// apiKey solely for `authMethod: "api_key"` connections and the OAuth
// accessToken everywhere else. Sending the wrong one makes CodeWhisperer answer
// 403 "The bearer token included in the request is invalid." even though the
// access token is perfectly valid.
func resolveProviderAuthToken(provider string, connData *ConnectionData, current string) string {
	if connData == nil || provider != "kiro" {
		return current
	}
	authMethod, _ := connData.ProviderSpecificData["authMethod"].(string)
	if authMethod == "api_key" && connData.APIKey != "" {
		return connData.APIKey
	}
	if connData.AccessToken != "" {
		return connData.AccessToken
	}
	if connData.APIKey != "" {
		return connData.APIKey
	}
	return current
}

// NormalizeProviderToken normalizes credentials for providers with specific token requirements.
// Only Cline OAuth tokens — WorkOS JWTs (base64url "eyJ…" with a dot) — take
// the "workos:" prefix; ClinePass API keys (e.g. "clp_…") ride plain Bearer
// (upstream parity: open-sse/shared/clineAuth.js getClineAccessToken).
func NormalizeProviderToken(provider, token string) string {
	if (provider == "cline" || provider == "clinepass") && token != "" {
		t := strings.TrimSpace(token)
		if isClineWorkOSJWT(t) {
			return "workos:" + t
		}
		return t
	}
	return token
}

// isClineWorkOSJWT reports whether token looks like a Cline OAuth WorkOS JWT:
// base64url "eyJ…" header followed by a dot (upstream parity:
// open-sse/shared/clineAuth.js getClineAccessToken). Non-JWT ClinePass API
// keys (e.g. "clp_…") fail this check and ride plain Bearer.
func isClineWorkOSJWT(token string) bool {
	if strings.HasPrefix(token, "workos:") || !strings.HasPrefix(token, "eyJ") {
		return false
	}
	dot := strings.IndexByte(token, '.')
	if dot <= 3 {
		return false
	}
	for i := 0; i < dot; i++ {
		c := token[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func extractAssignedModel(dataStr string) string {
	if dataStr == "" {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(dataStr), &m); err != nil {
		return ""
	}
	if am, ok := m["assignedModel"].(string); ok && am != "" {
		return am
	}
	if fm, ok := m["freebuffModel"].(string); ok && fm != "" {
		return fm
	}
	if psd, ok := m["providerSpecificData"].(map[string]any); ok {
		if am, ok := psd["assignedModel"].(string); ok && am != "" {
			return am
		}
		if fm, ok := psd["freebuffModel"].(string); ok && fm != "" {
			return fm
		}
	}
	return ""
}

func filterConnectionsForModel(provider string, connections []*models.ProviderConnection, model string, settings *db.SettingsData) []*models.ProviderConnection {
	if settings == nil || settings.ProviderStrategies == nil || model == "" {
		return connections
	}
	strat, ok := settings.ProviderStrategies[provider]
	if !ok || !strat.StrictModelAssignment {
		return connections
	}

	var filtered []*models.ProviderConnection
	for _, c := range connections {
		if c == nil {
			continue
		}
		if extractAssignedModel(c.Data) == model {
			filtered = append(filtered, c)
		}
	}
	return filtered
}

// canonicalLockModel normalizes model names for providers sharing a backend
// quota/capacity pool (such as Antigravity gemini-3.8-flash-low/high -> gemini-3.8-flash-tiered).
func canonicalLockModel(provider, model string) string {
	if model == "" {
		return ""
	}
	if provider == "antigravity" {
		return translator.NormalizeAntigravityModel(model)
	}
	return model
}

// ApplyConnectionStrategy rotates candidate connections according to the provider's configured strategy.
func (h *ChatHandler) ApplyConnectionStrategy(conns []*models.ProviderConnection, strat db.ProviderStrategy) []*models.ProviderConnection {
	return h.applyConnectionStrategy(conns, strat)
}

func (h *ChatHandler) applyConnectionStrategy(conns []*models.ProviderConnection, strat db.ProviderStrategy) []*models.ProviderConnection {
	if len(conns) <= 1 {
		return conns
	}

	switch strings.ToLower(strings.TrimSpace(strat.RotateStrategy)) {
	case "round-robin", "roundrobin", "sticky":
		stickyLimit := strat.StickyLimit
		if stickyLimit <= 0 {
			stickyLimit = 1
		}
		return h.selectByRecency(conns, stickyLimit)

	case "random":
		offset := rand.IntN(len(conns))
		rotated := make([]*models.ProviderConnection, len(conns))
		for i := range conns {
			rotated[i] = conns[(offset+i)%len(conns)]
		}
		return rotated

	default:
		// "none", "fallback", or empty: keep DB priority order
		return conns
	}
}

// selectByRecency implements persistent round-robin, ported from upstream
// getProviderCredentials (src/sse/services/auth.js:151-189). Upstream keeps no
// in-memory rotation index: it picks the most recently used row, holds it while
// its consecutive-use count is below the sticky limit, otherwise moves to the
// least recently used one, and writes the winner's stamp back. The Go port
// instead held a positional index keyed only by provider, which reset to the
// top account on every restart and advanced on every internal selection rather
// than once per request.
func (h *ChatHandler) selectByRecency(conns []*models.ProviderConnection, stickyLimit int) []*models.ProviderConnection {
	// Most recently used wins the tie-break by list order, which is priority
	// order, so two rows stamped in the same second rotate deterministically.
	currentIdx := -1
	for i, c := range conns {
		if c == nil || c.LastUsedAt == nil {
			continue
		}
		if currentIdx < 0 || *conns[currentIdx].LastUsedAt <= *c.LastUsedAt {
			currentIdx = i
		}
	}

	winner := -1
	consecutive := 1
	if currentIdx >= 0 {
		count := 0
		if conns[currentIdx].ConsecutiveUseCount != nil {
			count = *conns[currentIdx].ConsecutiveUseCount
		}
		if count < stickyLimit {
			// Still inside the sticky window: keep serving the same account.
			winner = currentIdx
			consecutive = count + 1
		}
	}

	if winner < 0 {
		// Least recently used; a row that has never served sorts first, and
		// rows stamped in the same pass keep priority order.
		var oldest *string
		for i, c := range conns {
			if c == nil {
				continue
			}
			if c.LastUsedAt == nil {
				winner = i
				oldest = nil
				break
			}
			if oldest == nil || *c.LastUsedAt < *oldest {
				winner = i
				oldest = c.LastUsedAt
			}
		}
		consecutive = 1
	}

	// Fall back to the largest stamp already in the pool rather than a bare
	// clock read: if the write fails, the in-memory row still has to advance
	// past its neighbours, or this same request's rotation would repeat on the
	// next pick.
	highest := ""
	for _, c := range conns {
		if c != nil && c.LastUsedAt != nil && *c.LastUsedAt > highest {
			highest = *c.LastUsedAt
		}
	}
	stamp := db.NextRotationStamp(highest)
	if h.Repo != nil {
		persisted, err := h.Repo.TouchConnectionRotation(conns[winner].ID, consecutive)
		if err != nil {
			log.Warn("connections", "persist round-robin stamp failed", "conn", conns[winner].ID, "error", err)
		} else {
			stamp = persisted
		}
	}
	conns[winner].LastUsedAt = &stamp
	conns[winner].ConsecutiveUseCount = &consecutive

	rotated := make([]*models.ProviderConnection, 0, len(conns))
	rotated = append(rotated, conns[winner])
	for i, c := range conns {
		if i != winner {
			rotated = append(rotated, c)
		}
	}
	return rotated
}
