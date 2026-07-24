package chat

import (
	"encoding/json"
	"errors"
	"context"
	"fmt"
	"9router/proxy/internal/log"
	"net/http"
	"slices"
	"strings"
	"time"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy/executor"
	"9router/proxy/internal/tokensaver"
	"9router/proxy/internal/translator"
)

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
	if pinnedConnectionID != "" {
		connObj, connData, err := h.getBestConnection(provider, pinnedConnectionID, nil, model)
		if err != nil {
			return fmt.Errorf("pinned connection %s: %w", pinnedConnectionID, err)
		}
		log.Debug("fallback", "pinned", "pinnedConn", pinnedConnectionID, "connObj", connObj.ID)
		return h.tryForwardWithConnection(ctx, w, provider, model, connObj.ID, connData, body, isStream, translateResponse, endpoint)
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
			return h.tryForwardWithConnection(ctx, w, provider, model, "default", &ConnectionData{APIKey: apiKey}, body, isStream, translateResponse, endpoint)
		}
		return fmt.Errorf("no active connections for provider: %s", provider)
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
		if err := h.tryForwardWithConnection(ctx, w, provider, model, c.ID, connData, body, isStream, translateResponse, endpoint); err == nil {
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
			cooldownSec := int((classification.CooldownMs + 999) / 1000) // ceil to seconds
			errMsg := errorText
			if errMsg == "" {
				errMsg = fmt.Sprintf("%d upstream error", ue.StatusCode)
			}
			h.Repo.LockConnectionModel(connObj.ID, model, cooldownSec, classification.NewBackoffLevel)
			log.Warn("fallback", "connection locked", "conn", connObj.ID, "provider", provider, "model", model, "status", ue.StatusCode, "cooldown_s", cooldownSec)
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
func (h *ChatHandler) tryForwardWithConnection(
	ctx context.Context,
	w http.ResponseWriter,
	provider string,
	model string,
	connectionID string,
	connData *ConnectionData,
	body []byte,
	isStream bool,
	translateResponse bool,
	endpoint string,
) error {
	ctx = translator.WithUsageCapture(ctx)

	providerCfg, err := h.getProviderConfig(provider, connData)
	if err != nil {
		return fmt.Errorf("get config for %s/%s: %w", provider, model, err)
	}

	apiKey := extractAPIKey(connData)
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

	pipedBody := h.applyTokenSavers(body)
	start := time.Now()
	metrics := &streamMetrics{}
	var fwdErr error

	httpClient := h.getClientForConnection(connData)

	if exec := executor.Get(provider); exec != nil {
		fwdErr = exec(w, &executor.Request{
			Ctx:           ctx,
			Client:        httpClient,
			Config:        providerCfg,
			APIKey:        apiKey,
			Body:          pipedBody,
			IsStream:      isStream,
			TranslateResp: translateResponse,
			ResponseBuf:   &metrics.ResponseBuf,
			StartTime:     start,
			TTFT:          &metrics.TTFT,
		})
	} else if providerCfg.IsGeminiNative() {
		fwdErr = h.forwardGeminiNativeRequest(ctx, w, provider, providerCfg, apiKey, connectionID, pipedBody, isStream, translateResponse, metrics)
	} else {
		fwdErr = h.forwardRequest(ctx, w, providerCfg, apiKey, pipedBody, isStream, translateResponse, metrics)
	}

	var ue *upstreamError
	if errors.As(fwdErr, &ue) && ue.StatusCode == http.StatusUnauthorized && connectionID != "" {
		refreshedKey, _, rErr := h.forceRefreshOAuthToken(connectionID)
		if rErr == nil && refreshedKey != "" && refreshedKey != apiKey {
			log.Info("fallback", "reactive 401 token refresh success, retrying request", "conn", connectionID)
			apiKey = refreshedKey
			if exec := executor.Get(provider); exec != nil {
				fwdErr = exec(w, &executor.Request{
					Ctx:           ctx,
					Client:        httpClient,
					Config:        providerCfg,
					APIKey:        apiKey,
					Body:          pipedBody,
					IsStream:      isStream,
					TranslateResp: translateResponse,
					ResponseBuf:   &metrics.ResponseBuf,
					StartTime:     start,
					TTFT:          &metrics.TTFT,
				})
			} else if providerCfg.IsGeminiNative() {
				fwdErr = h.forwardGeminiNativeRequest(ctx, w, provider, providerCfg, apiKey, connectionID, pipedBody, isStream, translateResponse, metrics)
			} else {
				fwdErr = h.forwardRequest(ctx, w, providerCfg, apiKey, pipedBody, isStream, translateResponse, metrics)
			}
		}
	}

	latencyMs := time.Since(start).Milliseconds()
	if fwdErr == nil {
		// Clear any existing model lock on success (matching Next.js clearAccountError)
		if unlockErr := h.Repo.UnlockConnectionModel(connectionID, model); unlockErr != nil {
			log.Warn("fallback", "unlock failed", "provider", provider, "model", model, "error", unlockErr)
		}
		usage := translator.GetAndClearUsage(ctx)
		if usage == nil {
			usage = &translator.OpenAIUsage{}
		}
		logInfo := &UsageLogInfo{
			Provider:     provider,
			Model:        model,
			ConnectionID: connectionID,
			APIKey:       apiKey,
			Endpoint:     endpoint,
		}
		h.logUsage(logInfo, usage, latencyMs, body, metrics)
	} else {
		var ue *upstreamError
		statusCode := 0
		if errors.As(fwdErr, &ue) {
			statusCode = ue.StatusCode
		}
		log.Warn("fallback", "upstream failed", "provider", provider, "model", model, "conn", connectionID, "status", statusCode, "error", fwdErr)
	}
	return fwdErr
}

// applyTokenSavers runs RTK compression and prompt injection on the request body.
// false from compress/inject means nothing changed (or unparseable) — keep original, not a failure.
func (h *ChatHandler) applyTokenSavers(body []byte) []byte {
	out := body
	if h.TokenSaver.RTKEnabled() {
		if next, did := tokensaver.CompressMessages(out); did {
			out = next
		}
	}
	if h.TokenSaver.CavemanEnabled() {
		prompt := tokensaver.GetCavemanPrompt(h.TokenSaver.CavemanLevel())
		if next, did := tokensaver.InjectSystemPrompt(out, prompt); did {
			out = next
		}
	}
	if h.TokenSaver.PonytailEnabled() {
		prompt := tokensaver.GetPonytailPrompt(h.TokenSaver.PonytailLevel())
		if next, did := tokensaver.InjectSystemPrompt(out, prompt); did {
			out = next
		}
	}
	return out
}

// extractErrorText attempts to extract a human-readable error message from an upstream error JSON body.
// Returns "" when the body isn't parseable or has no message field.
func extractErrorText(body []byte) string {
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil && parsed.Error.Message != "" {
		return parsed.Error.Message
	}
	return ""
}

// extractRetryAfter extracts a retryAfter ISO timestamp from an upstream error JSON body.
// Checks common field names: retryAfter, retry_after, resetsAt, resets_at.
// Returns "" when not found or not parseable.
func extractRetryAfter(body []byte) string {
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
	diffMs := time.Until(parsed)
	if diffMs <= 0 {
		return "reset after 0s"
	}
	totalSec := int((diffMs + 999) / 1000) // ceil
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
