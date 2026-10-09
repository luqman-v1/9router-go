package chat

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/translator"
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
			Role    string         `json:"role"`
			Content jsontext.Value `json:"content"`
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
func contentHasText(content jsontext.Value) bool {
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

	return capabilityFlag(providers.GetCapabilitiesForModel(provider, modelEntry), cap)
}

// DetectRequiredCapabilities extracts the capabilities a request needs.
//
// Modalities (vision/pdf/audioInput/videoInput) are scanned only on the current
// user turn — the trailing run after the last assistant/model message, which
// may span several messages. Media in older turns must not pin the combo to a
// vision model; that history is stripped or placeholdered downstream.
// "tools" is request-wide because it lives in the top-level tools array.
//
// Port of upstream detectRequiredCapabilities (open-sse/services/combo.js).
func DetectRequiredCapabilities(body []byte) map[string]bool {
	required := make(map[string]bool)

	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return required
	}

	// OpenAI / Claude / Hermes / Ollama.
	for _, msg := range trailingUserItems(m["messages"]) {
		scanMessage(msg, required)
	}
	// Responses API.
	for _, item := range trailingUserItems(m["input"]) {
		scanContentBlocks(itemContent(item), required)
	}
	// Gemini / Antigravity.
	contents, _ := m["contents"].([]any)
	if len(contents) == 0 {
		if req, ok := m["request"].(map[string]any); ok {
			contents, _ = req["contents"].([]any)
		}
	}
	for _, c := range trailingUserItems(contents) {
		scanContentBlocks(itemParts(c), required)
	}

	if tools, ok := m["tools"].([]any); ok {
		for _, t := range tools {
			tm, ok := t.(map[string]any)
			if !ok {
				continue
			}
			if tm["type"] == "function" || tm["function"] != nil || tm["functionDeclarations"] != nil {
				required["tools"] = true
				break
			}
		}
	}

	return required
}

// isAssistantRole reports whether a message is a model turn, which ends the
// current user run.
func isAssistantRole(role string) bool {
	return role == "assistant" || role == "model"
}

// trailingUserItems returns the trailing run of items after the last
// assistant/model entry — the current user turn. It may span several messages
// (text + image split across blocks).
func trailingUserItems(arr any) []any {
	list, ok := arr.([]any)
	if !ok || len(list) == 0 {
		return nil
	}
	i := len(list) - 1
	for i >= 0 {
		role := ""
		if m, ok := list[i].(map[string]any); ok {
			role, _ = m["role"].(string)
		}
		if isAssistantRole(role) {
			break
		}
		i--
	}
	return list[i+1:]
}

// itemParts reads a Gemini content entry's parts field.
func itemParts(item any) any {
	if m, ok := item.(map[string]any); ok {
		return m["parts"]
	}
	return nil
}

// scanMessage checks one message for capability requirements, covering the
// shapes that carry media outside the standard content array.
func scanMessage(msg any, required map[string]bool) {
	m, ok := msg.(map[string]any)
	if !ok {
		return
	}

	// Ollama / Hermes images array.
	if images, ok := m["images"].([]any); ok && len(images) > 0 {
		required["vision"] = true
	}

	// Vercel AI SDK / Hermes attachments / experimental_attachments.
	attachments, _ := m["experimental_attachments"].([]any)
	if len(attachments) == 0 {
		attachments, _ = m["attachments"].([]any)
	}
	for _, raw := range attachments {
		att, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		mime := firstString(att, "contentType", "mediaType")
		if mime == "" {
			if url, ok := att["url"].(string); ok {
				mime = dataURIMime(url)
			}
		}
		if mime != "" {
			addByMime(required, mime)
		} else if att["url"] != nil || att["data"] != nil {
			required["vision"] = true
		}
	}

	// Direct message-level modality properties.
	if m["image_url"] != nil || m["image"] != nil {
		required["vision"] = true
	}
	if m["audio_url"] != nil || m["audio"] != nil {
		required["audioInput"] = true
	}

	scanContentBlocks(m["content"], required)

	// Data URIs embedded in plain string content.
	if s, ok := m["content"].(string); ok {
		switch {
		case strings.Contains(s, "data:image/"):
			required["vision"] = true
		case strings.Contains(s, "data:audio/"):
			required["audioInput"] = true
		case strings.Contains(s, "data:application/pdf"):
			required["pdf"] = true
		}
	}
}

// scanContentBlocks walks a content array.
func scanContentBlocks(content any, required map[string]bool) {
	blocks, ok := content.([]any)
	if !ok {
		return
	}
	for _, b := range blocks {
		scanContentBlock(b, required)
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
	case "input_audio", "audio_url", "audio":
		required["audioInput"] = true
	case "input_video", "video_url", "video":
		required["videoInput"] = true
	case "file", "document", "input_file":
		// Infer the modality from an embedded mime when available; fall back
		// to pdf for a generic file.
		if mime := fileBlockMime(b); mime != "" {
			addByMime(required, mime)
		} else {
			required["pdf"] = true
		}
	}

	// Gemini parts carry a mime on inlineData / fileData.
	if inner, ok := b["inlineData"].(map[string]any); ok {
		addByMime(required, stringOf(inner, "mimeType"))
	}
	if inner, ok := b["fileData"].(map[string]any); ok {
		addByMime(required, stringOf(inner, "mimeType"))
	}
}

// fileBlockMime digs a mime type out of the several shapes a file block uses
// to carry its payload.
func fileBlockMime(b map[string]any) string {
	if ia, ok := b["input_audio"].(map[string]any); ok {
		if format := stringOf(ia, "format"); format != "" {
			return "audio/" + format
		}
	}
	if file, ok := b["file"].(map[string]any); ok {
		if data := stringOf(file, "file_data"); data != "" {
			return dataURIMime(data)
		}
	}
	if source, ok := b["source"].(map[string]any); ok {
		if mime := stringOf(source, "media_type"); mime != "" {
			return mime
		}
		if data := stringOf(source, "data"); data != "" {
			return dataURIMime(data)
		}
	}
	return ""
}

// addByMime maps a mime type to its capability. An empty or unknown mime is
// ignored so an unrelated content type does not pin the combo.
func addByMime(required map[string]bool, mime string) {
	switch {
	case mime == "":
		return
	case strings.HasPrefix(mime, "image/"):
		required["vision"] = true
	case mime == "application/pdf":
		required["pdf"] = true
	case strings.HasPrefix(mime, "audio/"):
		required["audioInput"] = true
	case strings.HasPrefix(mime, "video/"):
		required["videoInput"] = true
	}
}

// dataURIMime extracts the mime from a "data:<mime>;base64,..." URL.
func dataURIMime(url string) string {
	rest, ok := strings.CutPrefix(url, "data:")
	if !ok {
		return ""
	}
	mime, _, ok := strings.Cut(rest, ",")
	if !ok {
		return ""
	}
	mime, _, _ = strings.Cut(mime, ";")
	return mime
}

// firstString returns the first key that holds a string value.
func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v := stringOf(m, k); v != "" {
			return v
		}
	}
	return ""
}

// stringOf reads a string field, returning "" when absent or another type.
func stringOf(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

// ReorderByCapabilities stably floats models satisfying constraints to the
// front. Never drops a model, so the fallback chain stays intact.
//
// Tier 0: satisfies all hard AND all soft capabilities.
// Tier 1: satisfies every hard capability.
// Tier 2: the rest.
//
// Port of upstream reorderByCapabilities (open-sse/services/combo.js).
func ReorderByCapabilities(comboModels []string, required map[string]bool) []string {
	if len(required) == 0 || len(comboModels) <= 1 {
		return comboModels
	}

	hard := requiredHardCaps(required)
	soft := make([]string, 0, len(required))
	for cap := range required {
		if !slices.Contains(adapterCapabilityKeys, cap) {
			soft = append(soft, cap)
		}
	}

	tier := make(map[string]int, len(comboModels))
	order := make([]string, len(comboModels))
	for i, m := range comboModels {
		tier[m] = capabilityTier(m, hard, soft)
		order[i] = m
	}

	sort.SliceStable(order, func(i, j int) bool { return tier[order[i]] < tier[order[j]] })
	return order
}

// capabilityTier scores one model entry: 0 covers hard+soft, 1 covers hard
// only, 2 covers neither.
func capabilityTier(modelEntry string, hard, soft []string) int {
	caps := modelEntryCaps(modelEntry)
	for _, c := range hard {
		if !capabilityFlag(caps, c) {
			return 2
		}
	}
	for _, c := range soft {
		if !capabilityFlag(caps, c) {
			return 1
		}
	}
	return 0
}

// modelEntryCaps resolves the capability block for a "provider/model" entry.
func modelEntryCaps(modelEntry string) providers.Capabilities {
	provider, model, _ := strings.Cut(modelEntry, "/")
	return providers.GetCapabilitiesForModel(provider, model)
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
	case "round-robin", "roundrobin":
		if stickyLimit <= 0 {
			stickyLimit = 1
		}
		fallthrough
	case "sticky":
		if stickyLimit <= 0 {
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
	return strings.Join(slices.Sorted(maps.Keys(m)), ",")
}

// handleComboFallback iterates through combo model entries, trying each one.
// Auto-capability-switch: floats vision/pdf-capable models to the front.
// adapterModels holds the capacity-adapter entries injected for this request;
// their history is trimmed to their context window before forwarding.
func (h *ChatHandler) handleComboFallback(ctx context.Context, w http.ResponseWriter, body []byte, comboModels []string, strategy string, isStream bool, translateResponse bool, comboName string, stickyLimit int, adapterModels ...string) {
	cw := newCommittedResponseWriter(w)
	var lastErr *upstreamError
	var retry passRetry
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
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			wait := retry.wait()
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
			retry.reset()
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
				var picked *ProviderConnection
				var connData *ConnectionData
				isKnownNoAuth := false
				if cfg, ok := providers.KnownProviders[modelInfo.Provider]; ok && (cfg.NoAuth || cfg.DefaultAPIKey != "") {
					isKnownNoAuth = true
					connData = &ConnectionData{
						APIKey:      cfg.DefaultAPIKey,
						ProxyPoolID: h.ResolveProviderProxyPoolID(modelInfo.Provider),
					}
				} else {
					var cData *ConnectionData
					var err error
					picked, cData, err = h.getBestConnectionWithContext(ctx, modelInfo.Provider, modelInfo.ConnectionID, excludeIDs, modelInfo.Model)
					if err != nil {
						break
					}
					connID = picked.ID
					connData = cData
				}
				// Skip a connection that is already locked for this model
				if connID != "" {
					lockKey := canonicalLockModel(modelInfo.Provider, modelInfo.Model)
					locked, _ := h.Repo.IsConnectionModelLocked(connID, lockKey)
					if !locked && lockKey != modelInfo.Model {
						locked, _ = h.Repo.IsConnectionModelLocked(connID, modelInfo.Model)
					}
					if locked {
						log.Warn("combo", "skip locked connection", "provider", modelInfo.Provider, "model", modelInfo.Model, "lockKey", lockKey, "conn", connID)
						excludeIDs = append(excludeIDs, connID)
						continue
					}
				}

				var upstreamBody map[string]any
				upstreamBodyJSON := adapterTrimmedBody(body, entry, adapterModels)

				if err := json.Unmarshal(upstreamBodyJSON, &upstreamBody); err != nil {
					handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to parse request body")
					return
				}
				upstreamBody["model"] = modelInfo.Model
				repairToolCallIDsInMap(upstreamBody)
				upstreamJSON, err := json.Marshal(upstreamBody)
				if err != nil {
					break
				}

				var fwdErr error
				if modelInfo.Provider == "mimo-free" {
					comboMetrics := &streamMetrics{}
					fwdErr = h.MimoFreeChat(ctx, cw, upstreamJSON, isStream, comboMetrics)
				} else {
					fwdErr = h.tryForwardWithConnection(forwardRequestParams{
						Ctx: ctx, W: cw, Provider: modelInfo.Provider, Model: modelInfo.Model,
						RequestedModel: comboName, ComboName: comboName,
						ConnectionID: connID, ConnName: connObjName(picked), ConnEmail: connObjEmail(picked),
						ConnData: connData, Body: upstreamJSON,
						IsStream: isStream, TranslateResponse: translateResponse,
						Endpoint: forwardEndpoint(ctx, "/v1/chat/completions"),
					})
				}

				if fwdErr != nil {
					if ctx.Err() != nil {
						lastErr = &upstreamError{StatusCode: StatusClientClosedRequest, Body: []byte(`{"error":{"message":"client closed request","type":"client_closed_request","code":499}}`)}
						break
					}
					if isGuardrailBlock(fwdErr) {
						// A policy refusal is final: every other connection and
						// model in the combo would receive the same prompt and
						// answer with the same refused content, so continuing
						// would spread it across every provider the combo names.
						log.Warn("combo", "guardrail block, stopping turn", "provider", modelInfo.Provider, "conn", connID)
						var ue *upstreamError
						errors.As(fwdErr, &ue)
						lastErr = ue
						break
					}
					var ue *upstreamError
					if errors.As(fwdErr, &ue) {
						// A 410 naming a dead model is a routing fact, not an account
						// fault: every connection to this provider answers the same
						// way. Record it, then leave this entry for the next combo
						// model (#179).
						//
						// The connection is deliberately NOT added to excludeIDs:
						// that list spans the whole pass, so excluding it here would
						// starve every later entry on a single-account provider —
						// exactly the case the failover exists to rescue. The model
						// lock recordModelDeprecation writes is per provider/model,
						// which is the scope that actually matters.
						if providers.IsModelDeprecation(ue.StatusCode, ue.Body) {
							h.recordModelDeprecation(ctx, modelInfo.Provider, modelInfo.Model, connID, ue)
							retry.note(ue)
							lastErr = ue
							break
						}
						if providers.RetryableStatusCodes[ue.StatusCode] {
							h.comboLockRetryable(ctx, &excludeIDs, connID, modelInfo.Provider, modelInfo.Model, ue)
						}
						if ue.StatusCode == http.StatusServiceUnavailable || ue.StatusCode == http.StatusBadGateway || ue.StatusCode == http.StatusGatewayTimeout {
							// In combo loops, fail over immediately to the next connection/model without blocking the client turn
							log.Info("combo", "transient failover skip", "status", ue.StatusCode, "provider", modelInfo.Provider, "conn", connID)
						}
						retry.note(ue)
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
			if isGuardrailBlock(lastErr) {
				// A policy refusal ends the whole turn, not just this entry: the
				// next model would be sent the same prompt and answer with the
				// same refused content.
				break
			}
		}
		if isGuardrailBlock(lastErr) {
			break
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
		retry.writeError(cw, lastErr)
		return
	}
	if cw.IsCommitted() {
		return
	}
	handlerutil.WriteJSONError(cw, http.StatusBadGateway, "all combo models failed: no valid entries")
}

// handleMessagesComboFallback iterates through combo models for the Claude endpoint.
// Auto-capability-switch: floats vision/pdf-capable models to the front.
// adapterModels holds the capacity-adapter entries injected for this request;
// their history is trimmed to their context window before forwarding.
func (h *ChatHandler) handleMessagesComboFallback(ctx context.Context, w http.ResponseWriter, translatedReq map[string]any, comboModels []string, strategy string, isStream bool, comboName string, stickyLimit int, adapterModels ...string) {
	cw := newCommittedResponseWriter(w)
	var lastErr *upstreamError
	var retry passRetry

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
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			wait := retry.wait()
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
			retry.reset()
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
				var picked *ProviderConnection
				var connData *ConnectionData
				isKnownNoAuth := false
				if cfg, ok := providers.KnownProviders[modelInfo.Provider]; ok && (cfg.NoAuth || cfg.DefaultAPIKey != "") {
					isKnownNoAuth = true
					connData = &ConnectionData{
						APIKey:      cfg.DefaultAPIKey,
						ProxyPoolID: h.ResolveProviderProxyPoolID(modelInfo.Provider),
					}
				} else {
					var cData *ConnectionData
					var err error
					picked, cData, err = h.getBestConnectionWithContext(ctx, modelInfo.Provider, modelInfo.ConnectionID, excludeIDs, modelInfo.Model)
					if err != nil {
						break
					}
					connID = picked.ID
					connData = cData
				}
				if connID != "" {
					lockKey := canonicalLockModel(modelInfo.Provider, modelInfo.Model)
					locked, _ := h.Repo.IsConnectionModelLocked(connID, lockKey)
					if !locked && lockKey != modelInfo.Model {
						locked, _ = h.Repo.IsConnectionModelLocked(connID, modelInfo.Model)
					}
					if locked {
						log.Warn("combo", "skip locked connection", "provider", modelInfo.Provider, "model", modelInfo.Model, "lockKey", lockKey, "conn", connID)
						excludeIDs = append(excludeIDs, connID)
						continue
					}
				}

				entryBody, err := adapterTrimmedMap(translatedReq, entry, adapterModels)
				if err != nil {
					handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to parse request body")
					return
				}
				entryBody["model"] = modelInfo.Model

				upstreamJSON, err := json.Marshal(entryBody)
				if err != nil {
					break
				}

				fwdErr := h.tryForwardWithConnection(forwardRequestParams{
					Ctx: ctx, W: cw, Provider: modelInfo.Provider, Model: modelInfo.Model,
					RequestedModel: comboName, ComboName: comboName,
					ConnectionID: connID, ConnName: connObjName(picked), ConnEmail: connObjEmail(picked),
					ConnData: connData, Body: upstreamJSON,
					IsStream: isStream, TranslateResponse: !translator.IsResponsesClient(ctx),
					Endpoint: forwardEndpoint(ctx, "/v1/messages"),
				})

				if fwdErr != nil {
					if ctx.Err() != nil {
						lastErr = &upstreamError{StatusCode: StatusClientClosedRequest, Body: []byte(`{"error":{"message":"client closed request","type":"client_closed_request","code":499}}`)}
						break
					}
					if isGuardrailBlock(fwdErr) {
						// See the chat branch above: a policy refusal stops the
						// turn instead of being replayed against the next model.
						log.Warn("combo", "guardrail block, stopping turn", "provider", modelInfo.Provider, "conn", connID)
						var blocked *upstreamError
						errors.As(fwdErr, &blocked)
						lastErr = blocked
						break
					}
					var ue *upstreamError
					if errors.As(fwdErr, &ue) {
						// Same contract as the OpenAI loop: a 410 naming a dead
						// model is a provider-wide fact, so it is recorded and
						// the next combo entry is tried (#179). The connection
						// stays out of excludeIDs — see the OpenAI loop: that
						// list spans the pass, and a single-account provider
						// would have nothing left to fail over to.
						if providers.IsModelDeprecation(ue.StatusCode, ue.Body) {
							h.recordModelDeprecation(ctx, modelInfo.Provider, modelInfo.Model, connID, ue)
							retry.note(ue)
							lastErr = ue
							break
						}
						if providers.RetryableStatusCodes[ue.StatusCode] {
							h.comboLockRetryable(ctx, &excludeIDs, connID, modelInfo.Provider, modelInfo.Model, ue)
						}
						if ue.StatusCode == http.StatusServiceUnavailable || ue.StatusCode == http.StatusBadGateway || ue.StatusCode == http.StatusGatewayTimeout {
							// In combo loops, fail over immediately to the next connection/model without blocking the client turn
							log.Info("combo", "transient failover skip", "status", ue.StatusCode, "provider", modelInfo.Provider, "conn", connID)
						}
						retry.note(ue)
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
			if isGuardrailBlock(lastErr) {
				// See handleComboFallback: a policy refusal ends the turn.
				break
			}
		}
		if isGuardrailBlock(lastErr) {
			break
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
		retry.writeError(cw, lastErr)
		return
	}
	if cw.IsCommitted() {
		return
	}
	handlerutil.WriteJSONError(cw, http.StatusBadGateway, "all combo models failed: no valid entries")
}

// comboLockRetryable classifies a retryable upstream error in a combo loop and
// locks the failed connection+model so the exponential backoff persists across
// requests. The combo path iterates tryForwardWithConnection directly and used
// to skip this entirely — so a 429 never set a lock, every request re-tried all
// combo models on the same account, and Google rate-limited it forever. The
// connection is also appended to excludeIDs so the remaining combo models in
// this request skip it instead of re-hitting the same quota bucket.
func (h *ChatHandler) comboLockRetryable(ctx context.Context, excludeIDs *[]string, connID, provider, model string, ue *upstreamError) {
	if handlerutil.IsProbeContext(ctx) {
		*excludeIDs = append(*excludeIDs, connID)
		return
	}
	if connID == "" {
		return
	}
	currentLevel := h.Repo.GetConnectionBackoffLevel(connID)
	cls := providers.ClassifyError(ue.StatusCode, extractErrorText(ue.Body), currentLevel)
	// Upstream parity (checkFallbackError): request-scoped 4xx (400/404/405/
	// 409/422/...) must not lock the account — the bug is in the request,
	// not the credential. Only lock when ShouldFallback is set.
	if !cls.ShouldFallback || cls.CooldownMs <= 0 {
		return
	}
	cooldownSec := retryableCooldownSec(ue.StatusCode, time.Duration(cls.CooldownMs)*time.Millisecond, ue)
	lockKey := canonicalLockModel(provider, model)
	if err := h.Repo.LockConnectionModel(connID, lockKey, cooldownSec, cls.NewBackoffLevel); err != nil {
		log.Warn("combo", "lock failed", "conn", connID, "provider", provider, "model", lockKey, "error", err)
	}
	if lockKey != model {
		_ = h.Repo.LockConnectionModel(connID, model, cooldownSec, cls.NewBackoffLevel)
	}
	// Account-scoped cooldown alongside the per-model locks, so the selector
	// can skip this account before spending a request (upstream applyErrorState).
	// Only lock if error is account-scoped, not model-scoped (e.g. 401 auth issues),
	// so unrelated models stay available.
	if isModelScopedError(ue.StatusCode, extractErrorText(ue.Body), model) {
		if recErr := h.Repo.RecordConnectionScopedError(connID, model, "chat", ue.StatusCode, extractErrorText(ue.Body), cls.NewBackoffLevel); recErr != nil {
			log.Warn("combo", "record connection error failed", "conn", connID, "error", recErr)
		}
	} else {
		until := time.Now().UTC().Add(time.Duration(cooldownSec) * time.Second)
		if err := h.Repo.LockConnectionRateLimit(connID, until, cls.NewBackoffLevel, ue.StatusCode, extractErrorText(ue.Body)); err != nil {
			log.Warn("combo", "rate limit lock failed", "conn", connID, "error", err)
		}
	}
	*excludeIDs = append(*excludeIDs, connID)
	log.Warn("combo", "locked on retryable error", "provider", provider, "model", model, "lockKey", lockKey, "conn", connID, "status", ue.StatusCode, "cooldown_s", cooldownSec)
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
func (h *ChatHandler) handleFusion(ctx context.Context, w http.ResponseWriter, body []byte, comboModels []string, strategy string, isStream bool, translateResponse bool, comboName string, stickyLimit int, customJudgeModel string) {
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

	// Fan-out panel calls (ctx aborts stragglers the panel moves on without).
	ft := fusionDefaults
	calls := make([]func(context.Context) *fusionResult, len(panel))
	for i, entry := range panel {
		calls[i] = h.makePanelCall(panelJSON, entry)
	}

	settled := collectPanel(ctx, calls, ft)

	// Extract successful answers
	var answers []fusionAnswer
	judgeModel := panel[0] // default: first panel model
	if customJudgeModel != "" {
		judgeModel = customJudgeModel
	}
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
