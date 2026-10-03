package chat

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
	internalproxy "9router/proxy/internal/proxy"
	"9router/proxy/internal/proxy/executor"
	"9router/proxy/internal/tokensaver"
	"9router/proxy/internal/tracing"
	"9router/proxy/internal/translator"
	"9router/proxy/internal/usagetracker"
)

// StatusClientClosedRequest is the canonical HTTP status for client connection aborts (nginx 499).
const StatusClientClosedRequest = 499

// handleAccountFallback attempts to forward a request with automatic account fallback.
func (h *ChatHandler) handleAccountFallback(
	ctx context.Context,
	w http.ResponseWriter,
	provider string,
	model string,
	pinnedConnectionID string,
	body []byte,
	isStream bool,
	translateResponse bool,
	endpoint string,
) error {
	body = repairToolCallIDsInJSON(body)
	if pinnedConnectionID != "" {
		connObj, connData, err := h.getBestConnection(provider, pinnedConnectionID, nil, model)
		if err != nil {
			return fmt.Errorf("pinned connection %s: %w", pinnedConnectionID, err)
		}
		log.Debug("fallback", "pinned", "pinnedConn", pinnedConnectionID, "connObj", connObj.ID)
		return h.tryForwardWithConnection(forwardRequestParams{
			Ctx: ctx, W: w, Provider: provider, Model: model,
			ConnectionID: connObj.ID, ConnName: connObjName(connObj), ConnEmail: connObjEmail(connObj),
			ConnData: connData, Body: body,
			IsStream: isStream, TranslateResponse: translateResponse, Endpoint: endpoint,
		})
	}

	if !h.Repo.IsProviderAvailable(provider, model) {
		log.Warn("fallback", "skip unhealthy", "provider", provider, "model", model)
		return fmt.Errorf("provider %s/%s is unhealthy", provider, model)
	}

	allConns, err := h.Repo.GetProviderConnections(provider, true)
	if err != nil || len(allConns) == 0 {
		if cfg, ok := providers.KnownProviders[provider]; ok && (cfg.NoAuth || cfg.DefaultAPIKey != "") {
			apiKey := cfg.DefaultAPIKey
			if apiKey == "" {
				apiKey = "public"
			}
			proxyPoolID := h.ResolveProviderProxyPoolID(provider)
			return h.tryForwardWithConnection(forwardRequestParams{
				Ctx: ctx, W: w, Provider: provider, Model: model,
				ConnectionID: "default", ConnData: &ConnectionData{APIKey: apiKey, ProxyPoolID: proxyPoolID}, Body: body,
				IsStream: isStream, TranslateResponse: translateResponse, Endpoint: endpoint,
			})
		}
		return fmt.Errorf("no active connections for provider: %s", provider)
	}

	// Apply provider connection routing strategy (round-robin, sticky, random) if configured
	if len(allConns) > 1 && h.Repo != nil {
		if settings, sErr := h.Repo.GetSettings(); sErr == nil && settings != nil {
			strat := db.ProviderStrategy{}
			hasStrat := false
			if settings.ProviderStrategies != nil {
				if s, ok := settings.ProviderStrategies[provider]; ok {
					strat = s
					hasStrat = true
				}
			}
			if !hasStrat || strat.RotateStrategy == "" {
				if settings.FallbackStrategy != "" && settings.FallbackStrategy != "fill-first" {
					strat.RotateStrategy = settings.FallbackStrategy
					strat.StickyLimit = settings.StickyRoundRobinLimit
				}
			}
			if strat.RotateStrategy != "" && strat.RotateStrategy != "none" {
				allConns = h.applyConnectionStrategy(allConns, strat)
			}
		}
	}

	var excludeIDs []string
	var lastErr error
	for _, c := range allConns {
		if slices.Contains(excludeIDs, c.ID) {
			continue
		}
		connObj, connData, err := h.getBestConnection(provider, c.ID, nil, model)
		if err != nil || connObj == nil {
			continue
		}
		apiKey := extractAPIKey(connData)
		if apiKey == "" {
			providerCfg, pErr := h.getProviderConfig(provider, connData)
			if pErr == nil && providerCfg.DefaultAPIKey != "" {
				apiKey = providerCfg.DefaultAPIKey
			} else {
				continue
			}
		}
		log.Debug("fallback", "connection", "conn", c.ID, "connObj", connObj.ID)
		if err := h.tryForwardWithConnection(forwardRequestParams{
			Ctx: ctx, W: w, Provider: provider, Model: model,
			ConnectionID: c.ID, ConnName: connObjName(connObj), ConnEmail: connObjEmail(connObj),
			ConnData: connData, Body: body,
			IsStream: isStream, TranslateResponse: translateResponse, Endpoint: endpoint,
		}); err == nil {
			return nil
		} else {
			lastErr = err
		}
		var ue *upstreamError
		if errors.As(lastErr, &ue) && providers.RetryableStatusCodes[ue.StatusCode] {
			// Extract error text from upstream body for classification
			errorText := extractErrorText(ue.Body)
			// Get current backoff level from this connection
			currentBackoffLevel := h.Repo.GetConnectionBackoffLevel(connObj.ID)
			// Classify error to get dynamic cooldown
			classification := providers.ClassifyError(ue.StatusCode, errorText, currentBackoffLevel)
			cooldownSec := retryableCooldownSec(ue.StatusCode, time.Duration(classification.CooldownMs)*time.Millisecond, ue)
			errMsg := errorText
			if errMsg == "" {
				errMsg = fmt.Sprintf("%d upstream error", ue.StatusCode)
			}
			lockKey := canonicalLockModel(provider, model)
			h.Repo.LockConnectionModel(connObj.ID, lockKey, cooldownSec, classification.NewBackoffLevel)
			if lockKey != model {
				_ = h.Repo.LockConnectionModel(connObj.ID, model, cooldownSec, classification.NewBackoffLevel)
			}
			// Account-scoped cooldown alongside the per-model locks, so the
			// selector can skip this account before spending a request
			// (upstream applyErrorState).
			until := time.Now().UTC().Add(time.Duration(cooldownSec) * time.Second)
			if lockErr := h.Repo.LockConnectionRateLimit(connObj.ID, until, classification.NewBackoffLevel, ue.StatusCode, errorText); lockErr != nil {
				log.Warn("fallback", "rate limit lock failed", "conn", connObj.ID, "error", lockErr)
			}
			log.Warn("fallback", "connection locked", append([]any{
				"conn", connObj.ID, "provider", provider, "model", model,
				"lockKey", lockKey, "status", ue.StatusCode, "cooldown_s", cooldownSec,
			}, connIdentityKV(connObj)...)...)
			excludeIDs = append(excludeIDs, c.ID)
			continue
		}
		return lastErr
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("no available connections for provider: %s", provider)
}

// tryForwardWithConnection attempts a single upstream request using the given connection data.
// isAnthropicUpstream reports whether the request is headed to Anthropic's
// native Messages API (as opposed to an anthropic-compatible custom node).
func isAnthropicUpstream(provider string, cfg *providers.ProviderConfig) bool {
	if provider != "claude" && provider != "anthropic" {
		return false
	}
	if cfg == nil {
		return false
	}
	targetURL := cfg.BaseURL
	if cfg.StaticHeaders != nil {
		if relayTarget, ok := cfg.StaticHeaders["x-relay-target"]; ok && relayTarget != "" {
			targetURL = relayTarget + cfg.StaticHeaders["x-relay-path"]
		}
	}
	return targetURL == "https://api.anthropic.com/v1/messages" ||
		strings.HasPrefix(targetURL, "https://api.anthropic.com/v1/messages?")
}

func appendBetaQuery(u string) string {
	if strings.Contains(u, "beta=true") {
		return u
	}
	if strings.Contains(u, "?") {
		return u + "&beta=true"
	}
	return u + "?beta=true"
}

// forwardRequestParams bundles tryForwardWithConnection inputs. Ten positional
// params (including two adjacent bools) made call sites unreadable and
// extension error-prone; named fields fix the call sites while the function
// body intentionally keeps short local aliases.
type forwardRequestParams struct {
	Ctx               context.Context
	W                 http.ResponseWriter
	Provider          string
	Model             string
	ConnectionID      string
	ConnName          string // human-readable account name, for logs
	ConnEmail         string
	ConnData          *ConnectionData
	Body              []byte
	IsStream          bool
	TranslateResponse bool
	Endpoint          string
}

func (h *ChatHandler) tryForwardWithConnection(f forwardRequestParams) error {
	ctx, w := f.Ctx, f.W
	provider, model := f.Provider, f.Model
	connectionID, connData := f.ConnectionID, f.ConnData
	body, isStream := f.Body, f.IsStream
	translateResponse, endpoint := f.TranslateResponse, f.Endpoint
	ctx = translator.WithUsageCapture(ctx)

	providerCfg, err := h.getProviderConfig(provider, connData)
	if err != nil {
		return fmt.Errorf("get config for %s/%s: %w", provider, model, err)
	}
	// anthropic-compatible nodes fronting a Claude model need Anthropic-Beta flags (parity #3797)
	if (provider == "claude" || strings.HasPrefix(provider, "anthropic-compatible-") || strings.HasPrefix(provider, "anthropic")) && strings.HasPrefix(model, "claude-") {
		if providerCfg.StaticHeaders == nil {
			providerCfg.StaticHeaders = make(map[string]string)
		}
		if _, ok := providerCfg.StaticHeaders["Anthropic-Beta"]; !ok {
			providerCfg.StaticHeaders["Anthropic-Beta"] = "prompt-caching-scope-2026-01-05, context-management-2025-06-27"
		}
	}

	// OAuth connections (e.g. Claude subscription logins) must authenticate
	// with "Authorization: Bearer" against the beta endpoint, matching the
	// Next.js dashboard (open-sse/executors/default.js + registry/claude.js).
	// Sending the OAuth access token via x-api-key makes api.anthropic.com
	// return 401 "API key is invalid" even after a successful token refresh.
	// A connection is OAuth when it only carries an accessToken (no apiKey).
	isAnthropic := isAnthropicUpstream(provider, providerCfg)
	// OAuth = connection carries only an accessToken, OR the token itself is
	// an Anthropic OAuth token (some connections store sk-ant-oat in APIKey).
	isOAuth := isAnthropic && connData != nil &&
		((connData.APIKey == "" && connData.AccessToken != "") ||
			strings.Contains(connData.APIKey, "sk-ant-oat"))
	if isOAuth {
		providerCfg.AuthHeader = "Authorization"
		providerCfg.AuthScheme = "bearer"
		if providerCfg.StaticHeaders != nil && providerCfg.StaticHeaders["x-relay-path"] != "" {
			providerCfg.StaticHeaders["x-relay-path"] = appendBetaQuery(providerCfg.StaticHeaders["x-relay-path"])
		} else {
			providerCfg.BaseURL = appendBetaQuery(providerCfg.BaseURL)
		}
	}

	apiKey := resolveProviderAuthToken(provider, connData, extractAPIKey(connData))
	if apiKey == "" {
		if providerCfg.DefaultAPIKey != "" {
			apiKey = providerCfg.DefaultAPIKey
		} else {
			return &upstreamError{StatusCode: http.StatusUnauthorized, Body: []byte(`{"error":{"message":"no API key found","type":"auth_error","code":401}}`)}
		}
	}

	if connectionID != "" {
		rekey, _, err := h.refreshOAuthTokenIfExpired(connectionID, apiKey)
		if err == nil {
			apiKey = rekey
		} else {
			log.Warn("fallback", "OAuth token refresh error", "conn", connectionID, "error", err)
		}
	}
	apiKey = NormalizeProviderToken(provider, apiKey)

	// Token savers + provider-format normalization:
	// - /v1/messages (claudeNative): body stays Claude format; savers inject
	//   into top-level "system".
	// - /v1/chat/completions to an Anthropic upstream: the body is OpenAI
	//   format and would otherwise be forwarded raw; convert it to a
	//   spec-compliant Claude Messages payload (top-level system, tools,
	//   merged roles) — same conversion the dashboard applies server-side.
	// claudeNative indicates the CLIENT body is in Claude Messages format.
	// Requires an Anthropic upstream too: chat.go converts /v1/messages
	// requests for non-Anthropic providers (DeepSeek, OpenAI-compatible) to
	// OpenAI format before the fallback, and passing endpoint "/v1/v1/messages"
	// alone would wrongly inject a top-level "system" the upstream ignores.
	claudeNative := isAnthropic && (endpoint == "/v1/v1/messages" || endpoint == "/v1/messages")
	// A /v1/messages client is only converted away from Claude format when the
	// upstream cannot answer in it. opencode-zen routes the Claude and Qwen
	// models to its own /zen/v1/messages endpoint, so those requests keep the
	// client's own wire format end to end (upstream resolveTransport picks the
	// sourceFormat-matched transport and skips translation).
	if endpoint == "/v1/v1/messages" || endpoint == "/v1/messages" {
		if executor.ServesMessagesEndpoint(provider, model) {
			claudeNative = true
		}
	}
	pipedBody, origTokens, savedTokens, savedPct := h.applyTokenSavers(body, claudeNative)
	var claudeToolMap map[string]string
	if isAnthropic {
		if !claudeNative {
			// Raw OpenAI-format body would be invalid at the Messages API:
			// convert to a spec-compliant Claude payload (top-level system,
			// tools, merged roles) — what the dashboard does server-side.
			pipedBody = executor.EnsureClaudeMessages(pipedBody, model)
		}
		// The OpenAI→Claude conversion merges turns, so a client body whose
		// last message was a tool result can still land on an assistant turn.
		// A prefill is the client's own choice, so it is detected on the
		// pre-conversion body (upstream ensureTrailingUserTurn, claude.js:345-349).
		prefill := translator.ClaudeIntentionalPrefill(body)
		pipedBody = translator.EnsureTrailingUserTurnBody(pipedBody, prefill)

		// OAuth connections (or sk-ant-oat tokens) require Claude-Code-shaped requests:
		// billing-header system block + metadata.user_id + cloaked tools,
		// or the API 429s (anti-abuse fingerprinting).
		if isOAuth || strings.Contains(apiKey, "sk-ant-oat") {
			pipedBody = applyClaudeCloaking(pipedBody, apiKey, handlerutil.GetSessionID(ctx))
			var reqMap map[string]any
			if err := json.Unmarshal(pipedBody, &reqMap); err == nil {
				claudeToolMap = cloakClaudeTools(reqMap)
				if out, err := json.Marshal(reqMap); err == nil {
					pipedBody = out
				}
			}
		}
	}

	// A body that asked for thinking summaries must not also carry the
	// redact-thinking beta: it asks Anthropic for signature-only thinking blocks
	// and would blank exactly what the client requested. The header map is
	// shared with the registry, so this edits a request-local copy.
	if isAnthropic && executor.WantsThinkingSummaries(pipedBody) {
		providerCfg.StaticHeaders = providers.WithoutBetaFlag(
			providerCfg.StaticHeaders, providers.AnthropicBetaRedactThinking,
		)
	}

	if isAnthropic {
		// The caller's own beta flags are merged into the upstream request
		// instead of being dropped: a client asking for a beta the gateway does
		// not list would be refused without ever being told why. The registry
		// header map is shared, so this edits a request-local copy.
		if clientBeta := handlerutil.GetClientAnthropicBeta(ctx); clientBeta != "" {
			merged := providers.MergeAnthropicBeta(providerCfg.StaticHeaders["Anthropic-Beta"], clientBeta)
			if merged != "" {
				providerCfg.StaticHeaders = providers.WithHeader(providerCfg.StaticHeaders, "Anthropic-Beta", merged)
			}
		}

		// Claude OAuth wants x-claude-code-session-id to agree with the
		// metadata.user_id the cloak generates, otherwise the API sees two
		// different sessions for one request.
		if isOAuth || strings.Contains(apiKey, "sk-ant-oat") {
			if sid := claudeSessionIDFromBody(pipedBody); sid != "" {
				if providerCfg.StaticHeaders["x-claude-code-session-id"] == "" {
					providerCfg.StaticHeaders = providers.WithHeader(
						providerCfg.StaticHeaders, "x-claude-code-session-id", sid,
					)
				}
			}
		}
	}

	// Sanitize tool schemas for all OpenAI-compatible providers (opencode, gemini-openai, etc.)
	// Fixes misplaced `required` inside `properties` and missing `items` for arrays.
	if sanitized, err := translator.SanitizeOpenAITools(pipedBody); err == nil && sanitized != nil && string(sanitized) != string(pipedBody) {
		log.Debug("fallback", "sanitized tools", "provider", provider, "model", model, "conn", connectionID[:min(8, len(connectionID))], "beforeBytes", len(pipedBody), "afterBytes", len(sanitized))
		pipedBody = sanitized
	} else if err != nil {
		log.Warn("fallback", "sanitize failed", "provider", provider, "model", model, "error", err)
	}
	// DeepSeek answers 400 "Tool names must be unique" for a request that
	// declares the same tool twice, so same-name definitions are collapsed at
	// the last point before dispatch — after every conversion (OpenAI →
	// Claude for an Anthropic upstream included), like upstream
	// dedupeTools in open-sse/handlers/chatCore.js. Scoped by model id, so no
	// other provider's tool array is touched.
	if deduped := translator.DedupeToolsDeepSeek(pipedBody, model); len(deduped) != len(pipedBody) {
		log.Debug("fallback", "deduped duplicate tool names", "provider", provider, "model", model)
		pipedBody = deduped
	}

	// Fit tool names exceeding MaxToolNameLength (64 chars) to prevent upstream HTTP 400.
	fittedBody, fittedToolMap := translator.FitToolNames(pipedBody)
	if len(fittedToolMap) > 0 {
		log.Debug("fallback", "fitted long tool names", "provider", provider, "model", model, "count", len(fittedToolMap))
		pipedBody = fittedBody
		w = executor.NewToolNameRestoringWriter(w, fittedToolMap)
		ctx = translator.WithToolNameMap(ctx, fittedToolMap)
		if claudeToolMap == nil {
			claudeToolMap = make(map[string]string, len(fittedToolMap))
		}
		for k, v := range fittedToolMap {
			claudeToolMap[k] = v
		}
	}
	start := time.Now()
	metrics := &streamMetrics{}
	var fwdErr error

	usagetracker.GetTracker().TrackPending(model, provider, connectionID, true, false)
	defer func() {
		hasErr := fwdErr != nil
		usagetracker.GetTracker().TrackPending(model, provider, connectionID, false, hasErr)
	}()

	// A connection bound to a pool that cannot serve traffic must fail before
	// anything is sent: the first request is the one that would otherwise
	// escape from the real IP while the error only shows on the next attempt.
	httpClient, clientErr := h.getClientForConnection(connData)
	if clientErr != nil {
		return &upstreamError{
			StatusCode: http.StatusBadGateway,
			Body:       []byte(`{"error":{"type":"proxy_error","message":"` + clientErr.Error() + `"}}`),
		}
	}
	// The client carries the strict-proxy marker itself, but the executors'
	// DoRequest path sees only ctx, so the decision travels on both. A
	// connection with nothing proxied at all is left unmarked: strict there
	// means "never replay this request directly", not "a proxy must exist".
	if connData != nil && (connData.ProxyPoolID != "" || connData.ConnectionProxyEnabled || connData.StrictProxy) {
		ctx = internalproxy.WithStrictProxy(ctx, true)
	}
	sessionID := handlerutil.GetSessionID(ctx)

	if exec := executor.Get(provider); exec != nil {
		execReq := &executor.Request{
			Ctx:            ctx,
			Client:         httpClient,
			Config:         providerCfg,
			APIKey:         apiKey,
			Body:           pipedBody,
			IsStream:       isStream,
			ClaudeClient:   endpoint == "/v1/v1/messages" || endpoint == "/v1/messages",
			TranslateResp:  translateResponse,
			ConnectionID:   connectionID,
			SessionID:      sessionID,
			ToolNameMap:    claudeToolMap,
			UpstreamClaude: isAnthropic && !claudeNative,
			ResponseBuf:    &metrics.ResponseBuf,
			StartTime:      start,
			TTFT:           &metrics.TTFT,
		}
		// client_id cloaking: hand the connection's providerSpecificData
		// (fingerprintId) to executors that mimic an official client.
		if connData != nil && len(connData.ProviderSpecificData) > 0 {
			execReq.ConnData = connData.ProviderSpecificData
		}
		// Cross-process session coordination (e.g. Freebuff leases): nil
		// when no repo (tests), so executors fall back to memory only.
		if h.Repo != nil {
			execReq.Leases = h.Repo
		}
		fwdErr = exec(w, execReq)
	} else if providerCfg.IsGeminiNative() {
		fwdErr = h.forwardGeminiNativeRequest(ctx, w, provider, providerCfg, apiKey, connectionID, pipedBody, isStream, translateResponse, metrics, httpClient)
	} else {
		fwdErr = h.forwardRequest(ctx, w, providerCfg, apiKey, pipedBody, isStream, translateResponse, metrics, httpClient)
	}

	var ue *upstreamError
	if errors.As(fwdErr, &ue) && ue.StatusCode == http.StatusUnauthorized && connectionID != "" {
		refreshedKey, _, rErr := h.forceRefreshOAuthToken(connectionID)
		if rErr == nil && refreshedKey != "" && refreshedKey != apiKey {
			log.Info("fallback", "reactive 401 token refresh success, retrying request", "conn", connectionID)
			apiKey = NormalizeProviderToken(provider, refreshedKey)
			if exec := executor.Get(provider); exec != nil {
				retryReq := &executor.Request{
					Ctx:            ctx,
					Client:         httpClient,
					Config:         providerCfg,
					APIKey:         apiKey,
					Body:           pipedBody,
					IsStream:       isStream,
					ClaudeClient:   endpoint == "/v1/v1/messages" || endpoint == "/v1/messages",
					TranslateResp:  translateResponse,
					ConnectionID:   connectionID,
					SessionID:      sessionID,
					ToolNameMap:    claudeToolMap,
					UpstreamClaude: isAnthropic && !claudeNative,
					ResponseBuf:    &metrics.ResponseBuf,
					StartTime:      start,
					TTFT:           &metrics.TTFT,
				}
				// Same client_id cloaking as the first attempt: a refreshed
				// retry without ConnData would fall back to a random id and
				// re-brand the request mid-conversation.
				if connData != nil && len(connData.ProviderSpecificData) > 0 {
					retryReq.ConnData = connData.ProviderSpecificData
				}
				if h.Repo != nil {
					retryReq.Leases = h.Repo
				}
				fwdErr = exec(w, retryReq)
			} else if providerCfg.IsGeminiNative() {
				fwdErr = h.forwardGeminiNativeRequest(ctx, w, provider, providerCfg, apiKey, connectionID, pipedBody, isStream, translateResponse, metrics, httpClient)
			} else {
				fwdErr = h.forwardRequest(ctx, w, providerCfg, apiKey, pipedBody, isStream, translateResponse, metrics, httpClient)
			}
		}
	}

	// Codex answers an exhausted 5h/7d window with a 429 carrying
	// usage_limit_reached and the exact reset. Caching it lets the next
	// picker pass skip this account instead of spending a request to
	// rediscover the same 429 (mirrors the Antigravity quota cache).
	//
	// When the body carries no reset, read live quota instead — throttled to
	// one wham call per 30s per connection. That only blocks the account if
	// the reading is actually exhausted, so a per-request or burst 429 on an
	// otherwise healthy account caches a healthy reading and blocks nothing.
	if errors.As(fwdErr, &ue) && provider == "codex" && connectionID != "" && ue.StatusCode == http.StatusTooManyRequests {
		if resetAt := NoteCodexQuotaError(connectionID, ue.StatusCode, ue.Body); resetAt != nil {
			log.Info("fallback", "codex quota exhausted, cached until reset", "conn", shortConnID(connectionID), "resetAt", resetAt.Format(time.RFC3339))
		} else if _, qErr := RefreshCodexQuota(ctx, httpClient, connectionID, apiKey); qErr != nil {
			log.Debug("fallback", "codex quota refresh failed", "conn", shortConnID(connectionID), "error", qErr)
		}
	}

	latencyMs := time.Since(start).Milliseconds()

	// Lightweight request trace for /debug/traces (provider/model latency).
	completed := fwdErr == nil
	if !completed && isClientCanceled(ctx, fwdErr) && metrics != nil && metrics.ResponseBuf.Len() > 0 {
		completed = true
	}

	status := "error"
	if completed {
		status = strconv.Itoa(http.StatusOK)
	} else if isClientCanceled(ctx, fwdErr) {
		status = strconv.Itoa(StatusClientClosedRequest)
	} else if ue, ok := fwdErr.(*upstreamError); ok && ue.StatusCode > 0 {
		status = strconv.Itoa(ue.StatusCode)
	}
	tracing.Record(tracing.Span{
		Provider:   provider,
		Model:      model,
		Status:     status,
		DurationMs: latencyMs,
		TTFTMs:     metrics.TTFT,
	})

	usage := translator.GetAndClearUsage(ctx)
	if completed {
		// Clear any existing model lock on success (matching Next.js clearAccountError).
		lockKey := canonicalLockModel(provider, model)
		if unlockErr := h.Repo.UnlockConnectionModel(connectionID, lockKey); unlockErr != nil {
			log.Warn("fallback", "unlock failed", "provider", provider, "model", lockKey, "error", unlockErr)
		}
		if lockKey != model {
			_ = h.Repo.UnlockConnectionModel(connectionID, model)
		}
		// A served request also clears the account-scoped cooldown, so an
		// account that recovered is not kept out of rotation until the
		// cooldown expires on its own.
		if clearErr := h.Repo.ClearConnectionRateLimit(connectionID); clearErr != nil {
			log.Warn("fallback", "rate limit clear failed", "conn", connectionID, "error", clearErr)
		}
		// A served request proves the account is usable again, so drop any
		// cached quota block rather than leaving it to expire on its own.
		if provider == "codex" {
			ClearCodexQuotaBlock(connectionID)
		}
		if usage == nil {
			usage = &translator.OpenAIUsage{}
		}
		logInfo := &UsageLogInfo{
			Provider:            provider,
			Model:               model,
			ConnectionID:        connectionID,
			APIKey:              apiKey,
			Endpoint:            endpoint,
			Egress:              resolveEgress(connData, providerCfg).LogValue(),
			OriginalInputTokens: origTokens,
			SavedTokens:         savedTokens,
			SavedPercent:        savedPct,
		}
		logInfo.ConnName, logInfo.ConnEmail = identityNames(h.connIdentityKVOr(f, connectionID))
		h.logUsage(logInfo, usage, latencyMs, body, metrics)
		fwdErr = nil
		return nil
	}

	// Consume any terminal usage even when streaming aborts, then persist one
	// failure row. A later fallback attempt gets its own usage holder because
	// the outer handler installs capture once and tryForward never replaces it.
	statusCode := 0
	if errors.As(fwdErr, &ue) {
		statusCode = ue.StatusCode
	}
	identity := h.connIdentityKVOr(f, connectionID)
	// Every failure line below carries the egress: a 401/429 from a provider
	// that rate-limits by IP is only diagnosable if the log says whether the
	// request actually left through the pool.
	identity = append(identity, "egress", resolveEgress(connData, providerCfg).LogValue())
	connName, connEmail := identityNames(identity)
	h.LogFailure(
		&UsageLogInfo{
			Provider:            provider,
			Model:               model,
			ConnectionID:        connectionID,
			ConnName:            connName,
			ConnEmail:           connEmail,
			Endpoint:            endpoint,
			Egress:              resolveEgress(connData, providerCfg).LogValue(),
			OriginalInputTokens: origTokens,
			SavedTokens:         savedTokens,
			SavedPercent:        savedPct,
		},
		usage,
		fwdErr,
		latencyMs,
		body,
		metrics,
	)
	if isClientCanceled(ctx, fwdErr) {
		log.Info("fallback", "client canceled request", append([]any{
			"provider", provider, "model", model,
		}, identity...)...)
	} else if projectProbeCached(connectionID) {
		log.Debug("fallback", "upstream skipped (cached no-project)", append([]any{
			"provider", provider, "model", model, "error", fwdErr,
		}, identity...)...)
	} else {
		log.Warn("fallback", "upstream failed", append([]any{
			"provider", provider, "model", model,
			"status", statusCode, "error", fwdErr,
		}, identity...)...)
	}
	return fwdErr
}
func isClientCanceled(ctx context.Context, err error) bool {
	if ctx != nil && ctx.Err() != nil {
		return true
	}
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	errStr := err.Error()
	return strings.Contains(errStr, "context canceled") || strings.Contains(errStr, "client closed")
}

// applyTokenSavers runs RTK compression and prompt injection on the request body.
// claudeNative indicates the body is in Claude Messages format (endpoint
// /v1/messages): system prompts must go to the top-level "system" field —
// a role:"system" message is rejected by the Anthropic API.
// false from compress/inject means nothing changed (or unparseable) — keep original, not a failure.
func (h *ChatHandler) applyTokenSavers(body []byte, claudeNative bool) ([]byte, int, int, int) {
	// Prompt-injection guard: tag (never block) flagged user content. Early
	// detection here means operators can see abuse before it reaches upstream.
	// Toggle via settings.injectionGuardEnabled (off bypasses the scan).
	if h.TokenSaver.InjectionGuardEnabled() {
		if inj := tokensaver.DetectInjection(body); inj.Flagged {
			log.Warn("guard", "prompt injection flagged", "reasons", inj.Reasons, "messageId", inj.MessageID)
		}
	}
	out := body
	var origTokens, savedTokens, savedPct int
	if h.TokenSaver.RTKEnabled() {
		if next, did := tokensaver.CompressMessages(out); did {
			origTokens = len(out) / 4
			compressedTokens := len(next) / 4
			savedTokens = origTokens - compressedTokens
			savedPct = 0
			if origTokens > 0 {
				savedPct = (savedTokens * 100) / origTokens
			}
			log.Info("token_saver", "RTK compressed tool output",
				"orig_est", origTokens,
				"compressed_est", compressedTokens,
				"saved_est", savedTokens,
				"saved_pct", fmt.Sprintf("%d%%", savedPct),
			)
			out = next
		}
	}
	inject := tokensaver.InjectSystemPrompt
	if claudeNative {
		inject = tokensaver.InjectSystemPromptClaude
	}
	if h.TokenSaver.CavemanEnabled() {
		prompt := tokensaver.GetCavemanPrompt(h.TokenSaver.CavemanLevel())
		if next, did := inject(out, prompt); did {
			out = next
		}
	}
	if h.TokenSaver.PonytailEnabled() {
		prompt := tokensaver.GetPonytailPrompt(h.TokenSaver.PonytailLevel())
		if next, did := inject(out, prompt); did {
			out = next
		}
	}
	if h.TokenSaver.ADHDEnabled() {
		prompt := tokensaver.GetADHDPrompt(h.TokenSaver.ADHDLevel())
		if next, did := inject(out, prompt); did {
			out = next
		}
	}
	return out, origTokens, savedTokens, savedPct
}

// extractErrorText attempts to extract a human-readable error message from an upstream error JSON body.
// Returns "" when the body isn't parseable or has no message field.
func extractErrorText(body []byte) string {
	var parsed struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
			Details []struct {
				Reason string `json:"reason"`
			} `json:"details"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil {
		var parts []string
		if parsed.Error.Message != "" {
			parts = append(parts, parsed.Error.Message)
		} else if parsed.Message != "" {
			parts = append(parts, parsed.Message)
		}
		if parsed.Error.Status != "" {
			parts = append(parts, parsed.Error.Status)
		}
		for _, d := range parsed.Error.Details {
			if d.Reason != "" {
				parts = append(parts, d.Reason)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, " ")
		}
	}
	trimmed := bytes.TrimSpace(body)
	if bytes.HasPrefix(trimmed, []byte("<!DOCTYPE html")) || bytes.HasPrefix(trimmed, []byte("<html")) {
		lower := strings.ToLower(string(trimmed))
		if strings.Contains(lower, "cloudflare") || strings.Contains(lower, "attention required") {
			return "Cloudflare WAF challenge (Attention Required!): check User-Agent or network proxy"
		}
		if titleStart := strings.Index(lower, "<title>"); titleStart != -1 {
			titleEnd := strings.Index(lower[titleStart:], "</title>")
			if titleEnd != -1 {
				return "upstream returned HTML: " + strings.TrimSpace(string(trimmed[titleStart+7:titleStart+titleEnd]))
			}
		}
		return "upstream returned HTML error page"
	}
	return ""
}

var resetsInRegex = regexp.MustCompile(`(?i)resets?\s+in\s+([0-9hms\.]+)`)

const (
	minResetCooldown = 5 * time.Second
	maxResetCooldown = 2 * time.Hour
)

// extractResetDuration attempts to extract a structured reset duration from an error payload.
// It parses:
// 1. Google RPC ErrorInfo metadata: quotaResetDelay ("1h12m28.109534319s")
// 2. Text message patterns: "Resets in 1h12m28s."
// 3. Clamps duration between 5 seconds and 2 hours to prevent deadlock / indefinite lockout.
func extractResetDuration(body []byte) (time.Duration, bool) {
	if len(body) == 0 {
		return 0, false
	}

	// 1. Google RPC error details
	var rpcErr struct {
		Error struct {
			Message string `json:"message"`
			Details []struct {
				Metadata map[string]string `json:"metadata"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &rpcErr); err == nil {
		for _, d := range rpcErr.Error.Details {
			if delayStr, ok := d.Metadata["quotaResetDelay"]; ok && delayStr != "" {
				delayStr = strings.TrimRight(delayStr, ".")
				if dur, err := time.ParseDuration(delayStr); err == nil && dur > 0 {
					return clampResetDuration(dur), true
				}
			}
		}
		if rpcErr.Error.Message != "" {
			if matches := resetsInRegex.FindStringSubmatch(rpcErr.Error.Message); len(matches) > 1 {
				raw := strings.TrimRight(matches[1], ".")
				if dur, err := time.ParseDuration(raw); err == nil && dur > 0 {
					return clampResetDuration(dur), true
				}
			}
		}
	}

	// 2. Fallback regex on raw body string
	if matches := resetsInRegex.FindSubmatch(body); len(matches) > 1 {
		raw := strings.TrimRight(string(matches[1]), ".")
		if dur, err := time.ParseDuration(raw); err == nil && dur > 0 {
			return clampResetDuration(dur), true
		}
	}

	return 0, false
}

func clampResetDuration(dur time.Duration) time.Duration {
	if dur < minResetCooldown {
		return minResetCooldown
	}
	if dur > maxResetCooldown {
		return maxResetCooldown
	}
	return dur
}

// extractRetryAfter extracts a retryAfter ISO timestamp from an upstream error JSON body.
// Checks quotaResetDelay, "Resets in X", or common field names: retryAfter, retry_after, resetsAt, resets_at.
// Returns "" when not found or not parseable.
func extractRetryAfter(body []byte) string {
	if dur, ok := extractResetDuration(body); ok {
		return time.Now().UTC().Add(dur).Format(time.RFC3339)
	}
	var parsed struct {
		RetryAfter string `json:"retryAfter"`
		RetryAlt   string `json:"retry_after"`
		ResetsAt   string `json:"resetsAt"`
		ResetsAlt  string `json:"resets_at"`
		Error      struct {
			RetryAfter string `json:"retryAfter"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ""
	}
	if parsed.RetryAfter != "" {
		return parsed.RetryAfter
	}
	if parsed.RetryAlt != "" {
		return parsed.RetryAlt
	}
	if parsed.ResetsAt != "" {
		return parsed.ResetsAt
	}
	if parsed.ResetsAlt != "" {
		return parsed.ResetsAlt
	}
	if parsed.Error.RetryAfter != "" {
		return parsed.Error.RetryAfter
	}
	return ""
}

// formatRetryAfter formats an ISO timestamp into a human-readable "reset after Xm Ys" string.
// Returns "" when the timestamp is empty, unparseable, or in the past.
func formatRetryAfter(isoTimestamp string) string {
	if isoTimestamp == "" {
		return ""
	}
	parsed, err := time.Parse(time.RFC3339, isoTimestamp)
	if err != nil {
		return ""
	}
	// time.Until is a Duration in nanoseconds. Dividing by 1000 yielded
	// milliseconds, so a 150s cooldown read as "reset after 41399h" and
	// contradicted the Retry-After header sent alongside it.
	remaining := time.Until(parsed)
	if remaining <= 0 {
		return "reset after 0s"
	}
	totalSec := int((remaining + time.Second - 1) / time.Second) // ceil
	h := totalSec / 3600
	m := (totalSec % 3600) / 60
	s := totalSec % 60
	var parts []string
	if h > 0 {
		parts = append(parts, fmt.Sprintf("%dh", h))
	}
	if m > 0 {
		parts = append(parts, fmt.Sprintf("%dm", m))
	}
	if s > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%ds", s))
	}
	return "reset after " + strings.Join(parts, " ")
}

// claudeSessionIDFromBody reads the session id the cloak wrote into
// metadata.user_id, so it can be echoed in x-claude-code-session-id.
func claudeSessionIDFromBody(body []byte) string {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return ""
	}
	meta, _ := req["metadata"].(map[string]any)
	if meta == nil {
		return ""
	}
	userID, _ := meta["user_id"].(string)
	return extractClaudeSessionIdFromUserId(userID)
}
