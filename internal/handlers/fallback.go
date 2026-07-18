package handlers

import (
	"fmt"
	"log"
	"net/http"
	"slices"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/rtk"
	"9router/proxy/internal/translator"
)

// handleAccountFallback attempts to forward a request with automatic account fallback.
// On 401/429 upstream errors, it locks the provider/model and retries with the next available connection.
// Returns nil on success (response already written to w), or the last error on failure.
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
		_, connData, err := h.getBestConnection(provider, pinnedConnectionID, nil, model)
		if err != nil {
			return err
		}
		return h.tryForwardWithConnection(w, provider, model, pinnedConnectionID, connData, body, isStream, translateResponse, endpoint)
	}

	if !db.IsProviderHealthy(h.Repo.RawDB(), provider, model) {
		log.Printf("[health] provider %s/%s is unhealthy (consecutive errors >= 5), skipping", provider, model)
	}

	locked, _ := h.Repo.IsModelLocked(provider, model)

	allConns, err := h.Repo.GetProviderConnections(provider, true)
	if err != nil || len(allConns) == 0 {
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
			continue
		}

		_ = apiKey // used by tryForwardWithConnection via connData

		if locked {
			// Still try other connections if available
		}

		lastErr = h.tryForwardWithConnection(w, provider, model, connObj.ID, connData, body, isStream, translateResponse, endpoint)
		if lastErr == nil {
			return nil
		}

		if ue, ok := lastErr.(*upstreamError); ok && retryableStatusCodes[ue.StatusCode] {
			durationSec := 60
			if ue.StatusCode == http.StatusUnauthorized {
				durationSec = 120
			}
			errMsg := fmt.Sprintf("%d upstream error", ue.StatusCode)
			h.Repo.LockModel(provider, model, durationSec, errMsg, ue.StatusCode)
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
// It records provider health (status code + latency) after every attempt.
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
		return err
	}

	apiKey := extractAPIKey(connData)
	if apiKey == "" {
		return &upstreamError{StatusCode: http.StatusUnauthorized, Body: []byte(`{"error":{"message":"no API key found","type":"auth_error","code":401}}`)}
	}

	compressedBody := body
	if h.RTKEnabled {
		compressedBody, _ = rtk.CompressMessages(body)
	}

	start := time.Now()
	metrics := &streamMetrics{}
	fwdErr := h.forwardRequest(w, providerCfg, apiKey, compressedBody, isStream, translateResponse, metrics)
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
