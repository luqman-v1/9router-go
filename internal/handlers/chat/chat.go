package chat

import (
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/translator"
	"9router/proxy/internal/updater"
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// HandleChatCompletions handles POST /v1/chat/completions (OpenAI format requests).
func (h *ChatHandler) HandleChatCompletions(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer r.Body.Close()

	var reqBody struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(body, &reqBody); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if reqBody.Model == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing model")
		return
	}

	// Bypass synthetic requests (Claude Code naming, warmup, keepalive)
	if handleBypassRequest(w, body, reqBody.Model, reqBody.Stream) {
		return
	}

	modelInfo, err := h.resolveModel(reqBody.Model)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx := handlerutil.WithSessionID(r.Context(), handlerutil.ExtractSessionID(r))
	ctx = handlerutil.WithClientAnthropicBeta(ctx, r.Header.Get("anthropic-beta"))
	requiredCaps := DetectRequiredCapabilities(body)

	if len(modelInfo.ComboModels) > 0 {
		augmented, comboStrategy, injected := h.applyCapacityAdapter(modelInfo.ComboModels, requiredCaps, modelInfo.Strategy, reqBody.Model)
		if modelInfo.Strategy == "fusion" {
			// Upstream hands the fusion panel the combo's own models, never the
			// augmented list: a capacity-adapter model is a serial fallback, and
			// adding one would silently change the panel composition.
			h.handleFusion(ctx, w, body, modelInfo.ComboModels, modelInfo.Strategy, reqBody.Stream, false, reqBody.Model, modelInfo.StickyLimit, modelInfo.JudgeModel)
			return
		}
		h.handleComboFallback(ctx, w, body, augmented, comboStrategy, reqBody.Stream, false, reqBody.Model, modelInfo.StickyLimit, injected...)
		return
	}

	// Single model request: check if capacity adapter should auto-switch (e.g. vision for image inputs)
	targetEntry := reqBody.Model
	if !strings.Contains(targetEntry, "/") && modelInfo != nil && modelInfo.Provider != "" {
		targetEntry = modelInfo.Provider + "/" + modelInfo.Model
	}
	augmented, strat := h.AugmentModelsWithCapacityAdapter([]string{targetEntry}, requiredCaps)
	if len(augmented) > 1 {
		injected := augmented[:len(augmented)-1]
		log.Info("chat", "capacity adapter auto-switch", "target", reqBody.Model, "switched_to", augmented[0], "caps", keysString(requiredCaps))
		h.handleComboFallback(ctx, w, body, augmented, strat, reqBody.Stream, false, reqBody.Model, 0, injected...)
		return
	}

	h.handleSingleModel(ctx, w, body, modelInfo, reqBody.Stream, false)
}

// applyCapacityAdapter augments a combo's model list with the capacity-adapter
// pool and reports the strategy that must govern the resulting list.
//
// The pool exists precisely because none of the combo's own models can serve
// the request (a text-only combo receiving an image, say), so it is prepended
// and must be tried before the combo's own list. Handing the combo's own
// strategy to the augmented list instead used to fold the adapter model into
// the combo's rotation: with strategy round-robin, combo-wombo (whose only
// entry is oc/space-bunny-free, which reports no vision) alternated every turn
// between its own model and ag/gemini-3.8-flash-high, so traffic to a provider
// absent from the combo appeared to leak out of it. An augmented list is
// therefore governed by the adapter's own strategy; the combo's strategy
// applies only when nothing was injected.
func (h *ChatHandler) applyCapacityAdapter(comboModels []string, required map[string]bool, comboStrategy, requestedModel string) ([]string, string, []string) {
	augmented, adapterStrategy := h.AugmentModelsWithCapacityAdapter(comboModels, required)
	if len(augmented) == len(comboModels) {
		return augmented, comboStrategy, nil
	}
	log.Info("chat", "capacity adapter auto-switch combo",
		"target", requestedModel,
		"switched_to", augmented[0],
		"caps", keysString(required),
		"comboStrategy", comboStrategy,
		"adapterStrategy", adapterStrategy)
	return augmented, adapterStrategy, augmented[:len(augmented)-len(comboModels)]
}

// handleSingleModel resolves a single ModelInfo and forwards the request upstream.
func (h *ChatHandler) handleSingleModel(ctx context.Context, w http.ResponseWriter, body []byte, modelInfo *ModelInfo, isStream bool, translateResponse bool) {
	cw := newCommittedResponseWriter(w)
	var upstreamBody map[string]any
	if err := json.Unmarshal(body, &upstreamBody); err != nil {
		handlerutil.WriteJSONError(cw, http.StatusBadRequest, "failed to parse request body")
		return
	}
	upstreamBody["model"] = modelInfo.Model
	repairToolCallIDsInMap(upstreamBody)
	upstreamJSON, err := json.Marshal(upstreamBody)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to marshal upstream request")
		return
	}

	result := h.handleAccountFallback(ctx, cw, modelInfo.Provider, modelInfo.Model, modelInfo.ConnectionID, upstreamJSON, isStream, translateResponse, "/v1/chat/completions")
	if result != nil {
		if cw.IsCommitted() {
			log.Error("chat", "upstream error after headers committed", "error", result)
			return
		}
		var ue *upstreamError
		if errors.As(result, &ue) {
			cw.Header().Set("Content-Type", "application/json")
			cw.WriteHeader(ue.StatusCode)
			cw.Write(ue.Body)
			return
		}
		handlerutil.WriteJSONError(cw, http.StatusBadGateway, fmt.Sprintf("upstream error: %v", result))
	}
}

// HandleMessages handles POST /v1/messages (Claude format requests).
func (h *ChatHandler) HandleMessages(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Error("chat", "read body failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer r.Body.Close()

	var reqBody struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(body, &reqBody); err != nil {
		log.Error("chat", "parse JSON failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if reqBody.Model == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing model")
		return
	}

	modelInfo, err := h.resolveModel(reqBody.Model)
	if err != nil {
		log.Error("chat", "resolve model failed", "error", err, "model", reqBody.Model)
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	translateResponse := true
	var workingBody map[string]any
	if modelInfo.Provider == "claude" || modelInfo.Provider == "anthropic" {
		translateResponse = false
		body = translator.SanitizeClaudePassthrough(body, translator.ClaudeIntentionalPrefill(body))
		if modelInfo.Provider == "minimax" || modelInfo.Provider == "minimax-cn" {
			body = translator.DefaultClaudeToolType(body)
		}
		body = translator.AnchorClaudeCache(body)
		if err := json.Unmarshal(body, &workingBody); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
	} else {
		openaiBody, err := translator.TranslateClaudeToOpenAI(body)
		if err != nil {
			log.Error("chat", "translate failed", "error", err)
			handlerutil.WriteJSONError(w, http.StatusBadRequest, fmt.Sprintf("translation error: %v", err))
			return
		}
		if err := json.Unmarshal(openaiBody, &workingBody); err != nil {
			log.Error("chat", "parse translated failed", "error", err)
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to parse translated request")
			return
		}
	}
	workingBody["stream"] = reqBody.Stream
	ctx := handlerutil.WithSessionID(r.Context(), handlerutil.ExtractSessionID(r))
	ctx = handlerutil.WithClientAnthropicBeta(ctx, r.Header.Get("anthropic-beta"))
	// Store requested model for streaming echo (PR #3693) and for [1m] marker handling
	ctx = translator.WithRequestedModel(ctx, stripModelContextMarker(reqBody.Model))

	requiredCaps := DetectRequiredCapabilities(body)

	if len(modelInfo.ComboModels) > 0 {
		augmented, comboStrategy, injected := h.applyCapacityAdapter(modelInfo.ComboModels, requiredCaps, modelInfo.Strategy, reqBody.Model)
		if modelInfo.Strategy == "fusion" {
			bodyJSON, err := json.Marshal(workingBody)
			if err != nil {
				handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to marshal request body")
				return
			}
			h.handleFusion(ctx, w, bodyJSON, modelInfo.ComboModels, modelInfo.Strategy, reqBody.Stream, translateResponse, reqBody.Model, modelInfo.StickyLimit, modelInfo.JudgeModel)
			return
		}
		h.handleMessagesComboFallback(ctx, w, workingBody, augmented, comboStrategy, reqBody.Stream, reqBody.Model, modelInfo.StickyLimit, injected...)
		return
	}

	// Single model request: check if capacity adapter should auto-switch (e.g. vision for image inputs)
	targetEntry := reqBody.Model
	if !strings.Contains(targetEntry, "/") && modelInfo != nil && modelInfo.Provider != "" {
		targetEntry = modelInfo.Provider + "/" + modelInfo.Model
	}
	augmented, strat := h.AugmentModelsWithCapacityAdapter([]string{targetEntry}, requiredCaps)
	if len(augmented) > 1 {
		injected := augmented[:len(augmented)-1]
		log.Info("chat", "capacity adapter auto-switch messages", "target", reqBody.Model, "switched_to", augmented[0], "caps", keysString(requiredCaps))
		h.handleMessagesComboFallback(ctx, w, workingBody, augmented, strat, reqBody.Stream, reqBody.Model, 0, injected...)
		return
	}

	h.handleMessagesSingleModel(ctx, w, workingBody, modelInfo, reqBody.Stream, translateResponse)
}

// handleMessagesSingleModel forwards a translated Claude request for a single model.
func (h *ChatHandler) handleMessagesSingleModel(ctx context.Context, w http.ResponseWriter, translatedReq map[string]any, modelInfo *ModelInfo, isStream bool, translateResponse bool) {
	cw := newCommittedResponseWriter(w)
	translatedReq["model"] = modelInfo.Model
	finalBody, err := json.Marshal(translatedReq)
	if err != nil {
		handlerutil.WriteJSONError(cw, http.StatusInternalServerError, "failed to marshal translated request")
		return
	}

	result := h.handleAccountFallback(ctx, cw, modelInfo.Provider, modelInfo.Model, modelInfo.ConnectionID, finalBody, isStream, translateResponse, "/v1/v1/messages")
	if result != nil {
		if cw.IsCommitted() {
			log.Error("chat", "upstream error after headers committed", "error", result)
			return
		}
		var ue *upstreamError
		if errors.As(result, &ue) {
			cw.Header().Set("Content-Type", "application/json")
			cw.WriteHeader(ue.StatusCode)
			cw.Write(ue.Body)
			return
		}
		handlerutil.WriteJSONError(cw, http.StatusBadGateway, fmt.Sprintf("upstream error: %v", result))
	}
}

// HandleHealth responds with a simple health check status.
func (h *ChatHandler) HandleHealth(w http.ResponseWriter, r *http.Request) {
	handlerutil.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// HandleVersion responds with the proxy version details and update status.
func (h *ChatHandler) HandleVersion(w http.ResponseWriter, r *http.Request) {
	info := updater.GetCachedInfo()
	handlerutil.WriteJSON(w, http.StatusOK, info)
}

// HandleVersionStatus responds with the full updater engine status and auto-update configuration.
func (h *ChatHandler) HandleVersionStatus(w http.ResponseWriter, r *http.Request) {
	status := updater.GetStatus()
	handlerutil.WriteJSON(w, http.StatusOK, status)
}

// HandleChangelog serves the CHANGELOG.md file or fetches it from remote with fallbacks.
func (h *ChatHandler) HandleChangelog(w http.ResponseWriter, r *http.Request) {
	candidates := []string{
		"CHANGELOG.md",
		"../CHANGELOG.md",
		"../../CHANGELOG.md",
	}
	for _, path := range candidates {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
			return
		}
	}

	urls := []string{
		"https://raw.githubusercontent.com/luqman-v1/9router-go/main/CHANGELOG.md",
		"https://raw.githubusercontent.com/decolua/9router/refs/heads/master/CHANGELOG.md",
	}
	client := &http.Client{Timeout: 5 * time.Second}
	for _, u := range urls {
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u, nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			data, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr == nil && len(data) > 0 {
				w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(data)
				return
			}
		} else if resp != nil {
			_ = resp.Body.Close()
		}
	}

	handlerutil.WriteJSONError(w, http.StatusNotFound, "changelog not found")
}

// HandleToggleAutoUpdate enables or disables automatic updates in settings and runtime.
func (h *ChatHandler) HandleToggleAutoUpdate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.UnmarshalRead(r.Body, &body); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	updater.SetAutoUpdate(body.Enabled)
	if h.Repo != nil {
		if err := h.Repo.SetAutoUpdate(body.Enabled); err != nil {
			log.Warn("chat", "persist auto-update setting failed", "error", err)
		}
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success":           true,
		"autoUpdateEnabled": body.Enabled,
	})
}

// HandleCheckUpdate fetches fresh update info from remote release server.
func (h *ChatHandler) HandleCheckUpdate(w http.ResponseWriter, r *http.Request) {
	info, err := updater.CheckUpdate(r.Context())
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("check update failed: %v", err))
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, info)
}

// HandleTriggerUpdate performs immediate self-updating if an update is available and restarts gracefully.
func (h *ChatHandler) HandleTriggerUpdate(w http.ResponseWriter, r *http.Request) {
	info, err := updater.CheckUpdate(r.Context())
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("check update failed: %v", err))
		return
	}
	if !info.HasUpdate {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"status":  "up_to_date",
			"message": "9router-go is already on the latest version",
			"version": info.CurrentVersion,
		})
		return
	}
	if err := updater.PerformSelfUpdate(info.DownloadURL, info.SHA256); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, fmt.Sprintf("self-update failed: %v", err))
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"status":  "updated",
		"message": "Update installed successfully. Process is restarting...",
		"version": info.LatestVersion,
	})

	// Schedule process restart shortly after HTTP response is flushed
	go func() {
		time.Sleep(1 * time.Second)
		updater.RestartSelf()
	}()
}

// modelsListModeFromQuery reads the listing scope from the request. Absent
// params keep the upstream default (modeListAll) so existing clients are
// unaffected: `?connected=1` narrows to usable providers, `?all=1` forces the
// full catalog.
func modelsListModeFromQuery(r *http.Request) ModelsListMode {
	q := r.URL.Query()
	if queryFlagEnabled(q.Get("connected")) {
		return modeListConnected
	}
	if queryFlagEnabled(q.Get("all")) {
		return modeListCatalog
	}
	return modeListAll
}

// queryFlagEnabled treats an explicit "1"/"true" as on, matching the loose
// boolean parsing the rest of the dashboard API uses. An absent value is off,
// so `?all` alone (no value) also reads as false and the default applies.
func queryFlagEnabled(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

// HandleModels responds with the list of available model identifiers.
//
// Scope is controlled by query parameters:
//   - default        upstream behaviour: full static catalog on a fresh
//     install, connection-scoped once connections exist
//   - ?connected=1   only providers with an active connection, plus registry
//     noAuth providers — the set a client can actually call
//   - ?all=1         always the full static catalog, connections ignored
//
// The response always carries `mode` and `connections` so a caller can tell a
// candidate catalog from a usable model list.
func (h *ChatHandler) HandleModels(w http.ResponseWriter, r *http.Request) {
	mode := modelsListModeFromQuery(r)
	result := h.buildModelsListResult(r.Context(), mode)
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"object":      "list",
		"data":        result.Models,
		"models":      result.Models,
		"mode":        result.Mode,
		"connections": result.Connections,
	})
}

// HandleModelsInfo returns metadata for a specific model.
// GET /v1/models/info?id={modelId}
func (h *ChatHandler) HandleModelsInfo(w http.ResponseWriter, r *http.Request) {
	modelID := r.URL.Query().Get("id")
	if modelID == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing id query parameter")
		return
	}

	modelInfo, err := h.resolveModel(modelID)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, fmt.Sprintf("model not found: %s", modelID))
		return
	}

	ctxLen, maxOut := h.declaredTokenLimitsFor(modelInfo.Provider, modelID)
	if ctxLen == 0 || maxOut == 0 {
		tableCtxLen, tableMaxOut := providers.GetModelTokenLimits(modelID)
		if ctxLen == 0 {
			ctxLen = tableCtxLen
		}
		if maxOut == 0 {
			maxOut = tableMaxOut
		}
	}

	info := map[string]any{
		"id":                    modelID,
		"object":                "model",
		"owned_by":              modelInfo.Provider,
		"endpoint":              "/v1/chat/completions",
		"context_length":        ctxLen,
		"context_window":        ctxLen,
		"max_completion_tokens": maxOut,
		"max_input_tokens":      ctxLen - maxOut,
		"max_output_tokens":     maxOut,
	}
	if len(modelInfo.ComboModels) > 0 {
		info["combo"] = true
		info["strategy"] = modelInfo.Strategy
		info["models"] = modelInfo.ComboModels
	}

	handlerutil.WriteJSON(w, http.StatusOK, info)
}

// HandleModelsByKind returns models filtered by service kind.
// GET /v1/models/{kind}
func (h *ChatHandler) HandleModelsByKind(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	if kind == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing kind")
		return
	}

	var data []map[string]any
	now := time.Now().Unix()

	endpoint := "/v1/chat/completions"

	for id, cfg := range providers.KnownProviders {
		var match bool
		switch kind {
		case "image":
			match = cfg.ImageURL != ""
		case "tts":
			match = cfg.TTSURL != ""
		case "stt":
			match = cfg.STTURL != ""
		case "systemone":
			match = cfg.SystemoneURL != ""
		case "embedding":
			match = strings.Contains(cfg.BaseURL, "/embeddings")
		case "web":
			match = false
		case "image-to-text":
			match = true
		}
		if !match {
			continue
		}
		if kind == "web" {
			endpoint = "/v1/search"
		}
		if kind == "image" {
			endpoint = "/v1/images/generations"
		}
		if kind == "tts" {
			endpoint = "/v1/audio/speech"
		}
		if kind == "stt" {
			endpoint = "/v1/audio/transcriptions"
		}
		if kind == "embedding" {
			endpoint = "/v1/embeddings"
		}
		if kind == "systemone" {
			endpoint = "/v1/systemone"
		}
		if kind == "image-to-text" {
			endpoint = "/v1/chat/completions"
		}

		data = append(data, map[string]any{
			"id":       id,
			"object":   "model",
			"kind":     kind,
			"owned_by": id,
			"endpoint": endpoint,
			"created":  now,
		})
	}

	if data == nil {
		data = []map[string]any{}
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   data,
	})
}

// HandleModelLookup handles GET /v1/models/* catch-all for both kind filtering and provider/model lookup.
// Port of Next.js src/app/api/v1/models/[...model]/route.js (#3588).
// - GET /v1/models/{kind} -> list filtered by capability (image, tts, stt, embedding, image-to-text, web)
// - GET /v1/models/{provider}/{model} -> single model lookup (e.g. cc/claude-sonnet-5)
func (h *ChatHandler) HandleModelLookup(w http.ResponseWriter, r *http.Request) {
	// Extract suffix after /models
	path := r.URL.Path
	idx := strings.Index(path, "/models")
	if idx == -1 {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid path")
		return
	}
	suffix := strings.TrimPrefix(path[idx+len("/models"):], "/")
	// Handle wildcard "*" when no suffix (chi may pass "*")
	if suffix == "*" || suffix == "" {
		suffix = r.PathValue("*")
		if suffix == "" {
			suffix = r.PathValue("kind")
		}
	}
	// Fallback to chi wildcard or kind param
	if suffix == "" {
		if v := r.PathValue("*"); v != "" {
			suffix = v
		} else if v := r.PathValue("kind"); v != "" {
			suffix = v
		}
	}
	suffix = strings.Trim(suffix, "/")
	if decoded, err := url.PathUnescape(suffix); err == nil {
		suffix = decoded
	}
	// Normalize: handle encoded slash in single segment e.g. "cc%2Fclaude-sonnet-5"
	suffix = strings.TrimSpace(suffix)
	if suffix == "" {
		h.HandleModels(w, r)
		return
	}

	// Known kind slugs
	kindSlugMap := map[string]bool{
		"image":         true,
		"tts":           true,
		"stt":           true,
		"embedding":     true,
		"image-to-text": true,
		"web":           true,
	}
	// If suffix is a known kind without slash, delegate to kind handling
	if kindSlugMap[suffix] && !strings.Contains(suffix, "/") {
		// Reuse HandleModelsByKind logic by setting path value
		r.SetPathValue("kind", suffix)
		h.HandleModelsByKind(w, r)
		return
	}

	// Otherwise treat as provider/model ID lookup.
	if m, ok := h.findModelForLookup(r.Context(), suffix); ok {
		handlerutil.WriteJSON(w, http.StatusOK, m)
		return
	}
	// Also try without provider prefix? No, must be exact.

	handlerutil.WriteJSON(w, http.StatusNotFound, map[string]any{
		"error": map[string]any{
			"message": fmt.Sprintf("The model '%s' does not exist or you do not have access to it.", suffix),
			"type":    "invalid_request_error",
			"code":    "model_not_found",
		},
	})
}

// findModelForLookup resolves a provider/model id against the default list,
// then against the connected-mode list.
//
// The default list is connection-scoped as soon as any connection row exists,
// so a registry noAuth model that /v1/models?connected=1 advertises would 404
// here — the listing endpoint and the lookup endpoint would disagree about
// whether the same model exists. Falling back keeps this route additive in both
// directions: on a fresh install the default list is the full catalog, which
// already contains everything connected mode offers, and on a configured
// install connected mode is the superset. Replacing the lookup outright with
// connected mode would instead make a fresh install stricter, turning the
// credentialed-provider lookups that resolve today into 404s.
func (h *ChatHandler) findModelForLookup(ctx context.Context, modelID string) (ModelInfoObject, bool) {
	if m, ok := findModelByID(h.buildModelsList(ctx), modelID); ok {
		return m, true
	}
	return findModelByID(h.buildModelsListResult(ctx, modeListConnected).Models, modelID)
}

// findModelByID scans the published list for an exact provider/model id.
func findModelByID(data []ModelInfoObject, modelID string) (ModelInfoObject, bool) {
	for _, m := range data {
		if m.ID == modelID {
			return m, true
		}
	}
	return ModelInfoObject{}, false
}

// HandleAudioVoices lists available TTS voices for a provider.
// GET /v1/audio/voices?provider={alias}
func (h *ChatHandler) HandleAudioVoices(w http.ResponseWriter, r *http.Request) {
	provider := r.URL.Query().Get("provider")
	if provider == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing provider query parameter")
		return
	}

	p := resolveProviderAlias(provider)
	cfg, ok := providers.KnownProviders[p]
	if !ok {
		handlerutil.WriteJSONError(w, http.StatusNotFound, fmt.Sprintf("unknown provider: %s", provider))
		return
	}

	voicesURL := cfg.VoicesURL
	if voicesURL == "" && cfg.TTSURL != "" {
		voicesURL = strings.TrimSuffix(cfg.TTSURL, "/text-to-speech") + "/voices"
	}
	if voicesURL == "" {
		handlerutil.WriteJSONError(w, http.StatusNotFound, fmt.Sprintf("no voices endpoint for provider: %s", provider))
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), "GET", voicesURL, nil)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, fmt.Sprintf("create request: %v", err))
		return
	}
	if cfg.AuthHeader != "" && cfg.DefaultAPIKey != "" {
		handlerutil.SetAuthHeader(req, cfg.DefaultAPIKey, cfg.AuthHeader, cfg.AuthScheme)
	}

	resp, err := h.Client.Do(req)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("upstream error: %v", err))
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// HandleCountTokens estimates Anthropic-format token count.
// POST /v1/messages/count_tokens
func (h *ChatHandler) HandleCountTokens(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer r.Body.Close()

	inputTokens := estimateAnthropicTokens(body)
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"input_tokens": inputTokens,
	})
}

// EstimateAnthropicTokens estimates input token count from Claude-format body.
func EstimateAnthropicTokens(body []byte) int {
	return estimateAnthropicTokens(body)
}

// estimateAnthropicTokens estimates input token count from Claude-format body.
// Matches JS estimateAnthropicInputTokens in count_tokens/route.js.
func estimateAnthropicTokens(body []byte) int {
	var msg struct {
		System any   `json:"system"`
		Tools  []any `json:"tools"`
	}
	if err := json.Unmarshal(body, &msg); err != nil {
		return 0
	}

	var totalChars int
	if sysStr, ok := msg.System.(string); ok {
		totalChars += len(sysStr)
	} else if sysArr, ok := msg.System.([]any); ok {
		for _, item := range sysArr {
			totalChars += countValueChars(item)
		}
	}

	var req struct {
		Messages []struct {
			Content any `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err == nil {
		for _, m := range req.Messages {
			totalChars += messageContentChars(m.Content)
		}
	}

	var toolsReq struct {
		Tools []any `json:"tools"`
	}
	if err := json.Unmarshal(body, &toolsReq); err == nil {
		for _, t := range toolsReq.Tools {
			totalChars += countValueChars(t)
		}
	}

	if totalChars == 0 {
		totalChars = len(body)
	}
	return (totalChars + 3) / 4
}

// CountValueChars counts text characters in a generic JSON value.
func CountValueChars(v any) int {
	return countValueChars(v)
}

func countValueChars(v any) int {
	if v == nil {
		return 0
	}
	switch val := v.(type) {
	case string:
		return len(val)
	case []byte:
		return len(val)
	case float64:
		return len(fmt.Sprintf("%v", val))
	case bool:
		if val {
			return 4
		}
		return 5
	case []any:
		n := 0
		for _, item := range val {
			n += countValueChars(item)
		}
		return n
	case map[string]any:
		n := 0
		for k, item := range val {
			n += len(k) + countValueChars(item)
		}
		return n
	}
	return 0
}

func messageContentChars(content any) int {
	if content == nil {
		return 0
	}
	switch c := content.(type) {
	case string:
		return len(c)
	case []any:
		n := 0
		for _, block := range c {
			n += contentBlockChars(block)
		}
		return n
	}
	return countValueChars(content)
}

// MessageContentChars counts characters in a message content field.
func MessageContentChars(msg any) int {
	return messageContentChars(msg)
}

// ContentBlockChars counts characters in a single content block.
func ContentBlockChars(block any) int {
	return contentBlockChars(block)
}

func contentBlockChars(block any) int {
	if block == nil {
		return 0
	}
	m, ok := block.(map[string]any)
	if !ok {
		return countValueChars(block)
	}
	switch m["type"] {
	case "text":
		return countValueChars(m["text"])
	case "tool_use":
		return countValueChars(m["name"]) + countValueChars(m["input"])
	case "tool_result":
		return countValueChars(m["content"])
	case "thinking":
		return countValueChars(m["thinking"])
	default:
		return countValueChars(block)
	}
}

// HandleResponsesCompact marks a Responses request as a compaction and runs it
// down the same pipeline as /v1/responses, matching upstream's route, which
// sets body._compact and reuses handleChat rather than picking another wire
// format. Forcing the body through /v1/chat/completions instead would strip
// the Responses format the client spoke.
func (h *ChatHandler) HandleResponsesCompact(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer r.Body.Close()

	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	m["_compact"] = true
	body, err = json.Marshal(m)
	if err != nil {
		log.Error("chat", "marshal compact body failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to process request")
		return
	}

	newReq, _ := http.NewRequestWithContext(r.Context(), "POST", responsesEndpoint, bytes.NewReader(body))
	newReq.Header = r.Header
	h.HandleResponses(w, newReq)
}

// HandleOllamaChat handles Ollama-compatible /v1/api/chat endpoint.
// POST /v1/api/chat
func (h *ChatHandler) HandleOllamaChat(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer r.Body.Close()

	// Ollama request format is close to OpenAI — forward to chat completions.
	newReq, _ := http.NewRequestWithContext(r.Context(), "POST", "/v1/chat/completions", bytes.NewReader(body))
	newReq.Header = r.Header
	h.HandleChatCompletions(w, newReq)
}

// HandleTestModel handles POST /api/models/test to ping a model.
func (h *ChatHandler) HandleTestModel(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "failed to read body"})
		return
	}
	defer r.Body.Close()

	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.Model == "" {
		handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Model required"})
		return
	}

	payload := map[string]any{
		"model": req.Model,
		"messages": []map[string]string{
			{"role": "user", "content": "hi"},
		},
		"max_tokens": 1024,
		"stream":     false,
	}
	b, _ := json.Marshal(payload)

	testReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(b))
	testReq.Header.Set("Content-Type", "application/json")
	auth := r.Header.Get("Authorization")
	if auth == "" {
		if keys, err := h.Repo.GetApiKeys(); err == nil {
			for _, k := range keys {
				if k.IsActive == 1 && k.Key != "" {
					auth = "Bearer " + k.Key
					break
				}
			}
		}
	}
	if auth != "" {
		testReq.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	h.HandleChatCompletions(rec, testReq)

	if rec.Code == http.StatusOK {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
	} else {
		errMsg := strings.TrimSpace(rec.Body.String())
		var errObj struct {
			Error struct {
				Message string `json:"message"`
				Code    any    `json:"code"`
			} `json:"error"`
			Message string `json:"message"`
		}
		trimmed := errMsg
		if len(trimmed) > 500 {
			head, tail := trimmed[:200], trimmed[len(trimmed)-200:]
			trimmed = head + "\n...[truncated " + strconv.Itoa(len(errMsg)-400) + " bytes]...\n" + tail
		}
		// Non-JSON upstream bodies (proxied error pages, empty SSE) otherwise
		// surface as a bare "response bukan JSON" with no diagnostic tail.
		var probe any
		if json.Unmarshal([]byte(errMsg), &probe) != nil {
			errMsg = trimmed
		} else if json.Unmarshal([]byte(errMsg), &errObj) == nil {
			if errObj.Error.Message != "" {
				errMsg = errObj.Error.Message
			} else if errObj.Message != "" {
				errMsg = errObj.Message
			}
		}

		if rec.Code == http.StatusTooManyRequests && !strings.HasPrefix(errMsg, "429") {
			errMsg = "429: " + errMsg
		} else if rec.Code == http.StatusUnauthorized && !strings.HasPrefix(errMsg, "401") {
			errMsg = "401: " + errMsg
		} else if rec.Code == http.StatusServiceUnavailable && !strings.HasPrefix(errMsg, "503") {
			errMsg = "503: " + errMsg
		} else if rec.Code == http.StatusNotFound && !strings.HasPrefix(errMsg, "404") {
			errMsg = "404: " + errMsg
		}
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"ok": false, "error": errMsg})
	}
}
