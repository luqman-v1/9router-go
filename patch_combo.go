package main

import (
	"os"
	"strings"
	"log"
)

func main() {
	b, err := os.ReadFile("internal/handlers/chat/combo.go")
	if err != nil { log.Fatal(err) }
	content := string(b)

	// Fix handleComboFallback
	target1 := `		var connID string
		var connData *ConnectionData
		if cfg, ok := providers.KnownProviders[modelInfo.Provider]; ok && (cfg.NoAuth || cfg.DefaultAPIKey != "") {
			connData = &ConnectionData{
				APIKey: cfg.DefaultAPIKey,
			}
		} else {
			conn, cData, err := h.getBestConnection(modelInfo.Provider, modelInfo.ConnectionID, excludeIDs, modelInfo.Model)
			if err != nil {
				continue
			}
			connID = conn.ID
			connData = cData
		}
		// Skip a connection that is already locked for this model (pinned
		// connections bypass the lock check inside getBestConnection).
		if connID != "" {
			if locked, _ := h.Repo.IsConnectionModelLocked(connID, modelInfo.Model); locked {
				log.Warn("combo", "skip locked connection", "provider", modelInfo.Provider, "model", modelInfo.Model, "conn", connID)
				continue
			}
		}

		var upstreamBody map[string]any
		if err := json.Unmarshal(body, &upstreamBody); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to parse request body")
			return
		}
		upstreamBody["model"] = modelInfo.Model

		upstreamJSON, err := json.Marshal(upstreamBody)
		if err != nil {
			continue
		}

		var fwdErr error
		if modelInfo.Provider == "mimo-free" {
			comboMetrics := &streamMetrics{}
			fwdErr = h.MimoFreeChat(ctx, cw, upstreamJSON, isStream, comboMetrics)
		} else {
			fwdErr = h.tryForwardWithConnection(ctx, cw, modelInfo.Provider, modelInfo.Model, connID, connData, upstreamJSON, isStream, translateResponse, "/v1/chat/completions")
		}
		if fwdErr != nil {
			var ue *upstreamError
			if errors.As(fwdErr, &ue) {
				// Retryable error: lock the connection so this request and future
				// requests back off instead of re-hammering the same account quota.
				if providers.RetryableStatusCodes[ue.StatusCode] {
					h.comboLockRetryable(&excludeIDs, connID, modelInfo.Provider, modelInfo.Model, ue)
				}
				// Transient error: classify and wait before trying next model
				if ue.StatusCode == http.StatusServiceUnavailable || ue.StatusCode == http.StatusBadGateway || ue.StatusCode == http.StatusGatewayTimeout {
					errorText := extractErrorText(ue.Body)
					classification := providers.ClassifyError(ue.StatusCode, errorText, 0)
					if classification.CooldownMs > 0 && classification.CooldownMs <= 5000 {
						cooldown := time.Duration(classification.CooldownMs) * time.Millisecond
						log.Info("combo", "transient wait", "status", ue.StatusCode, "provider", modelInfo.Provider, "duration", cooldown)
						time.Sleep(cooldown)
					} else {
						// Cooldown >5s (e.g. "no credentials"): fall through immediately
						log.Info("combo", "transient skip", "status", ue.StatusCode, "provider", modelInfo.Provider, "cooldownMs", classification.CooldownMs)
				}
				}
				// Track earliest retryAfter across combo models
				if ra := extractRetryAfter(ue.Body); ra != "" {
					if earliestRetryAfter == "" || ra < earliestRetryAfter {
						earliestRetryAfter = ra
					}
				}
				lastErr = ue
				continue
			}
			lastErr = &upstreamError{StatusCode: http.StatusBadGateway, Body: []byte(fmt.Sprintf(` + "`" + `{"error":{"message":"upstream error: %v","type":"upstream_error","code":502}}` + "`" + `, fwdErr))}
			continue
		}

		// tryForwardWithConnection already logs usage, so we just return.
		return`

	replacement1 := `		var entrySuccess bool
		// Try up to 10 connections for this model entry
		for connAttempt := 0; connAttempt < 10; connAttempt++ {
			var connID string
			var connData *ConnectionData
			isKnownNoAuth := false
			if cfg, ok := providers.KnownProviders[modelInfo.Provider]; ok && (cfg.NoAuth || cfg.DefaultAPIKey != "") {
				isKnownNoAuth = true
				connData = &ConnectionData{
					APIKey: cfg.DefaultAPIKey,
				}
			} else {
				conn, cData, err := h.getBestConnection(modelInfo.Provider, modelInfo.ConnectionID, excludeIDs, modelInfo.Model)
				if err != nil {
					break
				}
				connID = conn.ID
				connData = cData
			}
			// Skip a connection that is already locked for this model
			if connID != "" {
				if locked, _ := h.Repo.IsConnectionModelLocked(connID, modelInfo.Model); locked {
					log.Warn("combo", "skip locked connection", "provider", modelInfo.Provider, "model", modelInfo.Model, "conn", connID)
					excludeIDs = append(excludeIDs, connID)
					continue
				}
			}

			var upstreamBody map[string]any
			upstreamBodyJSON := body
			poolModels := getCapacityAdapterModels(settings)
			if contains(poolModels, modelInfo.Model) || contains(poolModels, modelInfo.Provider+"/"+modelInfo.Model) {
				upstreamBodyJSON = stripHistoryForContext(body, getContextWindow(entry))
			}

			if err := json.Unmarshal(upstreamBodyJSON, &upstreamBody); err != nil {
				handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to parse request body")
				return
			}
			upstreamBody["model"] = modelInfo.Model

			upstreamJSON, err := json.Marshal(upstreamBody)
			if err != nil {
				break
			}

			var fwdErr error
			if modelInfo.Provider == "mimo-free" {
				comboMetrics := &streamMetrics{}
				fwdErr = h.MimoFreeChat(ctx, cw, upstreamJSON, isStream, comboMetrics)
			} else {
				fwdErr = h.tryForwardWithConnection(ctx, cw, modelInfo.Provider, modelInfo.Model, connID, connData, upstreamJSON, isStream, translateResponse, "/v1/chat/completions")
			}
			
			if fwdErr != nil {
				if ctx.Err() != nil {
					lastErr = &upstreamError{StatusCode: 499, Body: []byte(` + "`" + `{"error":{"message":"client closed request","type":"client_closed_request","code":499}}` + "`" + `)}
					break
				}
				var ue *upstreamError
				if errors.As(fwdErr, &ue) {
					if providers.RetryableStatusCodes[ue.StatusCode] {
						h.comboLockRetryable(&excludeIDs, connID, modelInfo.Provider, modelInfo.Model, ue)
					}
					if ue.StatusCode == http.StatusServiceUnavailable || ue.StatusCode == http.StatusBadGateway || ue.StatusCode == http.StatusGatewayTimeout {
						errorText := extractErrorText(ue.Body)
						classification := providers.ClassifyError(ue.StatusCode, errorText, 0)
						if classification.CooldownMs > 0 && classification.CooldownMs <= 5000 {
							cooldown := time.Duration(classification.CooldownMs) * time.Millisecond
							log.Info("combo", "transient wait", "status", ue.StatusCode, "provider", modelInfo.Provider, "duration", cooldown)
							time.Sleep(cooldown)
						} else {
							log.Info("combo", "transient skip", "status", ue.StatusCode, "provider", modelInfo.Provider, "cooldownMs", classification.CooldownMs)
						}
					}
					if ra := extractRetryAfter(ue.Body); ra != "" {
						if earliestRetryAfter == "" || ra < earliestRetryAfter {
							earliestRetryAfter = ra
						}
					}
					lastErr = ue
					if isKnownNoAuth {
						break
					}
					continue
				}
				lastErr = &upstreamError{StatusCode: http.StatusBadGateway, Body: []byte(fmt.Sprintf(` + "`" + `{"error":{"message":"upstream error: %v","type":"upstream_error","code":502}}` + "`" + `, fwdErr))}
				if isKnownNoAuth {
					break
				}
				continue
			}

			entrySuccess = true
			break
		}
		
		if entrySuccess || ctx.Err() != nil {
			return
		}`

	content = strings.Replace(content, target1, replacement1, 1)

	// Fix handleMessagesComboFallback
	target2 := `		var connID string
		var connData *ConnectionData
		if cfg, ok := providers.KnownProviders[modelInfo.Provider]; ok && (cfg.NoAuth || cfg.DefaultAPIKey != "") {
			connData = &ConnectionData{
				APIKey: cfg.DefaultAPIKey,
			}
		} else {
			conn, cData, err := h.getBestConnection(modelInfo.Provider, modelInfo.ConnectionID, excludeIDs, modelInfo.Model)
			if err != nil {
				continue
			}
			connID = conn.ID
			connData = cData
		}
		// Skip a connection that is already locked for this model (pinned
		// connections bypass the lock check inside getBestConnection).
		if connID != "" {
			if locked, _ := h.Repo.IsConnectionModelLocked(connID, modelInfo.Model); locked {
				log.Warn("combo", "skip locked connection", "provider", modelInfo.Provider, "model", modelInfo.Model, "conn", connID)
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

		fwdErr := h.tryForwardWithConnection(ctx, cw, modelInfo.Provider, modelInfo.Model, connID, connData, upstreamJSON, isStream, true, "/v1/messages")

		if fwdErr != nil {
			var ue *upstreamError
			if errors.As(fwdErr, &ue) {
				// Retryable error: lock the connection so this request and future
				// requests back off instead of re-hammering the same account quota.
				if providers.RetryableStatusCodes[ue.StatusCode] {
					h.comboLockRetryable(&excludeIDs, connID, modelInfo.Provider, modelInfo.Model, ue)
				}
				// Transient error: classify and wait before trying next model
				if ue.StatusCode == http.StatusServiceUnavailable || ue.StatusCode == http.StatusBadGateway || ue.StatusCode == http.StatusGatewayTimeout {
					errorText := extractErrorText(ue.Body)
					classification := providers.ClassifyError(ue.StatusCode, errorText, 0)
					if classification.CooldownMs > 0 && classification.CooldownMs <= 5000 {
						cooldown := time.Duration(classification.CooldownMs) * time.Millisecond
						log.Info("combo", "transient wait", "status", ue.StatusCode, "provider", modelInfo.Provider, "duration", cooldown)
						time.Sleep(cooldown)
					} else {
						// Cooldown >5s (e.g. "no credentials"): fall through immediately
						log.Info("combo", "transient skip", "status", ue.StatusCode, "provider", modelInfo.Provider, "cooldownMs", classification.CooldownMs)
				}
				}
				// Track earliest retryAfter across combo models
				if ra := extractRetryAfter(ue.Body); ra != "" {
					if earliestRetryAfter == "" || ra < earliestRetryAfter {
						earliestRetryAfter = ra
					}
				}
				lastErr = ue
				continue
			}
			lastErr = &upstreamError{StatusCode: http.StatusBadGateway, Body: []byte(fmt.Sprintf(` + "`" + `{"error":{"message":"upstream error: %v","type":"upstream_error","code":502}}` + "`" + `, fwdErr))}
			continue
		}

		// tryForwardWithConnection already logs usage, so we just return.
		return`

	replacement2 := `		var entrySuccess bool
		// Try up to 10 connections for this model entry
		for connAttempt := 0; connAttempt < 10; connAttempt++ {
			var connID string
			var connData *ConnectionData
			isKnownNoAuth := false
			if cfg, ok := providers.KnownProviders[modelInfo.Provider]; ok && (cfg.NoAuth || cfg.DefaultAPIKey != "") {
				isKnownNoAuth = true
				connData = &ConnectionData{
					APIKey: cfg.DefaultAPIKey,
				}
			} else {
				conn, cData, err := h.getBestConnection(modelInfo.Provider, modelInfo.ConnectionID, excludeIDs, modelInfo.Model)
				if err != nil {
					break
				}
				connID = conn.ID
				connData = cData
			}
			// Skip a connection that is already locked for this model
			if connID != "" {
				if locked, _ := h.Repo.IsConnectionModelLocked(connID, modelInfo.Model); locked {
					log.Warn("combo", "skip locked connection", "provider", modelInfo.Provider, "model", modelInfo.Model, "conn", connID)
					excludeIDs = append(excludeIDs, connID)
					continue
				}
			}

			entryReq := make(map[string]any, len(translatedReq))
			for k, v := range translatedReq {
				entryReq[k] = v
			}
			
			poolModels := getCapacityAdapterModels(settings)
			if contains(poolModels, modelInfo.Model) || contains(poolModels, modelInfo.Provider+"/"+modelInfo.Model) {
				bodyJSON, _ := json.Marshal(translatedReq)
				stripped := stripHistoryForContext(bodyJSON, getContextWindow(entry))
				var strippedMap map[string]any
				if err := json.Unmarshal(stripped, &strippedMap); err == nil {
					for k, v := range strippedMap {
						entryReq[k] = v
					}
				}
			}
			entryReq["model"] = modelInfo.Model

			upstreamJSON, err := json.Marshal(entryReq)
			if err != nil {
				break
			}

			fwdErr := h.tryForwardWithConnection(ctx, cw, modelInfo.Provider, modelInfo.Model, connID, connData, upstreamJSON, isStream, true, "/v1/messages")

			if fwdErr != nil {
				if ctx.Err() != nil {
					lastErr = &upstreamError{StatusCode: 499, Body: []byte(` + "`" + `{"error":{"message":"client closed request","type":"client_closed_request","code":499}}` + "`" + `)}
					break
				}
				var ue *upstreamError
				if errors.As(fwdErr, &ue) {
					if providers.RetryableStatusCodes[ue.StatusCode] {
						h.comboLockRetryable(&excludeIDs, connID, modelInfo.Provider, modelInfo.Model, ue)
					}
					if ue.StatusCode == http.StatusServiceUnavailable || ue.StatusCode == http.StatusBadGateway || ue.StatusCode == http.StatusGatewayTimeout {
						errorText := extractErrorText(ue.Body)
						classification := providers.ClassifyError(ue.StatusCode, errorText, 0)
						if classification.CooldownMs > 0 && classification.CooldownMs <= 5000 {
							cooldown := time.Duration(classification.CooldownMs) * time.Millisecond
							log.Info("combo", "transient wait", "status", ue.StatusCode, "provider", modelInfo.Provider, "duration", cooldown)
							time.Sleep(cooldown)
						} else {
							log.Info("combo", "transient skip", "status", ue.StatusCode, "provider", modelInfo.Provider, "cooldownMs", classification.CooldownMs)
						}
					}
					if ra := extractRetryAfter(ue.Body); ra != "" {
						if earliestRetryAfter == "" || ra < earliestRetryAfter {
							earliestRetryAfter = ra
						}
					}
					lastErr = ue
					if isKnownNoAuth {
						break
					}
					continue
				}
				lastErr = &upstreamError{StatusCode: http.StatusBadGateway, Body: []byte(fmt.Sprintf(` + "`" + `{"error":{"message":"upstream error: %v","type":"upstream_error","code":502}}` + "`" + `, fwdErr))}
				if isKnownNoAuth {
					break
				}
				continue
			}

			entrySuccess = true
			break
		}

		if entrySuccess || ctx.Err() != nil {
			return
		}`

	content = strings.Replace(content, target2, replacement2, 1)
	
	// Add settings extraction at the top of handleComboFallback
	content = strings.Replace(content, "var earliestRetryAfter string", "var earliestRetryAfter string\n\tsettings, _ := h.Repo.GetSettings()", 1)
	
	// Add settings extraction at the top of handleMessagesComboFallback
	// BUT because strings.Replace with 1 only replaces the first, we need to do it correctly.
	// Actually we can just do strings.ReplaceAll, it's safe because earliestRetryAfter is declared twice.
	content = strings.ReplaceAll(content, "var earliestRetryAfter string", "var earliestRetryAfter string\n\tsettings, _ := h.Repo.GetSettings()")
	
	os.WriteFile("internal/handlers/chat/combo.go", []byte(content), 0644)
}
