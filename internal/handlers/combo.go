package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"9router/proxy/internal/translator"
)

// applyComboStrategy reorders combo models based on the configured strategy.
func (h *ChatHandler) applyComboStrategy(strategy string, models []string) []string {
	if len(models) <= 1 {
		return models
	}

	switch strategy {
	case "round-robin":
		h.rrMu.Lock()
		start := h.rrIdx % len(models)
		h.rrIdx++
		h.rrMu.Unlock()
		out := make([]string, len(models))
		for i := 0; i < len(models); i++ {
			out[i] = models[(start+i)%len(models)]
		}
		return out
	case "capacity":
		fallthrough
	default:
		out := make([]string, len(models))
		copy(out, models)
		return out
	}
}

// handleComboFallback iterates through combo model entries, trying each one.
func (h *ChatHandler) handleComboFallback(w http.ResponseWriter, body []byte, comboModels []string, strategy string, isStream bool, translateResponse bool) {
	var lastErr *upstreamError

	models := h.applyComboStrategy(strategy, comboModels)

	for _, entry := range models {
		modelInfo := h.resolveModelEntry(entry)
		if modelInfo == nil {
			continue
		}

		var providerCfg *ProviderConfig
		var apiKey string
		if cfg, ok := knownProviders[modelInfo.Provider]; ok && (cfg.NoAuth || cfg.DefaultAPIKey != "") {
			c := cfg
			providerCfg = &c
			apiKey = c.DefaultAPIKey
		} else {
			_, connData, err := h.getBestConnection(modelInfo.Provider, modelInfo.ConnectionID, nil, modelInfo.Model)
			if err != nil {
				continue
			}
			cfg, err := h.getProviderConfig(modelInfo.Provider, connData)
			if err != nil {
				continue
			}
			providerCfg = cfg
			apiKey = extractAPIKey(connData)
			if apiKey == "" {
				continue
			}
		}

		var upstreamBody map[string]any
		if err := json.Unmarshal(body, &upstreamBody); err != nil {
			writeJSONError(w, http.StatusBadRequest, "failed to parse request body")
			return
		}
		upstreamBody["model"] = modelInfo.Model

		upstreamJSON, err := json.Marshal(upstreamBody)
		if err != nil {
			continue
		}

		comboStart := time.Now()
		comboMetrics := &streamMetrics{}

		var fwdErr error
		if modelInfo.Provider == "mimo-free" {
			fwdErr = h.MimoFreeChat(w, upstreamJSON, isStream, comboMetrics)
		} else {
			fwdErr = h.forwardRequest(w, providerCfg, apiKey, upstreamJSON, isStream, translateResponse, comboMetrics)
		}
		comboLatency := time.Since(comboStart).Milliseconds()
		if fwdErr != nil {
			if ue, ok := fwdErr.(*upstreamError); ok {
				lastErr = ue
				continue
			}
			lastErr = &upstreamError{StatusCode: http.StatusBadGateway, Body: []byte(fmt.Sprintf(`{"error":{"message":"upstream error: %v","type":"upstream_error","code":502}}`, fwdErr))}
			continue
		}

		usage := translator.GetAndClearLastUsage()
		if usage == nil {
			usage = &translator.OpenAIUsage{}
		}
		logInfo := &UsageLogInfo{
			Provider:     modelInfo.Provider,
			Model:        modelInfo.Model,
			ConnectionID: modelInfo.ConnectionID,
			APIKey:       apiKey,
			Endpoint:     "/v1/chat/completions",
		}
		h.logUsage(logInfo, usage, comboLatency, upstreamJSON, comboMetrics)
		return
	}

	if lastErr != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(lastErr.StatusCode)
		w.Write(lastErr.Body)
		return
	}
	writeJSONError(w, http.StatusBadGateway, "all combo models failed: no valid entries")
}

// handleMessagesComboFallback iterates through combo models for the Claude endpoint.
func (h *ChatHandler) handleMessagesComboFallback(w http.ResponseWriter, translatedReq map[string]any, comboModels []string, strategy string, isStream bool) {
	var lastErr *upstreamError

	models := h.applyComboStrategy(strategy, comboModels)

	for _, entry := range models {
		modelInfo := h.resolveModelEntry(entry)
		if modelInfo == nil {
			continue
		}

		var providerCfg *ProviderConfig
		var apiKey string
		if cfg, ok := knownProviders[modelInfo.Provider]; ok && (cfg.NoAuth || cfg.DefaultAPIKey != "") {
			c := cfg
			providerCfg = &c
			apiKey = c.DefaultAPIKey
		} else {
			_, connData, err := h.getBestConnection(modelInfo.Provider, modelInfo.ConnectionID, nil, modelInfo.Model)
			if err != nil {
				continue
			}
			cfg, err := h.getProviderConfig(modelInfo.Provider, connData)
			if err != nil {
				continue
			}
			providerCfg = cfg
			apiKey = extractAPIKey(connData)
			if apiKey == "" {
				continue
			}
		}

		entryReq := make(map[string]any, len(translatedReq))
		for k, v := range translatedReq {
			entryReq[k] = v
		}
		entryReq["model"] = modelInfo.Model

		upstreamJSON, err := json.Marshal(entryReq)
		if err != nil {
			continue
		}

		comboStart := time.Now()
		comboMetrics := &streamMetrics{}
		fwdErr := h.forwardRequest(w, providerCfg, apiKey, upstreamJSON, isStream, true, comboMetrics)
		comboLatency := time.Since(comboStart).Milliseconds()
		if fwdErr != nil {
			if ue, ok := fwdErr.(*upstreamError); ok {
				lastErr = ue
				continue
			}
			lastErr = &upstreamError{StatusCode: http.StatusBadGateway, Body: []byte(fmt.Sprintf(`{"error":{"message":"upstream error: %v","type":"upstream_error","code":502}}`, fwdErr))}
			continue
		}

		usage := translator.GetAndClearLastUsage()
		if usage == nil {
			usage = &translator.OpenAIUsage{}
		}
		logInfo := &UsageLogInfo{
			Provider:     modelInfo.Provider,
			Model:        modelInfo.Model,
			ConnectionID: modelInfo.ConnectionID,
			APIKey:       apiKey,
			Endpoint:     "/v1/v1/messages",
		}
		h.logUsage(logInfo, usage, comboLatency, upstreamJSON, comboMetrics)
		return
	}

	if lastErr != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(lastErr.StatusCode)
		w.Write(lastErr.Body)
		return
	}
	writeJSONError(w, http.StatusBadGateway, "all combo models failed: no valid entries")
}
