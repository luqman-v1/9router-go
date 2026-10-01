package media

import (
	"9router/proxy/internal/constants"
	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/handlers/shared"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
	"9router/proxy/internal/usagetracker"
	"bytes"
	"encoding/base64"
	json "encoding/json/v2"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"
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
	var transport http.RoundTripper
	if origTransport, ok := http.DefaultTransport.(*http.Transport); ok {
		t := origTransport.Clone()
		t.ResponseHeaderTimeout = 2 * time.Minute
		transport = proxy.NewFallbackTransport(t)
	} else if fb, ok := http.DefaultTransport.(*proxy.FallbackTransport); ok {
		transport = fb
	} else {
		transport = proxy.NewFallbackTransport(http.DefaultTransport)
	}
	return &MediaHandler{
		Repo:       repo,
		Client:     &http.Client{Transport: transport, Timeout: 0},
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
	// Xiaomi MiMo TTS uses a chat-completions contract, not the OpenAI
	// /audio/speech shape (port of open-sse/handlers/ttsProviders/xiaomi-mimo.js).
	if h.isMiMoSpeech(body) {
		h.forwardMiMoSpeech(w, r, body)
		return
	}
	h.forwardTTSRequest(w, r, body)
}

// HandleSystemone forwards /v1/systemone structured-evaluation requests.
// Body: {"model":"<provider>/<model>","state":"...","questions":{...}}.
// Target URL from ProviderConfig.SystemoneURL, else swap /chat/completions.
func (h *MediaHandler) HandleSystemone(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()
	var reqBody struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &reqBody); err != nil || reqBody.Model == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing model")
		return
	}
	h.forwardSystemoneRequest(w, r, body, reqBody.Model)
}

// forwardSystemoneRequest resolves model, connection, config and forwards
// the raw body to the provider systemone endpoint with auth + static headers.
func (h *MediaHandler) forwardSystemoneRequest(w http.ResponseWriter, r *http.Request, body []byte, model string) {
	modelInfo, err := h.ChatH.ResolveModel(model)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	conn, connData, err := h.ChatH.GetBestConnection(modelInfo.Provider, modelInfo.ConnectionID, nil, modelInfo.Model)
	if err != nil || connData == nil {
		if cfg, ok := providers.KnownProviders[modelInfo.Provider]; ok && (cfg.NoAuth || cfg.DefaultAPIKey != "") {
			apiKey := cfg.DefaultAPIKey
			if apiKey == "" {
				apiKey = "public"
			}
			connData = &chat.ConnectionData{
				APIKey:      apiKey,
				ProxyPoolID: h.ChatH.ResolveProviderProxyPoolID(modelInfo.Provider),
			}
		} else {
			handlerutil.WriteJSONError(w, http.StatusNotFound, fmt.Sprintf("no active connections for provider: %s", modelInfo.Provider))
			return
		}
	}
	providerCfg, err := h.ChatH.GetProviderConfig(modelInfo.Provider, connData)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	apiKey := chat.ExtractAPIKey(connData)
	if apiKey == "" {
		if providerCfg != nil && providerCfg.DefaultAPIKey != "" {
			apiKey = providerCfg.DefaultAPIKey
		} else {
			handlerutil.WriteJSONError(w, http.StatusUnauthorized, "no API key found")
			return
		}
	}
	targetURL := providerCfg.SystemoneURL
	if connData != nil && connData.BaseURL != "" {
		baseURL := strings.TrimRight(connData.BaseURL, "/")
		if strings.Contains(baseURL, "/chat/completions") {
			targetURL = strings.Replace(baseURL, "/chat/completions", "/systemone", 1)
		} else {
			targetURL = baseURL + "/systemone"
		}
	} else if targetURL == "" {
		baseURL := strings.TrimRight(providerCfg.BaseURL, "/")
		if strings.Contains(baseURL, "/chat/completions") {
			targetURL = strings.Replace(baseURL, "/chat/completions", "/systemone", 1)
		} else {
			targetURL = baseURL + "/systemone"
		}
	}
	finalBody := handlerutil.UpdateModelInBody(body, modelInfo.Model)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, bytes.NewReader(finalBody))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to create request")
		return
	}
	req.Header.Set(constants.HeaderContentType, constants.ContentTypeJSON)
	handlerutil.SetAuthHeader(req, apiKey, providerCfg.AuthHeader, providerCfg.AuthScheme)
	for k, v := range providerCfg.StaticHeaders {
		if req.Header.Get(k) == "" {
			req.Header.Set(k, v)
		}
	}
	if modelInfo.Provider == "opencode" || modelInfo.Provider == "opencode-zen" {
		req.Header.Set("x-opencode-session", proxy.GenerateOpenCodeSessionID())
	}
	client, clientErr := h.ChatH.GetClientForConnection(connData)
	if clientErr != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, clientErr.Error())
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		// Transport-level failure only (proxy block, DNS, reset): retry once
		// direct before failing. A real upstream status (e.g. 403 account
		// verification) must surface — retrying direct against the same
		// target would mask it.
		directReq, err2 := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, bytes.NewReader(finalBody))
		if err2 == nil {
			directReq.Header = req.Header.Clone()
			resp2, err3 := directHTTPClient.Do(directReq)
			if err3 == nil {
				if resp != nil {
					resp.Body.Close()
				}
				resp = resp2
				err = nil
			}
		}
	}
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "upstream request failed: "+err.Error())
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
	}
}

// isMiMoSpeech reports whether body.model resolves to the xiaomi-mimo provider.
func (h *MediaHandler) isMiMoSpeech(body []byte) bool {
	var reqBody struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &reqBody); err != nil || reqBody.Model == "" {
		return false
	}
	info, err := h.ChatH.ResolveModel(reqBody.Model)
	return err == nil && info != nil && info.Provider == "xiaomi-mimo"
}

// mimoTTSPrefix/voice defaults (open-sse/handlers/ttsProviders/xiaomi-mimo.js).
const (
	mimoDefaultModel = "mimo-v2.5-tts"
	mimoDefaultVoice = "mimo_default"
)

// parseMiMoModelVoice splits the model part as "modelId/voiceId" against the
// single known mimo model (port of _base.js parseModelVoice for this adapter).
func parseMiMoModelVoice(model string) (modelID, voiceID string) {
	switch {
	case model == "":
		return mimoDefaultModel, mimoDefaultVoice
	case model == mimoDefaultModel:
		return mimoDefaultModel, mimoDefaultVoice
	case strings.HasPrefix(model, mimoDefaultModel+"/"):
		return mimoDefaultModel, strings.TrimPrefix(model, mimoDefaultModel+"/")
	}
	if before, after, ok := strings.CutLast(model, "/"); ok && before != "" {
		return before, after
	}
	return mimoDefaultModel, mimoDefaultVoice
}

// forwardMiMoSpeech synthesizes speech via Xiaomi MiMo's OpenAI-compatible
// chat-completions contract: target text in role:assistant, style/language
// instructions in role:user, voice via the top-level audio.voice field, audio
// returned base64 in choices[0].message.audio.data.
func (h *MediaHandler) forwardMiMoSpeech(w http.ResponseWriter, r *http.Request, body []byte) {
	var req struct {
		Model       string `json:"model"`
		Input       string `json:"input"`
		Style       string `json:"style"`
		Language    string `json:"language"`
		ResponseFmt string `json:"response_format"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Input) == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Missing required field: input")
		return
	}

	modelInfo, err := h.ChatH.ResolveModel(req.Model)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	_, connData, err := h.ChatH.GetBestConnection(modelInfo.Provider, modelInfo.ConnectionID, nil, modelInfo.Model)
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

	modelID, voiceID := parseMiMoModelVoice(modelInfo.Model)

	// Language and style are soft instructions; MiMo auto-detects the spoken
	// language of the text, the hint only nudges it (independent of the voice).
	var instructions []string
	if req.Language != "" {
		instructions = append(instructions, "Speak in "+req.Language+".")
	}
	if req.Style != "" {
		instructions = append(instructions, req.Style)
	}
	messages := []map[string]string{{"role": "assistant", "content": req.Input}}
	if len(instructions) > 0 {
		messages = append([]map[string]string{{"role": "user", "content": strings.Join(instructions, " ")}}, messages...)
	}

	payload, err := json.Marshal(map[string]any{
		"model":    modelID,
		"stream":   false,
		"messages": messages,
		"audio":    map[string]string{"format": "wav", "voice": voiceID},
	})
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	upstreamReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, providerCfg.BaseURL, bytes.NewReader(payload))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, fmt.Sprintf("create request: %v", err))
		return
	}
	upstreamReq.Header.Set(constants.HeaderContentType, constants.ContentTypeJSON)
	upstreamReq.Header.Set(constants.HeaderAuthorization, "Bearer "+apiKey)

	resp, err := h.Client.Do(upstreamReq)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("upstream error: %v", err))
		return
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
		return
	}

	var data struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Choices []struct {
			Message struct {
				Audio struct {
					Data   string `json:"data"`
					Format string `json:"format"`
				} `json:"audio"`
			} `json:"message"`
		} `json:"choices"`
	}
	// Upstream body may be non-JSON; tolerate parse failure (JS wraps in try/catch).
	_ = json.Unmarshal(respBody, &data)

	if resp.StatusCode != http.StatusOK {
		msg := fmt.Sprintf("MiMo TTS error (%d)", resp.StatusCode)
		switch {
		case data.Error != nil && data.Error.Message != "":
			msg = data.Error.Message
		case len(bytes.TrimSpace(respBody)) > 0:
			msg = string(bytes.TrimSpace(respBody))
		}
		handlerutil.WriteJSONError(w, http.StatusBadGateway, msg)
		return
	}

	audio, format := "", "wav"
	if len(data.Choices) > 0 {
		audio = data.Choices[0].Message.Audio.Data
		if data.Choices[0].Message.Audio.Format != "" {
			format = data.Choices[0].Message.Audio.Format
		}
	}
	if audio == "" {
		msg := "MiMo TTS returned no audio"
		if data.Error != nil && data.Error.Message != "" {
			msg = data.Error.Message
		}
		handlerutil.WriteJSONError(w, http.StatusBadGateway, msg)
		return
	}

	// response_format=json returns {audio, format}; default returns raw audio bytes.
	if req.ResponseFmt == "json" {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]string{"audio": audio, "format": format})
		return
	}
	raw, err := base64.StdEncoding.DecodeString(audio)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "MiMo TTS returned invalid audio")
		return
	}
	w.Header().Set("Content-Type", "audio/"+format)
	w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
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
	if strings.Contains(id, "..") || strings.Contains(id, "/") || strings.Contains(id, "\\") {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid video job id")
		return
	}
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
	} else if strings.Contains(r.Header.Get("Content-Type"), "multipart/form-data") {
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err == nil && params["boundary"] != "" {
			mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
			for {
				p, err := mr.NextPart()
				if err != nil {
					break
				}
				if p.FormName() == "model" {
					val, _ := io.ReadAll(p)
					if len(val) > 0 {
						model = strings.TrimSpace(string(val))
					}
					break
				}
			}
		}
	}
	modelInfo, err := h.ChatH.ResolveModel(model)
	if err != nil {
		log.Warn("media", "resolve model failed", "endpoint", endpoint, "model", model, "error", err)
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	log.Debug("media", "forward request", "endpoint", endpoint, "model", model, "provider", modelInfo.Provider, "resolvedModel", modelInfo.Model)

	// Handle combo fallback if model is a combo
	if len(modelInfo.ComboModels) > 0 {
		var lastErr string
		lastStatus := http.StatusBadGateway
		for _, entry := range modelInfo.ComboModels {
			subInfo, err := h.ChatH.ResolveModel(entry)
			if err != nil {
				continue
			}
			if endpoint == "/v1/search" && subInfo.Provider == "antigravity" {
				if err := h.handleAntigravitySearch(w, r, body, subInfo); err == nil {
					return
				} else {
					lastErr, lastStatus = searchErrorParts(err)
					continue
				}
			}
			if endpoint == "/v1/search" && (subInfo.Provider == "xquik" || subInfo.Provider == "xquik-search") {
				if err := h.handleXquikSearch(w, r, body, subInfo); err == nil {
					return
				} else {
					lastErr, lastStatus = searchErrorParts(err)
					continue
				}
			}
			if (endpoint == "/v1/images/generations" || endpoint == "/images/generations") && (subInfo.Provider == "antigravity" || subInfo.Provider == "ag") {
				if err := h.handleAntigravityImage(w, r, body, subInfo); err == nil {
					return
				} else {
					lastErr = err.Error()
					continue
				}
			}
			if (endpoint == "/v1/audio/transcriptions" || endpoint == "/audio/transcriptions") && (subInfo.Provider == "antigravity" || subInfo.Provider == "ag") {
				if err := h.handleAntigravitySTT(w, r, body, subInfo); err == nil {
					return
				} else {
					lastErr = err.Error()
					continue
				}
			}

			// Realtime STT: dispatched on the model's transport marker, never on a
			// hardcoded id, so a new streaming provider extends through data.
			if isTranscriptionEndpoint(endpoint) &&
				providers.ResolveModelTransport(subInfo.Provider, subInfo.Model) == providers.TransportGeminiLive {
				if err := h.handleGeminiLiveSTT(w, r, body, subInfo); err == nil {
					return
				} else {
					lastErr = err.Error()
					continue
				}
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
			method := r.Method
			// Use provider FetchURL for web/fetch when configured (e.g., ollama cloud)
			if endpoint == "/v1/web/fetch" && providerCfg.FetchURL != "" {
				targetURL = providerCfg.FetchURL
				if providerCfg.FetchMethod != "" {
					method = providerCfg.FetchMethod
				} else {
					method = "POST"
				}
				// Ollama cloud expects {"url": "https://..."} only
				if subInfo.Provider == "ollama" {
					var tmp struct {
						URL string `json:"url"`
					}
					if err := json.Unmarshal(body, &tmp); err == nil && tmp.URL != "" {
						if b, err := json.Marshal(map[string]string{"url": tmp.URL}); err == nil {
							finalBody = b
						}
					}
				}
			}
			req, err := http.NewRequestWithContext(r.Context(), method, targetURL, bytes.NewReader(finalBody))
			if err != nil {
				lastErr = err.Error()
				continue
			}
			for k, v := range r.Header {
				req.Header[k] = v
			}
			handlerutil.SetAuthHeader(req, apiKey, providerCfg.AuthHeader, providerCfg.AuthScheme)
			client, clientErr := h.ChatH.GetClientForConnection(connData)
			if clientErr != nil {
				lastErr = clientErr.Error()
				continue
			}
			resp, err := client.Do(req)
			if err != nil {
				log.Warn("media", "upstream combo request failed", "endpoint", endpoint, "provider", subInfo.Provider, "model", subInfo.Model, "conn", conn.ID[:min(8, len(conn.ID))], "error", err)
				lastErr = err.Error()
				continue
			}
			if resp.StatusCode >= 400 {
				errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1*1024))
				resp.Body.Close()
				errText := strings.TrimSpace(string(errBody))
				if errText == "" {
					errText = resp.Status
				}
				log.Warn("media", "upstream combo error", "endpoint", endpoint, "provider", subInfo.Provider, "model", subInfo.Model, "conn", conn.ID[:min(8, len(conn.ID))], "status", resp.StatusCode, "body", errText)
				// Upstream parity (videoGeneration CREATE_ROTATION_STATUSES):
				// billable video creation must not rotate on 5xx — the job
				// may already exist upstream. Auth/quota errors rotate.
				if isVideoCreateEndpoint(endpoint) && resp.StatusCode >= 500 {
					lastErr = fmt.Sprintf("upstream status %d: %s", resp.StatusCode, errText[:min(200, len(errText))])
					lastStatus = resp.StatusCode
					break
				}
				lastErr = fmt.Sprintf("upstream status %d: %s", resp.StatusCode, errText[:min(200, len(errText))])
				lastStatus = resp.StatusCode
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
		handlerutil.WriteJSONError(w, lastStatus, fmt.Sprintf("all combo models failed: %s", lastErr))
		return
	}

	if endpoint == "/v1/search" && modelInfo.Provider == "antigravity" {
		if err := h.handleAntigravitySearch(w, r, body, modelInfo); err != nil {
			writeSearchError(w, err)
		}
		return
	}

	if endpoint == "/v1/search" && (modelInfo.Provider == "xquik" || modelInfo.Provider == "xquik-search") {
		if err := h.handleXquikSearch(w, r, body, modelInfo); err != nil {
			writeSearchError(w, err)
		}
		return
	}
	if (endpoint == "/v1/images/generations" || endpoint == "/images/generations") && (modelInfo.Provider == "antigravity" || modelInfo.Provider == "ag") {
		if err := h.handleAntigravityImage(w, r, body, modelInfo); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
		}
		return
	}
	if (endpoint == "/v1/audio/transcriptions" || endpoint == "/audio/transcriptions") && (modelInfo.Provider == "antigravity" || modelInfo.Provider == "ag") {
		if err := h.handleAntigravitySTT(w, r, body, modelInfo); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
		}
		return
	}

	// Realtime STT: dispatched on the model's transport marker, never on a
	// hardcoded id, so a new streaming provider extends through data.
	if isTranscriptionEndpoint(endpoint) &&
		providers.ResolveModelTransport(modelInfo.Provider, modelInfo.Model) == providers.TransportGeminiLive {
		if err := h.handleGeminiLiveSTT(w, r, body, modelInfo); err != nil {
			writeLiveSTTError(w, err)
		}
		return
	}

	preferredConnID := r.Header.Get("x-connection-id")
	if preferredConnID == "" {
		preferredConnID = r.Header.Get("x-provider-connection-id")
	}
	if preferredConnID == "" {
		preferredConnID = modelInfo.ConnectionID
	}
	conn, connData, err := h.ChatH.GetBestConnection(modelInfo.Provider, preferredConnID, nil, modelInfo.Model)
	if err != nil || connData == nil {
		if cfg, ok := providers.KnownProviders[modelInfo.Provider]; ok && (cfg.NoAuth || cfg.DefaultAPIKey != "") {
			apiKey := cfg.DefaultAPIKey
			if apiKey == "" {
				apiKey = "public"
			}
			connData = &chat.ConnectionData{
				APIKey:      apiKey,
				ProxyPoolID: h.ChatH.ResolveProviderProxyPoolID(modelInfo.Provider),
			}
		} else {
			log.Warn("media", "no connection", "endpoint", endpoint, "provider", modelInfo.Provider, "model", modelInfo.Model, "error", err)
			handlerutil.WriteJSONError(w, http.StatusNotFound, fmt.Sprintf("no active connections for provider: %s", modelInfo.Provider))
			return
		}
	}

	providerCfg, err := h.ChatH.GetProviderConfig(modelInfo.Provider, connData)
	if err != nil {
		log.Error("media", "get provider config failed", "endpoint", endpoint, "provider", modelInfo.Provider, "error", err)
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	apiKey := chat.ExtractAPIKey(connData)
	if apiKey == "" {
		if providerCfg != nil && providerCfg.DefaultAPIKey != "" {
			apiKey = providerCfg.DefaultAPIKey
		} else {
			connIDStr := ""
			if conn != nil {
				connIDStr = conn.ID[:min(8, len(conn.ID))]
			}
			log.Warn("media", "no api key", "endpoint", endpoint, "provider", modelInfo.Provider, "conn", connIDStr)
			handlerutil.WriteJSONError(w, http.StatusUnauthorized, "no API key found")
			return
		}
	}

	finalBody := body
	if !strings.Contains(r.Header.Get("Content-Type"), "multipart/form-data") {
		finalBody = handlerutil.UpdateModelInBody(body, modelInfo.Model)
	} else if model != "" && model != modelInfo.Model {
		// Upstream parity (videoGeneration byte-exact multipart): never
		// rewrite inside the binary payload — a byte.Replace could corrupt
		// file bytes that happen to match. Rebuild only the model part.
		if rebuilt, err := rewriteMultipartModelPart(body, r.Header.Get("Content-Type"), model, modelInfo.Model); err == nil {
			finalBody = rebuilt
		} else {
			log.Warn("media", "multipart model rewrite skipped", "endpoint", endpoint, "error", err)
		}
	}
	client, clientErr := h.ChatH.GetClientForConnection(connData)
	if clientErr != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, clientErr.Error())
		return
	}

	connID := ""
	if conn != nil {
		connID = conn.ID
	}
	var fwdErr error
	usagetracker.GetTracker().TrackPending(modelInfo.Model, modelInfo.Provider, connID, true, false)
	defer func() {
		hasErr := fwdErr != nil
		usagetracker.GetTracker().TrackPending(modelInfo.Model, modelInfo.Provider, connID, false, hasErr)
	}()

	baseURL := strings.TrimRight(providerCfg.BaseURL, "/")
	if connData != nil && connData.BaseURL != "" {
		baseURL = strings.TrimRight(connData.BaseURL, "/")
	}
	baseURL = strings.TrimSuffix(baseURL, "/chat/completions")
	baseURL = strings.TrimSuffix(baseURL, "/messages")

	hasCustomBaseURL := connData != nil && connData.BaseURL != "" && connData.BaseURL != providerCfg.BaseURL

	var targetURL string
	switch endpoint {
	case "/v1/images/generations", "/images/generations":
		if !hasCustomBaseURL && providerCfg.ImageURL != "" {
			targetURL = providerCfg.ImageURL
		} else {
			targetURL = strings.TrimSuffix(baseURL, "/v1") + "/v1/images/generations"
		}
	case "/v1/audio/speech", "/audio/speech":
		if !hasCustomBaseURL && providerCfg.TTSURL != "" {
			targetURL = providerCfg.TTSURL
		} else {
			targetURL = strings.TrimSuffix(baseURL, "/v1") + "/v1/audio/speech"
		}
	case "/v1/audio/transcriptions", "/audio/transcriptions":
		if !hasCustomBaseURL && providerCfg.STTURL != "" {
			targetURL = providerCfg.STTURL
		} else {
			targetURL = strings.TrimSuffix(baseURL, "/v1") + "/v1/audio/transcriptions"
		}
	case "/v1/videos/generations", "/videos/generations":
		if !hasCustomBaseURL && providerCfg.VideoURL != "" {
			if strings.HasSuffix(providerCfg.VideoURL, "/generations") {
				targetURL = providerCfg.VideoURL
			} else {
				targetURL = strings.TrimRight(providerCfg.VideoURL, "/") + "/generations"
			}
		} else {
			targetURL = strings.TrimSuffix(baseURL, "/v1") + "/v1/videos/generations"
		}
	case "/v1/systemone", "/systemone":
		if !hasCustomBaseURL && providerCfg.SystemoneURL != "" {
			targetURL = providerCfg.SystemoneURL
		} else {
			targetURL = strings.TrimSuffix(baseURL, "/v1") + "/v1/systemone"
		}
	default:
		if strings.HasSuffix(baseURL, "/v1") && strings.HasPrefix(endpoint, "/v1/") {
			targetURL = strings.TrimSuffix(baseURL, "/v1") + endpoint
		} else {
			targetURL = baseURL + endpoint
		}
	}
	method := r.Method
	if endpoint == "/v1/web/fetch" && providerCfg.FetchURL != "" {
		targetURL = providerCfg.FetchURL
		if providerCfg.FetchMethod != "" {
			method = providerCfg.FetchMethod
		} else {
			method = "POST"
		}
		if modelInfo.Provider == "ollama" {
			var tmp struct {
				URL string `json:"url"`
			}
			if err := json.Unmarshal(body, &tmp); err == nil && tmp.URL != "" {
				if b, err := json.Marshal(map[string]string{"url": tmp.URL}); err == nil {
					finalBody = b
				}
			}
		}
	}
	log.Info("media", "forwarding to targetURL", "targetURL", targetURL, "method", method, "model", modelInfo.Model)
	req, err := http.NewRequestWithContext(r.Context(), method, targetURL, bytes.NewReader(finalBody))
	if err != nil {
		log.Error("media", "create request failed", "endpoint", endpoint, "error", err)
		return
	}

	for k, v := range r.Header {
		req.Header[k] = v
	}
	req.Header.Del("Host")
	req.Header.Del("Content-Length")
	for k, v := range providerCfg.StaticHeaders {
		req.Header.Set(k, v)
	}
	handlerutil.SetAuthHeader(req, apiKey, providerCfg.AuthHeader, providerCfg.AuthScheme)
	connIDStr := ""
	if conn != nil {
		connIDStr = conn.ID[:min(8, len(conn.ID))]
	}
	// Log search query for observability (was "nebak" before)
	if endpoint == "/v1/search" {
		var qb struct {
			Query  string `json:"query"`
			Prompt string `json:"prompt"`
		}
		_ = json.Unmarshal(body, &qb)
		q := qb.Query
		if q == "" {
			q = qb.Prompt
		}
		if q != "" {
			log.Info("request", "POST "+endpoint, "provider", modelInfo.Provider, "model", modelInfo.Model, "query", q, "conn", connIDStr)
		}
	}
	start := time.Now()
	resp, err := doDirectOrClient(r.Context(), client, req)
	if err != nil {
		log.Error("media", "upstream request failed", "endpoint", endpoint, "provider", modelInfo.Provider, "model", modelInfo.Model, "conn", connIDStr, "error", err)
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "upstream request failed")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1*1024))
		log.Warn("media", "upstream error", "endpoint", endpoint, "provider", modelInfo.Provider, "model", modelInfo.Model, "conn", connIDStr, "status", resp.StatusCode, "body", string(errBody))
		// Need to re-create body for copying
		resp.Body = io.NopCloser(bytes.NewReader(errBody))
	}

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

// rewriteMultipartModelPart rebuilds a multipart body with only the "model"
// part value replaced (upstream videoGeneration byte-exact parity). Every
// other part — including binary file bytes — is copied verbatim, so a model
// string occurring inside file bytes can never be corrupted.
func rewriteMultipartModelPart(body []byte, contentType, oldModel, newModel string) ([]byte, error) {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil || params["boundary"] == "" {
		return nil, fmt.Errorf("invalid multipart content type: %w", err)
	}
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.SetBoundary(params["boundary"]); err != nil {
		return nil, err
	}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(part)
		if err != nil {
			return nil, err
		}
		value := string(data)
		if part.FormName() == "model" && strings.TrimSpace(value) == oldModel {
			value = newModel
		}
		fw, err := mw.CreatePart(part.Header)
		if err != nil {
			return nil, err
		}
		if _, err := fw.Write([]byte(value)); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// isVideoCreateEndpoint reports whether the endpoint creates a billable async
// video job (upstream videoGeneration CREATE_ROTATION_STATUSES parity:
// 5xx must not rotate — the job may already exist upstream).
func isVideoCreateEndpoint(endpoint string) bool {
	switch endpoint {
	case "/v1/videos/generations", "/videos/generations",
		"/v1/videos/edits", "/videos/edits",
		"/v1/videos/extensions", "/videos/extensions":
		return true
	}
	return false
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
