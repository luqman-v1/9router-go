package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy/executor"
	"9router/proxy/internal/tokensaver"
	"9router/proxy/internal/translator"
)

// handleAccountFallback attempts to forward a request with automatic account fallback.
func (h *ChatHandler) handleAccountFallback(
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
		log.Printf("[debug] fallback pinned: pinnedConnectionID=%q, connObj.ID=%q", pinnedConnectionID, connObj.ID)
		return h.tryForwardWithConnection(w, provider, model, connObj.ID, connData, body, isStream, translateResponse, endpoint)
	}

	if !db.IsProviderHealthy(h.Repo.RawDB(), provider, model) {
		log.Printf("[health] provider %s/%s is unhealthy (consecutive errors >= 5), skipping", provider, model)
		return fmt.Errorf("provider %s/%s is unhealthy", provider, model)
	}

	locked, _ := h.Repo.IsModelLocked(provider, model)
	allConns, err := h.Repo.GetProviderConnections(provider, true)
	if err != nil || len(allConns) == 0 {
		if cfg, ok := providers.KnownProviders[provider]; ok && (cfg.NoAuth || cfg.DefaultAPIKey != "") {
			apiKey := cfg.DefaultAPIKey
			if apiKey == "" {
				apiKey = "public"
			}
			return h.tryForwardWithConnection(w, provider, model, "default", &ConnectionData{APIKey: apiKey}, body, isStream, translateResponse, endpoint)
		}
		return fmt.Errorf("no active connections for provider: %s", provider)
	}

	var excludeIDs []string
	var lastErr error
	for _, conn := range allConns {
		if slices.Contains(excludeIDs, conn.ID) {
			continue
		}
		connObj, connData, err := h.getBestConnection(provider, conn.ID, nil, model)
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
		_ = locked // available for future use (model lock check)
		log.Printf("[debug] fallback loop: conn.ID=%q, connObj.ID=%q", conn.ID, connObj.ID)
		lastErr = h.tryForwardWithConnection(w, provider, model, connObj.ID, connData, body, isStream, translateResponse, endpoint)
		if lastErr == nil {
			return nil
		}
		var ue *upstreamError
		if errors.As(lastErr, &ue) && providers.RetryableStatusCodes[ue.StatusCode] {
			// Extract error text from upstream body for classification
			errorText := extractErrorText(ue.Body)
			// Get current backoff level from existing lock
			currentBackoffLevel := 0
			if existingLock, _ := h.Repo.GetModelLock(provider, model); existingLock != nil {
				currentBackoffLevel = existingLock.BackoffLevel
			}
			// Classify error to get dynamic cooldown
			classification := providers.ClassifyError(ue.StatusCode, errorText, currentBackoffLevel)
			cooldownSec := int((classification.CooldownMs + 999) / 1000) // ceil to seconds
			errMsg := errorText
			if errMsg == "" {
				errMsg = fmt.Sprintf("%d upstream error", ue.StatusCode)
			}
			h.Repo.LockModel(provider, model, cooldownSec, errMsg, ue.StatusCode, classification.NewBackoffLevel)
			excludeIDs = append(excludeIDs, conn.ID)
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
			log.Printf("[oauth] token refresh error for connection %s: %v (using existing token)", connectionID, err)
		}
	}

	pipedBody := h.applyTokenSavers(body)
	start := time.Now()
	metrics := &streamMetrics{}
	var fwdErr error

	if exec := executor.Get(provider); exec != nil {
		fwdErr = exec(w, &executor.Request{
			Client:         h.Client,
			Config:         providerCfg,
			APIKey:         apiKey,
			Body:           pipedBody,
			IsStream:       isStream,
			TranslateResp:  translateResponse,
		})
	} else if providerCfg.IsGeminiNative() {
		fwdErr = h.forwardGeminiNativeRequest(w, provider, providerCfg, apiKey, connectionID, pipedBody, isStream, translateResponse, metrics)
	} else {
		fwdErr = h.forwardRequest(w, providerCfg, apiKey, pipedBody, isStream, translateResponse, metrics)
	}

	latencyMs := time.Since(start).Milliseconds()
	statusCode := http.StatusOK
	if fwdErr != nil {
		if ue, ok := fwdErr.(*upstreamError); ok {
			statusCode = ue.StatusCode
		} else {
			statusCode = http.StatusBadGateway
		}
	}
	if healthErr := db.RecordProviderHealth(h.Repo.RawDB(), provider, model, statusCode, latencyMs); healthErr != nil {
		log.Printf("[health] failed to record health for %s/%s: %v", provider, model, healthErr)
	}

	if fwdErr == nil {
		usage := translator.GetAndClearLastUsage()
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
	}
	return fwdErr
}

// applyTokenSavers runs RTK compression and prompt injection on the request body.
func (h *ChatHandler) applyTokenSavers(body []byte) []byte {
	out := body
	var ok bool
	if h.TokenSaver.RTKEnabled() {
		out, ok = tokensaver.CompressMessages(out)
		if !ok {
			log.Printf("[tokensaver] RTK compression failed")
		}
	}
	if h.TokenSaver.CavemanEnabled() {
		out, ok = tokensaver.InjectSystemPrompt(out, tokensaver.CavemanPrompt)
		if !ok {
			log.Printf("[tokensaver] Caveman injection failed")
		}
	}
	if h.TokenSaver.PonytailEnabled() {
		out, ok = tokensaver.InjectSystemPrompt(out, tokensaver.PonytailPrompt)
		if !ok {
			log.Printf("[tokensaver] Ponytail injection failed")
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
