package media

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/handlers/shared"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
)

// MediaHandler handles embeddings, responses, audio, video, image, and web tool endpoints.
type MediaHandler struct {
	Repo       *db.Repo
	Client     *http.Client
	TokenSaver *shared.TokenSaverConfig
	ChatH      *chat.ChatHandler
}

// NewMediaHandler creates a MediaHandler instance.
func NewMediaHandler(repo *db.Repo, ts *shared.TokenSaverConfig, chatH *chat.ChatHandler) *MediaHandler {
	return &MediaHandler{
		Repo:       repo,
		Client:     &http.Client{Timeout: 0},
		TokenSaver: ts,
		ChatH:      chatH,
	}
}

// HandleEmbeddings forwards /v1/embeddings requests to upstream providers.
func (h *MediaHandler) HandleEmbeddings(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer r.Body.Close()

	var reqBody struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &reqBody); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if reqBody.Model == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing model")
		return
	}

	modelInfo, err := h.ChatH.ResolveModel(reqBody.Model)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	conn, connData, err := h.ChatH.GetBestConnection(modelInfo.Provider, modelInfo.ConnectionID, nil, modelInfo.Model)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, err.Error())
		return
	}

	providerCfg, err := h.ChatH.GetProviderConfig(modelInfo.Provider, connData)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	apiKey := chat.ExtractAPIKey(connData)
	if apiKey == "" {
		handlerutil.WriteJSONError(w, http.StatusUnauthorized, "no API key found")
		return
	}

	embeddingsURL := buildEmbeddingsURL(providerCfg.BaseURL)
	finalBody := handlerutil.UpdateModelInBody(body, modelInfo.Model)

	req, err := http.NewRequestWithContext(r.Context(), "POST", embeddingsURL, strings.NewReader(string(finalBody)))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, fmt.Sprintf("create request: %v", err))
		return
	}

	req.Header.Set(constants.HeaderContentType, constants.ContentTypeJSON)
	handlerutil.SetAuthHeader(req, apiKey, providerCfg.AuthHeader, providerCfg.AuthScheme)

	start := time.Now()
	resp, err := h.Client.Do(req)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("upstream error: %v", err))
		return
	}
	defer resp.Body.Close()

	w.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)

	if resp.StatusCode == http.StatusOK {
		h.Repo.UpdateConnectionLastUsed(conn.ID)
		latencyMs := time.Since(start).Milliseconds()
		logInfo := &shared.UsageLogInfo{
			Provider:     modelInfo.Provider,
			Model:        modelInfo.Model,
			ConnectionID: conn.ID,
			APIKey:       apiKey,
			Endpoint:     "/embeddings",
		}
		h.ChatH.LogUsage(logInfo, nil, latencyMs, body, nil)
	}
}

// HandleResponses handles OpenAI Responses API (/v1/responses).
func (h *MediaHandler) HandleResponses(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	h.forwardMediaRequest(w, r, body, "gpt-4o", "/responses")
}

// HandleResponsesCompact handles compact responses.
func (h *MediaHandler) HandleResponsesCompact(w http.ResponseWriter, r *http.Request) {
	h.HandleResponses(w, r)
}

// HandleImages handles /v1/images/generations.
func (h *MediaHandler) HandleImages(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()
	h.forwardMediaRequest(w, r, body, "dall-e-3", "/v1/images/generations")
}

// HandleAudioSpeech handles /v1/audio/speech.
func (h *MediaHandler) HandleAudioSpeech(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()
	h.forwardMediaRequest(w, r, body, "tts-1", "/v1/audio/speech")
}

// HandleAudioVoices lists available audio voices.
func (h *MediaHandler) HandleAudioVoices(w http.ResponseWriter, r *http.Request) {
	voices := []map[string]string{
		{"id": "alloy", "name": "Alloy"},
		{"id": "echo", "name": "Echo"},
		{"id": "fable", "name": "Fable"},
		{"id": "onyx", "name": "Onyx"},
		{"id": "nova", "name": "Nova"},
		{"id": "shimmer", "name": "Shimmer"},
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"voices": voices})
}

// HandleAudioTranscriptions handles /v1/audio/transcriptions.
func (h *MediaHandler) HandleAudioTranscriptions(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()
	h.forwardMediaRequest(w, r, body, "whisper-1", "/v1/audio/transcriptions")
}

// HandleVideoGenerations handles /v1/videos/generations.
func (h *MediaHandler) HandleVideoGenerations(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()
	h.forwardMediaRequest(w, r, body, "sora", "/v1/videos/generations")
}

// HandleVideoEdits handles /v1/videos/edits.
func (h *MediaHandler) HandleVideoEdits(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()
	h.forwardMediaRequest(w, r, body, "sora", "/v1/videos/edits")
}

// HandleVideoExtensions handles /v1/videos/extensions.
func (h *MediaHandler) HandleVideoExtensions(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()
	h.forwardMediaRequest(w, r, body, "sora", "/v1/videos/extensions")
}

// HandleVideoGet handles GET /v1/videos/{id}.
func (h *MediaHandler) HandleVideoGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"id":     id,
		"status": "completed",
	})
}

// HandleSearch handles /v1/search.
func (h *MediaHandler) HandleSearch(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()
	h.forwardMediaRequest(w, r, body, "gpt-4o", "/v1/search")
}

// HandleScrape handles /v1/scrape.
func (h *MediaHandler) HandleScrape(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()
	h.forwardMediaRequest(w, r, body, "gpt-4o", "/v1/scrape")
}

// HandleWebFetch handles /v1/web/fetch.
func (h *MediaHandler) HandleWebFetch(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()
	h.forwardMediaRequest(w, r, body, "gpt-4o", "/v1/web/fetch")
}

func (h *MediaHandler) forwardMediaRequest(w http.ResponseWriter, r *http.Request, body []byte, defaultModel, endpoint string) {
	model := defaultModel
	var reqBody struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &reqBody); err == nil && reqBody.Model != "" {
		model = reqBody.Model
	}

	modelInfo, err := h.ChatH.ResolveModel(model)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Handle combo fallback if model is a combo
	if len(modelInfo.ComboModels) > 0 {
		var lastErr string
		for _, entry := range modelInfo.ComboModels {
			subInfo, err := h.ChatH.ResolveModel(entry)
			if err != nil {
				continue
			}
			conn, connData, err := h.ChatH.GetBestConnection(subInfo.Provider, subInfo.ConnectionID, nil, subInfo.Model)
			if err != nil || conn == nil {
				lastErr = fmt.Sprintf("no connection for %s", subInfo.Provider)
				continue
			}
			providerCfg, err := h.ChatH.GetProviderConfig(subInfo.Provider, connData)
			if err != nil {
				lastErr = err.Error()
				continue
			}
			apiKey := chat.ExtractAPIKey(connData)
			if apiKey == "" {
				lastErr = "no API key"
				continue
			}

			finalBody := handlerutil.UpdateModelInBody(body, subInfo.Model)
			targetURL := strings.TrimRight(providerCfg.BaseURL, "/") + endpoint
			req, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, bytes.NewReader(finalBody))
			if err != nil {
				lastErr = err.Error()
				continue
			}
			for k, v := range r.Header {
				req.Header[k] = v
			}
			handlerutil.SetAuthHeader(req, apiKey, providerCfg.AuthHeader, providerCfg.AuthScheme)
			client := h.ChatH.GetClientForConnection(connData)
			resp, err := client.Do(req)
			if err != nil {
				lastErr = err.Error()
				continue
			}
			if resp.StatusCode >= 400 {
				resp.Body.Close()
				lastErr = fmt.Sprintf("upstream status %d", resp.StatusCode)
				continue
			}
			defer resp.Body.Close()
			for k, v := range resp.Header {
				w.Header()[k] = v
			}
			w.WriteHeader(resp.StatusCode)
			io.Copy(w, resp.Body)
			h.Repo.UpdateConnectionLastUsed(conn.ID)
			return
		}
		handlerutil.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("all combo models failed: %s", lastErr))
		return
	}

	conn, connData, err := h.ChatH.GetBestConnection(modelInfo.Provider, modelInfo.ConnectionID, nil, modelInfo.Model)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, fmt.Sprintf("no active connections for provider: %s", modelInfo.Provider))
		return
	}

	providerCfg, err := h.ChatH.GetProviderConfig(modelInfo.Provider, connData)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	apiKey := chat.ExtractAPIKey(connData)
	if apiKey == "" {
		handlerutil.WriteJSONError(w, http.StatusUnauthorized, "no API key found")
		return
	}

	finalBody := handlerutil.UpdateModelInBody(body, modelInfo.Model)
	targetURL := strings.TrimRight(providerCfg.BaseURL, "/") + endpoint
	req, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, bytes.NewReader(finalBody))
	if err != nil {
		log.Error("media", "create request failed", "endpoint", endpoint, "error", err)
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to create request")
		return
	}

	for k, v := range r.Header {
		req.Header[k] = v
	}

	handlerutil.SetAuthHeader(req, apiKey, providerCfg.AuthHeader, providerCfg.AuthScheme)
	client := h.ChatH.GetClientForConnection(connData)
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		log.Error("media", "upstream request failed", "endpoint", endpoint, "error", err)
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "upstream request failed")
		return
	}
	defer resp.Body.Close()

	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)

	if resp.StatusCode == http.StatusOK && conn != nil {
		h.Repo.UpdateConnectionLastUsed(conn.ID)
		latencyMs := time.Since(start).Milliseconds()
		logInfo := &shared.UsageLogInfo{
			Provider:     modelInfo.Provider,
			Model:        modelInfo.Model,
			ConnectionID: conn.ID,
			APIKey:       apiKey,
			Endpoint:     endpoint,
		}
		h.ChatH.LogUsage(logInfo, nil, latencyMs, body, nil)
	}
}

// BuildEmbeddingsURL converts a chat completions URL to an embeddings URL.
func BuildEmbeddingsURL(baseURL string) string {
	return buildEmbeddingsURL(baseURL)
}

func buildEmbeddingsURL(baseURL string) string {
	if strings.Contains(baseURL, "/chat/completions") {
		return strings.Replace(baseURL, "/chat/completions", "/embeddings", 1)
	}
	return strings.TrimRight(baseURL, "/") + "/embeddings"
}
