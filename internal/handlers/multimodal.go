package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"9router/proxy/internal/constants"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
)

// multimodalPath returns the sub-endpoint URL for a multimodal service.
// It checks provider-specific sub-endpoint URLs first, then falls back to
// swapping /chat/completions suffix in the base URL.
func multimodalPath(cfg *providers.ProviderConfig, path string) string {
	switch {
	case path == "/images/generations" && cfg.ImageURL != "":
		return cfg.ImageURL
	case path == "/audio/speech" && cfg.TTSURL != "":
		return cfg.TTSURL
	case path == "/audio/transcriptions" && cfg.STTURL != "":
		return cfg.STTURL
	case strings.HasPrefix(path, "/videos") && cfg.VideoURL != "":
		return cfg.VideoURL
	}
	if strings.Contains(cfg.BaseURL, "/chat/completions") {
		return strings.Replace(cfg.BaseURL, "/chat/completions", path, 1)
	}
	return strings.TrimRight(cfg.BaseURL, "/") + path
}

// modelsProviderCtx holds the resolved connection + config for a single-model forward.
type modelsProviderCtx struct {
	conn        *models.ProviderConnection
	connData    *ConnectionData
	providerCfg *providers.ProviderConfig
	apiKey      string
	modelInfo   *ModelInfo
}

// resolveSingleModel resolves the model and looks up the best connection/config.
func (h *ChatHandler) resolveSingleModel(body []byte) (*ModelInfo, *modelsProviderCtx, error) {
	var reqBody struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &reqBody); err != nil {
		return nil, nil, fmt.Errorf("invalid JSON body")
	}
	if reqBody.Model == "" {
		return nil, nil, fmt.Errorf("missing model")
	}

	modelInfo, err := h.resolveModel(reqBody.Model)
	if err != nil {
		return nil, nil, err
	}

	return h.buildProviderCtx(modelInfo)
}

// buildProviderCtx looks up the connection and config for an already-resolved model.
func (h *ChatHandler) buildProviderCtx(modelInfo *ModelInfo) (*ModelInfo, *modelsProviderCtx, error) {
	conn, connData, err := h.getBestConnection(modelInfo.Provider, modelInfo.ConnectionID, nil, modelInfo.Model)
	if err != nil {
		return nil, nil, err
	}
	providerCfg, err := h.getProviderConfig(modelInfo.Provider, connData)
	if err != nil {
		return nil, nil, err
	}
	apiKey := extractAPIKey(connData)
	if apiKey == "" {
		return nil, nil, fmt.Errorf("no API key found")
	}
	ctx := &modelsProviderCtx{
		conn:        conn,
		connData:    connData,
		providerCfg: providerCfg,
		apiKey:      apiKey,
		modelInfo:   modelInfo,
	}
	return modelInfo, ctx, nil
}

// statusForModelErr maps a resolve/connection error to an HTTP status.
func statusForModelErr(err error) int {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "no active connections"), strings.Contains(msg, "not found"):
		return http.StatusNotFound
	case strings.Contains(msg, "no API key"):
		return http.StatusUnauthorized
	default:
		return http.StatusBadRequest
	}
}

// forwardMultimodal posts the request body to the given provider sub-path and
// copies the upstream response (headers + body) back to the client unchanged.
func (h *ChatHandler) forwardMultimodal(w http.ResponseWriter, _ *http.Request, ctx *modelsProviderCtx, path string, body []byte) {
	url := multimodalPath(ctx.providerCfg, path)
	finalBody := handlerutil.UpdateModelInBody(body, ctx.modelInfo.Model)

	req, err := http.NewRequest("POST", url, strings.NewReader(string(finalBody)))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, fmt.Sprintf("create request: %v", err))
		return
	}
	req.Header.Set(constants.HeaderContentType, constants.ContentTypeJSON)
	handlerutil.SetAuthHeader(req, ctx.apiKey, ctx.providerCfg.AuthHeader, ctx.providerCfg.AuthScheme)

	resp, err := h.Client.Do(req)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("upstream error: %v", err))
		return
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get(constants.HeaderContentType); ct != "" {
		w.Header().Set(constants.HeaderContentType, ct)
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)

	if resp.StatusCode == http.StatusOK {
		h.Repo.UpdateConnectionLastUsed(ctx.conn.ID)
	}
}

// HandleImages forwards /v1/images/generations requests to upstream providers.
func (h *ChatHandler) HandleImages(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer r.Body.Close()

	_, ctx, err := h.resolveSingleModel(body)
	if err != nil {
		handlerutil.WriteJSONError(w, statusForModelErr(err), err.Error())
		return
	}
	h.forwardMultimodal(w, r, ctx, "/images/generations", body)
}

// HandleAudioSpeech forwards /v1/audio/speech (TTS) requests upstream.
func (h *ChatHandler) HandleAudioSpeech(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer r.Body.Close()

	_, ctx, err := h.resolveSingleModel(body)
	if err != nil {
		handlerutil.WriteJSONError(w, statusForModelErr(err), err.Error())
		return
	}
	h.forwardMultimodal(w, r, ctx, "/audio/speech", body)
}

// HandleAudioTranscriptions forwards /v1/audio/transcriptions (STT) requests upstream.
func (h *ChatHandler) HandleAudioTranscriptions(w http.ResponseWriter, r *http.Request) {
	model := r.FormValue("model")
	if model == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing model")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer r.Body.Close()
	modelInfo, err := h.resolveModel(model)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	_, ctx, err := h.buildProviderCtx(modelInfo)
	if err != nil {
		handlerutil.WriteJSONError(w, statusForModelErr(err), err.Error())
		return
	}

	url := multimodalPath(ctx.providerCfg, "/audio/transcriptions")
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, fmt.Sprintf("create request: %v", err))
		return
	}
	if ct := r.Header.Get(constants.HeaderContentType); ct != "" {
		req.Header.Set(constants.HeaderContentType, ct)
	}
	handlerutil.SetAuthHeader(req, ctx.apiKey, ctx.providerCfg.AuthHeader, ctx.providerCfg.AuthScheme)

	resp, err := h.Client.Do(req)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("upstream error: %v", err))
		return
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get(constants.HeaderContentType); ct != "" {
		w.Header().Set(constants.HeaderContentType, ct)
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)

	if resp.StatusCode == http.StatusOK {
		h.Repo.UpdateConnectionLastUsed(ctx.conn.ID)
	}
}

// ---- Video endpoints (xAI Grok Imagine) ----

// HandleVideoGenerations creates an async video generation job.
// POST /v1/videos/generations
func (h *ChatHandler) HandleVideoGenerations(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer r.Body.Close()

	_, ctx, err := h.resolveSingleModel(body)
	if err != nil {
		handlerutil.WriteJSONError(w, statusForModelErr(err), err.Error())
		return
	}
	h.forwardMultimodal(w, r, ctx, "/videos/generations", body)
}

// HandleVideoEdits creates an async video edit job.
// POST /v1/videos/edits
func (h *ChatHandler) HandleVideoEdits(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer r.Body.Close()

	_, ctx, err := h.resolveSingleModel(body)
	if err != nil {
		handlerutil.WriteJSONError(w, statusForModelErr(err), err.Error())
		return
	}
	h.forwardMultimodal(w, r, ctx, "/videos/edits", body)
}

// HandleVideoExtensions creates an async video extension job.
// POST /v1/videos/extensions
func (h *ChatHandler) HandleVideoExtensions(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer r.Body.Close()

	_, ctx, err := h.resolveSingleModel(body)
	if err != nil {
		handlerutil.WriteJSONError(w, statusForModelErr(err), err.Error())
		return
	}
	h.forwardMultimodal(w, r, ctx, "/videos/extensions", body)
}

// HandleVideoGet polls async video job status by request ID.
// GET /v1/videos/{id}
func (h *ChatHandler) HandleVideoGet(w http.ResponseWriter, r *http.Request) {
	requestID := r.PathValue("id")
	if requestID == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing video request ID")
		return
	}

	provider := r.URL.Query().Get("provider")
	if provider == "" {
		provider = "xai"
	}
	p := resolveProviderAlias(provider)

	var videoURL string
	if cfg, ok := providers.KnownProviders[p]; ok {
		if cfg.VideoURL != "" {
			videoURL = strings.TrimRight(cfg.VideoURL, "/") + "/" + requestID
		}
	}
	if videoURL == "" {
		handlerutil.WriteJSONError(w, http.StatusNotFound, fmt.Sprintf("no video endpoint for provider: %s", provider))
		return
	}

	conn, connData, err := h.getBestConnection(p, "", nil, "")
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, fmt.Sprintf("no connection for provider: %s", provider))
		return
	}
	cfg, err := h.getProviderConfig(p, connData)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	apiKey := extractAPIKey(connData)
	if apiKey == "" {
		handlerutil.WriteJSONError(w, http.StatusUnauthorized, "no API key found")
		return
	}

	req, err := http.NewRequest("GET", videoURL, nil)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, fmt.Sprintf("create request: %v", err))
		return
	}
	handlerutil.SetAuthHeader(req, apiKey, cfg.AuthHeader, cfg.AuthScheme)

	resp, err := h.Client.Do(req)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("upstream error: %v", err))
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)

	if resp.StatusCode == http.StatusOK {
		h.Repo.UpdateConnectionLastUsed(conn.ID)
	}
}
