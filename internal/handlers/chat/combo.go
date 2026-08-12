package chat

import (
	"9router/proxy/internal/log"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"9router/proxy/internal/constants"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/providers"
)

// detectNewTurn reports whether the request body starts a new conversation
// turn. A turn boundary is the most recent plain-text user message; a request
// whose last user-type message is a tool result continues the current turn,
// and the combo must not switch providers mid-turn (Gemini thinking models
// require a thought_signature on every current-turn functionCall, which only
// the model that made the call can provide).
func detectNewTurn(body []byte) bool {
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return true
	}
	for i := len(req.Messages) - 1; i >= 0; i-- {
		msg := req.Messages[i]
		if msg.Role == "tool" {
			return false
		}
		if msg.Role != "user" {
			continue
		}
		return contentHasText(msg.Content)
	}
	return true
}

// contentHasText reports whether OpenAI message content contains plain text.
func contentHasText(content json.RawMessage) bool {
	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		return s != ""
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &parts); err == nil {
		for _, p := range parts {
			if p.Text != "" {
				return true
			}
		}
	}
	return false
}

// visionProviders lists providers known to support vision/image input.
var visionProviders = map[string]bool{
	"openai":      true,
	"anthropic":   true,
	"claude":      true,
	"gemini":      true,
	"antigravity": true,
	"xai":         true,
	"mistral":     true,
	"groq":        true,
	"openrouter":  true,
}

// pdfProviders lists providers known to support PDF/document input.
var pdfProviders = map[string]bool{
	"openai":      true,
	"anthropic":   true,
	"claude":      true,
	"gemini":      true,
	"antigravity": true,
}

// modelHasCapability checks if a model (from a "provider/model" entry) supports
// the given capability. Returns true when uncertain (optimistic default).
func modelHasCapability(modelEntry string, cap string) bool {
	provider := modelEntry
	if idx := strings.Index(modelEntry, "/"); idx >= 0 {
		provider = modelEntry[:idx]
	}

	caps := providers.GetCapabilitiesForModel(provider, modelEntry)

	switch cap {
	case "vision":
		return caps.Vision
	case "pdf":
		return caps.PDF
	case "audioInput":
		return caps.AudioInput
	case "videoInput":
		return caps.VideoInput
	case "imageOutput":
		return caps.ImageOutput
	case "audioOutput":
		return caps.AudioOutput
	case "search":
		return caps.Search
	case "tools":
		return caps.Tools
	case "reasoning":
		return caps.Reasoning
	default:
		return true
	}
}

// DetectRequiredCapabilities extracts capabilities requested by the client
func DetectRequiredCapabilities(body []byte) map[string]bool {
	required := make(map[string]bool)

	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return required
	}

	// Scan messages (OpenAI / Claude format) - ONLY trailing user turn
	if msgs, ok := m["messages"].([]any); ok {
		for i := len(msgs) - 1; i >= 0; i-- {
			msg, ok := msgs[i].(map[string]any)
			if ok && msg["role"] == "user" {
				scanMessageContent(msgs[i], required)
				break
			}
		}
	}

	// Scan input (Responses API format) - ONLY trailing user turn
	if input, ok := m["input"].([]any); ok {
		for i := len(input) - 1; i >= 0; i-- {
			msg, ok := input[i].(map[string]any)
			if ok && msg["role"] == "user" {
				scanMessageContent(input[i], required)
				break
			}
		}
	}

	return required
}

// scanMessageContent checks a single message for capability requirements.
func scanMessageContent(msg any, required map[string]bool) {
	m, ok := msg.(map[string]any)
	if !ok {
		return
	}
	content := m["content"]
	if content == nil {
		return
	}

	switch c := content.(type) {
	case string:
		// No modality detection from plain text
	case []any:
		for _, block := range c {
			scanContentBlock(block, required)
		}
	}
}

// scanContentBlock checks a single content block for capability requirements.
func scanContentBlock(block any, required map[string]bool) {
	b, ok := block.(map[string]any)
	if !ok {
		return
	}
	typ, _ := b["type"].(string)
	switch typ {
	case "image_url", "image", "input_image":
		required["vision"] = true
	case "file", "document", "input_file":
		required["pdf"] = true
	case "audio_url", "audio", "input_audio":
		required["audioInput"] = true
	case "video_url", "video", "input_video":
		required["videoInput"] = true
	}
	// Check mime type for inlineData/fileData (Gemini format)
	if mime, ok := b["mimeType"].(string); ok {
		if strings.HasPrefix(mime, "image/") {
			required["vision"] = true
		} else if strings.HasPrefix(mime, "audio/") {
			required["audioInput"] = true
		} else if strings.HasPrefix(mime, "video/") {
			required["videoInput"] = true
		} else if mime == "application/pdf" {
			required["pdf"] = true
		}
	}
	// Also check nested inlineData / fileData
	for _, key := range []string{"inlineData", "fileData"} {
		if fd, ok := b[key].(map[string]any); ok {
			if mime, ok := fd["mimeType"].(string); ok {
				if strings.HasPrefix(mime, "image/") {
					required["vision"] = true
				} else if strings.HasPrefix(mime, "audio/") {
					required["audioInput"] = true
				} else if strings.HasPrefix(mime, "video/") {
					required["videoInput"] = true
				} else if mime == "application/pdf" {
					required["pdf"] = true
				}
			}
		}
	}
}

// ReorderByCapabilities stably floats models satisfying constraints to the front
func ReorderByCapabilities(comboModels []string, required map[string]bool) []string {
	if len(required) == 0 || len(comboModels) <= 1 {
		return comboModels
	}

	var tier0, tier1 []string
	for _, m := range comboModels {
		allMatch := true
		for cap := range required {
			if !modelHasCapability(m, cap) {
				allMatch = false
				break
			}
		}
		if allMatch {
			tier0 = append(tier0, m)
		} else {
			tier1 = append(tier1, m)
		}
	}

	result := make([]string, 0, len(comboModels))
	result = append(result, tier0...)
	result = append(result, tier1...)
	return result
}

// ApplyComboStrategy rotates the array of models based on the selected strategy.
// It treats every call as a new turn (backward-compatible wrapper).
func (h *ChatHandler) ApplyComboStrategy(strategy string, models []string, comboName string, stickyLimit int) []string {
	return h.applyComboStrategy(strategy, models, comboName, stickyLimit, true)
}

// applyComboStrategy is ApplyComboStrategy with turn awareness: the rotation
// index advances only on a new turn (newTurn=true), so a mid-turn tool-use
// sequence stays on the same provider/model.
func (h *ChatHandler) applyComboStrategy(strategy string, models []string, comboName string, stickyLimit int, newTurn bool) []string {
	if len(models) <= 1 {
		return models
	}

	switch strategy {
	case "round-robin":
		// Round-robin is just sticky with limit=1
		stickyLimit = 1
		fallthrough
	case "sticky":
		if stickyLimit <= 1 {
			stickyLimit = 1
		}
		h.stickyMu.Lock()
		defer h.stickyMu.Unlock()
		if h.stickyState == nil {
			h.stickyState = make(map[string]*comboStickyState)
		}

		key := comboName
		if key == "" {
			key = "__default__"
		}
		state, exists := h.stickyState[key]
		if !exists {
			state = &comboStickyState{Index: 0, ConsecutiveUseCount: 0}
			h.stickyState[key] = state
		}

		// The model owning this turn. A new turn starts from the rotation
		// pointer; a mid-turn request reuses the model serving the turn even
		// after the pointer has advanced for the next turn.
		servingIndex := state.Index % len(models)
		if newTurn {
			// Advance the rotation pointer at the turn boundary.
			state.ConsecutiveUseCount++
			if state.ConsecutiveUseCount >= stickyLimit {
				state.Index = (servingIndex + 1) % len(models)
				state.ConsecutiveUseCount = 0
			}
			state.ServingIndex = servingIndex
		} else {
			servingIndex = state.ServingIndex
		}

		rotated := make([]string, len(models))
		for i := 0; i < len(models); i++ {
			rotated[i] = models[(servingIndex+i)%len(models)]
		}

		return rotated
	case "capacity":
		fallthrough
	default:
		out := make([]string, len(models))
		copy(out, models)
		return out
	}
}

// keysString returns a comma-separated list of map keys.
func keysString(m map[string]bool) string {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

// handleComboFallback iterates through combo model entries, trying each one.
// Auto-capability-switch: floats vision/pdf-capable models to the front.
func (h *ChatHandler) handleComboFallback(ctx context.Context, w http.ResponseWriter, body []byte, comboModels []string, strategy string, isStream bool, translateResponse bool, comboName string, stickyLimit int) {
	cw := newCommittedResponseWriter(w)
	var lastErr *upstreamError
	var earliestRetryAfter string
	// Connections that failed with a retryable status this request; remaining
	// combo models must not re-select them (same account = same 429 quota).
	var excludeIDs []string

	// 1. Apply combo rotation strategy first
	models := h.applyComboStrategy(strategy, comboModels, comboName, stickyLimit, detectNewTurn(body))

	// 2. Auto-capability-switch: float models that satisfy the request's required capabilities to the front.
	// This ensures that if the rotated model lacks required capabilities (e.g., vision), a capable model overrides it.
	if required := DetectRequiredCapabilities(body); len(required) > 0 {
		reordered := ReorderByCapabilities(models, required)
		if reordered[0] != models[0] {
			log.Info("combo", "auto-switch", "caps", keysString(required), "model", reordered[0])
		}
		models = reordered
	}

	// If every model fails, retry the whole pass once after a bounded
	// Retry-After wait so a transient provider blip doesn't surface as a hard
	// 429 to the client.
	var attempt int
	for ; attempt < 2; attempt++ {
		if attempt > 0 {
			wait := comboRetryAfter(earliestRetryAfter)
			if wait == 0 {
				break
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			// Fresh pass: re-allow connections locked by the previous attempt
			// (their cooldown has elapsed) and clear the error state.
			lastErr = nil
			earliestRetryAfter = ""
			excludeIDs = nil
		}

		for _, entry := range models {
			modelInfo := h.resolveModelEntry(entry)
			if modelInfo == nil {
				continue
			}

			// Skip unavailable (model-locked) providers
			if !h.Repo.IsProviderAvailable(modelInfo.Provider, modelInfo.Model) {
				log.Warn("combo", "skip unhealthy", "provider", modelInfo.Provider, "model", modelInfo.Model)
				if lastErr == nil {
					lastErr = &upstreamError{StatusCode: http.StatusTooManyRequests, Body: []byte(`{"error":{"message":"all connections for this provider are rate-limited","type":"rate_limit_error","code":429}}`)}
				}
				continue
			}

			var entrySuccess bool
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
						lastErr = &upstreamError{StatusCode: 499, Body: []byte(`{"error":{"message":"client closed request","type":"client_closed_request","code":499}}`)}
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
					lastErr = &upstreamError{StatusCode: http.StatusBadGateway, Body: []byte(fmt.Sprintf(`{"error":{"message":"upstream error: %v","type":"upstream_error","code":502}}`, fwdErr))}
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
			}
		}

		// All entries failed. Retry once only if a bounded wait is available;
		// otherwise fall through to the error response below.
		if lastErr == nil || ctx.Err() != nil || attempt == 1 {
			break
		}
	}

	if lastErr != nil {
		if cw.IsCommitted() {
			log.Error("combo", "upstream error after headers committed", "error", lastErr)
			return
		}
		cw.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
		if earliestRetryAfter != "" {
			retryAfterSec := int((time.Until(mustParseTime(earliestRetryAfter)) + time.Second - 1) / time.Second)
			if retryAfterSec < 1 {
				retryAfterSec = 1
			}
			cw.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfterSec))
			retryHuman := formatRetryAfter(earliestRetryAfter)
			var errBody map[string]any
			if err := json.Unmarshal(lastErr.Body, &errBody); err == nil {
				if errObj, ok := errBody["error"].(map[string]any); ok {
					if msg, _ := errObj["message"].(string); msg != "" {
						errObj["message"] = msg + " (" + retryHuman + ")"
						updated, _ := json.Marshal(errBody)
						cw.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
						cw.WriteHeader(lastErr.StatusCode)
						cw.Write(updated)
						return
					}
				}
			}
		}
		cw.WriteHeader(lastErr.StatusCode)
		cw.Write(lastErr.Body)
		return
	}
	if cw.IsCommitted() {
		return
	}
	handlerutil.WriteJSONError(cw, http.StatusBadGateway, "all combo models failed: no valid entries")
}

// handleMessagesComboFallback iterates through combo models for the Claude endpoint.
// Auto-capability-switch: floats vision/pdf-capable models to the front.
func (h *ChatHandler) handleMessagesComboFallback(ctx context.Context, w http.ResponseWriter, translatedReq map[string]any, comboModels []string, strategy string, isStream bool, comboName string, stickyLimit int) {
	cw := newCommittedResponseWriter(w)
	var lastErr *upstreamError
	var earliestRetryAfter string

	// Auto-capability-switch: convert body to JSON for detection
	bodyJSON, _ := json.Marshal(translatedReq)
	// Connections that failed with a retryable status this request; remaining
	// combo models must not re-select them (same account = same 429 quota).
	var excludeIDs []string
	models := h.applyComboStrategy(strategy, comboModels, comboName, stickyLimit, detectNewTurn(bodyJSON))
	if required := DetectRequiredCapabilities(bodyJSON); len(required) > 0 {
		reordered := ReorderByCapabilities(models, required)
		models = reordered
	}

	// If every model fails, retry the whole pass once after a bounded
	// Retry-After wait so a transient provider blip doesn't surface as a hard
	// 429 to the client.
	var attempt int
	for ; attempt < 2; attempt++ {
		if attempt > 0 {
			wait := comboRetryAfter(earliestRetryAfter)
			if wait == 0 {
				break
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			// Fresh pass: re-allow connections locked by the previous attempt
			// (their cooldown has elapsed) and clear the error state.
			lastErr = nil
			earliestRetryAfter = ""
			excludeIDs = nil
		}

		for _, entry := range models {
			modelInfo := h.resolveModelEntry(entry)
			if modelInfo == nil {
				continue
			}

			// Skip unavailable (model-locked) providers
			if !h.Repo.IsProviderAvailable(modelInfo.Provider, modelInfo.Model) {
				log.Warn("combo", "skip unhealthy", "provider", modelInfo.Provider, "model", modelInfo.Model)
				if lastErr == nil {
					lastErr = &upstreamError{StatusCode: http.StatusTooManyRequests, Body: []byte(`{"error":{"message":"all connections for this provider are rate-limited","type":"rate_limit_error","code":429}}`)}
				}
				continue
			}

			var entrySuccess bool
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

				entryReq["model"] = modelInfo.Model

				upstreamJSON, err := json.Marshal(entryReq)
				if err != nil {
					break
				}

				fwdErr := h.tryForwardWithConnection(ctx, cw, modelInfo.Provider, modelInfo.Model, connID, connData, upstreamJSON, isStream, true, "/v1/messages")

				if fwdErr != nil {
					if ctx.Err() != nil {
						lastErr = &upstreamError{StatusCode: 499, Body: []byte(`{"error":{"message":"client closed request","type":"client_closed_request","code":499}}`)}
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
					lastErr = &upstreamError{StatusCode: http.StatusBadGateway, Body: []byte(fmt.Sprintf(`{"error":{"message":"upstream error: %v","type":"upstream_error","code":502}}`, fwdErr))}
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
			}
		}

		// All entries failed. Retry once only if a bounded wait is available;
		// otherwise fall through to the error response below.
		if lastErr == nil || ctx.Err() != nil || attempt == 1 {
			break
		}
	}

	if lastErr != nil {
		if cw.IsCommitted() {
			log.Error("combo", "upstream error after headers committed", "error", lastErr)
			return
		}
		cw.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
		if earliestRetryAfter != "" {
			retryAfterSec := int((time.Until(mustParseTime(earliestRetryAfter)) + time.Second - 1) / time.Second)
			if retryAfterSec < 1 {
				retryAfterSec = 1
			}
			cw.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfterSec))
			retryHuman := formatRetryAfter(earliestRetryAfter)
			var errBody map[string]any
			if err := json.Unmarshal(lastErr.Body, &errBody); err == nil {
				if errObj, ok := errBody["error"].(map[string]any); ok {
					if msg, _ := errObj["message"].(string); msg != "" {
						errObj["message"] = msg + " (" + retryHuman + ")"
						updated, _ := json.Marshal(errBody)
						cw.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
						cw.WriteHeader(lastErr.StatusCode)
						cw.Write(updated)
						return
					}
				}
			}
		}
		cw.WriteHeader(lastErr.StatusCode)
		cw.Write(lastErr.Body)
		return
	}
	if cw.IsCommitted() {
		return
	}
	handlerutil.WriteJSONError(cw, http.StatusBadGateway, "all combo models failed: no valid entries")
}

// mustParseTime parses an RFC3339 timestamp. Returns zero time on error.
func mustParseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// comboRetryWaitCap bounds how long a fully-failed combo pass will hold the
// request before retrying. Longer upstream Retry-After values are surfaced via
// the Retry-After header instead so the client decides.
const comboRetryWaitCap = 8 * time.Second

// comboRetryAfter returns how long to wait before retrying a fully-failed
// combo pass. It honors the earliest upstream Retry-After, capped at
// comboRetryWaitCap. Returns 0 (no retry) when there is no usable Retry-After
// or the wait would exceed the cap.
func comboRetryAfter(retryAfter string) time.Duration {
	if retryAfter == "" {
		return 0
	}
	until := mustParseTime(retryAfter)
	if until.IsZero() {
		return 0
	}
	sec := int((time.Until(until) + time.Second - 1) / time.Second)
	if sec < 1 {
		sec = 1
	}
	if sec > int(comboRetryWaitCap/time.Second) {
		return 0
	}
	return time.Duration(sec) * time.Second
}

// comboLockRetryable classifies a retryable upstream error in a combo loop and
// locks the failed connection+model so the exponential backoff persists across
// requests. The combo path iterates tryForwardWithConnection directly and used
// to skip this entirely — so a 429 never set a lock, every request re-tried all
// combo models on the same account, and Google rate-limited it forever. The
// connection is also appended to excludeIDs so the remaining combo models in
// this request skip it instead of re-hitting the same quota bucket.
func (h *ChatHandler) comboLockRetryable(excludeIDs *[]string, connID, provider, model string, ue *upstreamError) {
	if connID == "" {
		return
	}
	currentLevel := h.Repo.GetConnectionBackoffLevel(connID)
	cls := providers.ClassifyError(ue.StatusCode, extractErrorText(ue.Body), currentLevel)
	if cls.CooldownMs <= 0 {
		return
	}
	cooldownSec := int((cls.CooldownMs + 999) / 1000)
	if err := h.Repo.LockConnectionModel(connID, model, cooldownSec, cls.NewBackoffLevel); err != nil {
		log.Warn("combo", "lock failed", "conn", connID, "provider", provider, "model", model, "error", err)
	}
	*excludeIDs = append(*excludeIDs, connID)
	log.Warn("combo", "locked on retryable error", "provider", provider, "model", model, "conn", connID, "status", ue.StatusCode, "cooldown_s", cooldownSec)
}

// ---- Fusion (parallel fan-out + judge synthesis) ----

// fusionResult holds a single panel model's response.
type fusionResult struct {
	model string
	body  []byte
	ok    bool
	err   error
}

// FusionTuning tunes parallel-collection behavior of combo fusion.
// Matches JS FUSION_DEFAULTS in open-sse/services/combo.js.
type FusionTuning struct {
	MinPanel           int `json:"minPanel"`
	StragglerGraceMs   int `json:"stragglerGraceMs"`
	PanelHardTimeoutMs int `json:"panelHardTimeoutMs"`
}

var fusionDefaults = FusionTuning{
	MinPanel:           2,
	StragglerGraceMs:   8000,
	PanelHardTimeoutMs: 90000,
}

// handleFusion implements combo fusion: parallel model fan-out + judge synthesis.
// Matches JS handleFusionChat in combo.js.
func (h *ChatHandler) handleFusion(ctx context.Context, w http.ResponseWriter, body []byte, comboModels []string, strategy string, isStream bool, translateResponse bool, comboName string, stickyLimit int) {
	cw := newCommittedResponseWriter(w)
	panel := h.ApplyComboStrategy(strategy, comboModels, comboName, stickyLimit)
	if len(panel) == 0 {
		handlerutil.WriteJSONError(cw, http.StatusBadRequest, "fusion combo has no models")
		return
	}
	if len(panel) == 1 {
		h.handleComboFallback(ctx, cw, body, panel, "fallback", isStream, translateResponse, comboName, stickyLimit)
		return
	}

	// Build panel body: strip tools → prose, force non-streaming
	var panelBody map[string]any
	if err := json.Unmarshal(body, &panelBody); err != nil {
		handlerutil.WriteJSONError(cw, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if msgs, ok := panelBody["messages"].([]any); ok {
		panelBody["messages"] = flattenToolHistory(msgs)
	}
	panelBody["stream"] = false
	delete(panelBody, "tools")
	delete(panelBody, "tool_choice")
	panelJSON, err := json.Marshal(panelBody)
	if err != nil {
		handlerutil.WriteJSONError(cw, http.StatusInternalServerError, "failed to marshal panel body")
		return
	}

	// Fan-out panel calls
	ft := fusionDefaults
	calls := make([]func() *fusionResult, len(panel))
	for i, entry := range panel {
		calls[i] = h.makePanelCall(panelJSON, entry)
	}

	settled := collectPanel(calls, ft)

	// Extract successful answers
	var answers []fusionAnswer
	judgeModel := panel[0] // default: first panel model
	for i, res := range settled {
		if res == nil || !res.ok {
			continue
		}
		text := extractPanelText(res.body)
		if text == "" {
			continue
		}
		answers = append(answers, fusionAnswer{model: panel[i], text: text})
	}

	// Degradation
	if len(answers) == 0 {
		handlerutil.WriteJSONError(cw, http.StatusServiceUnavailable, "all fusion panel models failed")
		return
	}
	if len(answers) == 1 {
		h.handleComboFallback(ctx, cw, body, []string{answers[0].model}, "fallback", isStream, translateResponse, comboName, stickyLimit)
		return
	}

	// Judge synthesizes final answer
	judgeBody := appendUserTurn(body, buildJudgePrompt(answers))

	var ub map[string]any
	if err := json.Unmarshal(judgeBody, &ub); err != nil {
		log.Error("fusion", "unmarshal judge body failed", "error", err)
		handlerutil.WriteJSONError(cw, http.StatusInternalServerError, "failed to parse judge request")
		return
	}
	ub["model"] = judgeModel
	if isStream {
		ub["stream"] = true
	} else {
		delete(ub, "stream")
	}
	judgeJSON, err := json.Marshal(ub)
	if err != nil {
		log.Error("combo", "marshal judge body failed", "error", err)
		handlerutil.WriteJSONError(cw, http.StatusInternalServerError, "failed to marshal judge request")
		return
	}

	modelInfo := h.resolveModelEntry(judgeModel)
	if modelInfo == nil {
		handlerutil.WriteJSONError(cw, http.StatusBadGateway, fmt.Sprintf("unresolved judge model: %s", judgeModel))
		return
	}
	h.handleSingleModel(ctx, cw, judgeJSON, modelInfo, isStream, translateResponse)
}

// ResetComboState clears the sticky state for combos
func (h *ChatHandler) ResetComboState(comboName string) {
	h.stickyMu.Lock()
	defer h.stickyMu.Unlock()
	if h.stickyState == nil {
		h.stickyState = make(map[string]*comboStickyState)
	}
	if comboName != "" {
		delete(h.stickyState, comboName)
	} else {
		h.stickyState = make(map[string]*comboStickyState)
	}
}
