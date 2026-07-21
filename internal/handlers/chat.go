package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/translator"
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

	modelInfo, err := h.resolveModel(reqBody.Model)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	if len(modelInfo.ComboModels) > 0 {
		if modelInfo.Strategy == "fusion" {
			h.handleFusion(w, body, modelInfo.ComboModels, modelInfo.Strategy, reqBody.Stream, false)
			return
		}
		h.handleComboFallback(w, body, modelInfo.ComboModels, modelInfo.Strategy, reqBody.Stream, false)
		return
	}

	h.handleSingleModel(w, body, modelInfo, reqBody.Stream, false)
}

// handleSingleModel resolves a single ModelInfo and forwards the request upstream.
func (h *ChatHandler) handleSingleModel(w http.ResponseWriter, body []byte, modelInfo *ModelInfo, isStream bool, translateResponse bool) {
	var upstreamBody map[string]any
	if err := json.Unmarshal(body, &upstreamBody); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to parse request body")
		return
	}
	upstreamBody["model"] = modelInfo.Model

	upstreamJSON, err := json.Marshal(upstreamBody)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to marshal upstream request")
		return
	}

	result := h.handleAccountFallback(w, modelInfo.Provider, modelInfo.Model, modelInfo.ConnectionID, upstreamJSON, isStream, translateResponse, "/v1/chat/completions")
	if result != nil {
		var ue *upstreamError
		if errors.As(result, &ue) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(ue.StatusCode)
			w.Write(ue.Body)
			return
		}
		handlerutil.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("upstream error: %v", result))
	}
}

// HandleMessages handles POST /v1/messages (Claude format requests).
func (h *ChatHandler) HandleMessages(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("[error] component=messages err=\"read body: %v\"", err)
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer r.Body.Close()

	var reqBody struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(body, &reqBody); err != nil {
		log.Printf("[error] component=messages err=\"parse JSON: %v\"", err)
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if reqBody.Model == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing model")
		return
	}

	modelInfo, err := h.resolveModel(reqBody.Model)
	if err != nil {
		log.Printf("[error] component=messages err=\"resolve model: %v\" model=%s", err, reqBody.Model)
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	translateResponse := true
	var workingBody map[string]any
	if modelInfo.Provider == "claude" || modelInfo.Provider == "anthropic" {
		translateResponse = false
		if err := json.Unmarshal(body, &workingBody); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
	} else {
		openaiBody, err := translator.TranslateClaudeToOpenAI(body)
		if err != nil {
			log.Printf("[error] component=messages err=\"translate: %v\"", err)
			handlerutil.WriteJSONError(w, http.StatusBadRequest, fmt.Sprintf("translation error: %v", err))
			return
		}
		if err := json.Unmarshal(openaiBody, &workingBody); err != nil {
			log.Printf("[error] component=messages err=\"parse translated: %v\"", err)
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to parse translated request")
			return
		}
	}
	workingBody["stream"] = reqBody.Stream

	if len(modelInfo.ComboModels) > 0 {
		if modelInfo.Strategy == "fusion" {
			bodyJSON, err := json.Marshal(workingBody)
			if err != nil {
				handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to marshal request body")
				return
			}
			h.handleFusion(w, bodyJSON, modelInfo.ComboModels, modelInfo.Strategy, reqBody.Stream, translateResponse)
			return
		}
		h.handleMessagesComboFallback(w, workingBody, modelInfo.ComboModels, modelInfo.Strategy, reqBody.Stream)
		return
	}

	h.handleMessagesSingleModel(w, workingBody, modelInfo, reqBody.Stream, translateResponse)
}

// handleMessagesSingleModel forwards a translated Claude request for a single model.
func (h *ChatHandler) handleMessagesSingleModel(w http.ResponseWriter, translatedReq map[string]any, modelInfo *ModelInfo, isStream bool, translateResponse bool) {
	translatedReq["model"] = modelInfo.Model
	finalBody, err := json.Marshal(translatedReq)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to marshal translated request")
		return
	}

	result := h.handleAccountFallback(w, modelInfo.Provider, modelInfo.Model, modelInfo.ConnectionID, finalBody, isStream, translateResponse, "/v1/v1/messages")
	if result != nil {
		var ue *upstreamError
		if errors.As(result, &ue) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(ue.StatusCode)
			w.Write(ue.Body)
			return
		}
		handlerutil.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("upstream error: %v", result))
	}
}

// HandleHealth responds with a simple health check status.
func (h *ChatHandler) HandleHealth(w http.ResponseWriter, r *http.Request) {
	handlerutil.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// HandleVersion responds with the proxy version.
func (h *ChatHandler) HandleVersion(w http.ResponseWriter, r *http.Request) {
	handlerutil.WriteJSON(w, http.StatusOK, map[string]string{"version": "9router-go/1.0.0"})
}

// HandleModels responds with the list of available model identifiers from the DB.
func (h *ChatHandler) HandleModels(w http.ResponseWriter, r *http.Request) {
	type modelObj struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}

	var data []modelObj
	now := time.Now().Unix()

	aliases, err := h.Repo.GetModelAliases()
	if err == nil {
		for alias := range aliases {
			data = append(data, modelObj{
				ID:      alias,
				Object:  "model",
				Created: now,
				OwnedBy: "system",
			})
		}
	}

	combos, err := h.Repo.GetCombos()
	if err == nil {
		for _, c := range combos {
			data = append(data, modelObj{
				ID:      c.Name,
				Object:  "model",
				Created: now,
				OwnedBy: "system",
			})
		}
	}

	if data == nil {
		data = []modelObj{}
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   data,
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

	info := map[string]any{
		"id":       modelID,
		"object":   "model",
		"owned_by": modelInfo.Provider,
		"endpoint": "/v1/chat/completions",
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

	req, err := http.NewRequest("GET", voicesURL, nil)
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

func countValueChars(v any) int {
	if v == nil {
		return 0
	}
	switch val := v.(type) {
	case string:
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

// HandleResponsesCompact forwards to chat handler with compact flag.
// POST /v1/responses/compact
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
		log.Printf("[chat] failed to marshal compact request body: %v", err)
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to process request")
		return
	}

	newReq, _ := http.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	newReq.Header = r.Header
	h.HandleChatCompletions(w, newReq)
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
	newReq, _ := http.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	newReq.Header = r.Header
	h.HandleChatCompletions(w, newReq)
}

// SetupRoutes mounts the chat handler routes on the provided chi router.
func SetupRoutes(r interface {
	Get(pattern string, handlerFn http.HandlerFunc)
	Post(pattern string, handlerFn http.HandlerFunc)
}, repo *db.Repo, ts *TokenSaverConfig) {
	handler := NewChatHandler(repo, ts)

	r.Get("/version", handler.HandleVersion)
	r.Get("/models", handler.HandleModels)
	r.Get("/models/info", handler.HandleModelsInfo)
	r.Get("/models/{kind}", handler.HandleModelsByKind)
	r.Post("/chat/completions", handler.HandleChatCompletions)
	r.Post("/messages", handler.HandleMessages)
	r.Post("/messages/count_tokens", handler.HandleCountTokens)
	r.Post("/embeddings", handler.HandleEmbeddings)
	r.Post("/responses", handler.HandleResponses)
	r.Post("/responses/compact", handler.HandleResponsesCompact)
	r.Post("/images/generations", handler.HandleImages)
	r.Post("/audio/speech", handler.HandleAudioSpeech)
	r.Get("/audio/voices", handler.HandleAudioVoices)
	r.Post("/audio/transcriptions", handler.HandleAudioTranscriptions)
	r.Post("/api/oauth/{provider}/import", handler.HandleOAuthImport)
	r.Get("/api/oauth/kiro/social-authorize", handler.HandleOAuthKiroSocialAuthorize)
	r.Post("/api/oauth/kiro/social-exchange", handler.HandleOAuthKiroSocialExchange)
	r.Post("/api/oauth/codex/bulk-import", handler.HandleOAuthCodexBulkImport)
	r.Post("/videos/generations", handler.HandleVideoGenerations)
	r.Post("/videos/edits", handler.HandleVideoEdits)
	r.Post("/videos/extensions", handler.HandleVideoExtensions)
	r.Get("/videos/{id}", handler.HandleVideoGet)
	r.Post("/search", handler.HandleSearch)
	r.Post("/scrape", handler.HandleScrape)
	r.Post("/web/fetch", handler.HandleWebFetch)
	r.Post("/api/chat", handler.HandleOllamaChat)
}
