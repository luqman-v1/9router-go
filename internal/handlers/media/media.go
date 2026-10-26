package media

import (
	"9router/proxy/internal/constants"
	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/handlers/shared"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/middleware"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
	"9router/proxy/internal/usagetracker"
	"bytes"
	"context"
	"encoding/base64"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
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

// mediaConnectionError renders why a media lane found no usable account.
//
// GetBestConnection already distinguishes the cases that matter: "every
// account is parked in cooldown until <stamp>" is a temporary condition that
// resolves on its own, while "all excluded" or a parse failure is not. The
// generic "no active connections" text this replaced collapsed both into one
// sentence, so an operator whose account was merely cooling down read it as a
// broken credential — and the dashboard's model sweep had no way to tell the
// untested models from the genuinely broken ones.
func mediaConnectionError(err error, provider string) string {
	if err != nil && err.Error() != "" {
		return err.Error()
	}
	return fmt.Sprintf("no active connections for provider: %s", provider)
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

	if !h.ChatH.EnforceModelAccess(w, r, reqBody.Model) {
		return
	}
	modelInfo, err := h.ChatH.ResolveModel(reqBody.Model)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	conn, connData, err := h.ChatH.GetBestConnectionWithContext(r.Context(), modelInfo.Provider, modelInfo.ConnectionID, nil, modelInfo.Model)
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
	for k, v := range providerCfg.StaticHeaders {
		if req.Header.Get(k) == "" {
			req.Header.Set(k, v)
		}
	}
	// GitHub Copilot hands out a short-lived token alongside the OAuth
	// credential, and the embeddings endpoint is authenticated by that token
	// rather than by the stored one. It is set after the static-header loop
	// because it replaces the Authorization header on purpose.
	//
	// The override is scoped to the GitHub provider: the token is a GitHub
	// credential, and upstream sends it only from the GitHub embeddings
	// adapter (open-sse/handlers/embeddingProviders/github.js). Without the
	// scope check the token was applied to whichever upstream the connection
	// pointed at, so a connection for any other provider that happened to
	// carry the key had its own key replaced and leaked the GitHub credential.
	if connData != nil && providers.ResolveAlias(modelInfo.Provider) == "github" {
		if copilotToken, ok := connData.ProviderSpecificData["copilotToken"].(string); ok && copilotToken != "" {
			req.Header.Set("Authorization", "Bearer "+copilotToken)
		}
	}

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
		h.ChatH.LogUsage(r.Context(), logInfo, nil, latencyMs, body, nil)
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
	// It leaves forwardTTSRequest behind, so it needs the guard here.
	if h.isMiMoSpeech(body) {
		if _, refused := guardMediaInput(w, r.Header.Get("Content-Type"), body); refused {
			return
		}
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
	if !h.ChatH.EnforceModelAccess(w, r, reqBody.Model) {
		return
	}
	// SystemOne forwards on its own path rather than through
	// forwardMediaRequest, so the cap and the usage accounting have to be
	// applied here. Its body is JSON with a `state` payload, which is why it
	// never trips the media walk in practice — but "in practice" is exactly
	// the assumption an unbounded lane depends on.
	if _, refused := guardMediaInput(w, r.Header.Get("Content-Type"), body); refused {
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
	conn, connData, err := h.ChatH.GetBestConnectionWithContext(r.Context(), modelInfo.Provider, modelInfo.ConnectionID, nil, modelInfo.Model)
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
			// The selector's own error says whether the provider is simply
			// parked in cooldown and when it frees up. Substituting a generic
			// "no active connections" here throws that away, and the
			// dashboard's model sweep then cannot tell a cooldown from a
			// broken account.
			handlerutil.WriteJSONError(w, http.StatusNotFound, mediaConnectionError(err, modelInfo.Provider))
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
	override := ""
	if connData != nil {
		override = strings.TrimSpace(connData.BaseURL)
	}
	var targetURL string
	switch {
	case override != "" && strings.Contains(override, "/chat/completions"):
		targetURL = strings.Replace(strings.TrimRight(override, "/"), "/chat/completions", "/systemone", 1)
	case override != "":
		targetURL = buildSystemoneURL(override, providerCfg.SystemoneURL, providerCfg.SystemonePath)
	case providerCfg.SystemoneURL != "":
		targetURL = providerCfg.SystemoneURL
	default:
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
		// One stable session per provider+connection, like the chat lanes:
		// minting a fresh session per request drains the egress budget at
		// mint time and surfaces as 429 FreeUsageLimitError. A separate
		// namespace from the chat lanes keeps media traffic from drawing on
		// a conversation's session budget.
		mediaSessionKey := "media:" + modelInfo.Provider
		if modelInfo.ConnectionID != "" {
			mediaSessionKey += ":conn:" + modelInfo.ConnectionID
		}
		req.Header.Set("x-opencode-session", proxy.StableOpenCodeSessionID(mediaSessionKey))
	}
	client, clientErr := h.ChatH.GetClientForConnection(connData)
	if clientErr != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, clientErr.Error())
		return
	}
	resp, err := client.Do(req)
	if err != nil && r.Context().Err() == nil {
		// Transport-level failure only (proxy block, DNS, reset): retry once
		// direct before failing. A real upstream status (e.g. 403 account
		// verification) must surface — retrying direct against the same
		// target would mask it. A canceled request is neither: the caller is
		// gone, so the retry would make a fresh connection for nobody.
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
	copyUpstreamHeaders(w.Header(), resp.Header)
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
	_, connData, err := h.ChatH.GetBestConnectionWithContext(r.Context(), modelInfo.Provider, modelInfo.ConnectionID, nil, modelInfo.Model)
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
		middleware.WriteBodyReadError(w, r, err)
		return
	}
	defer r.Body.Close()
	h.forwardMediaRequest(w, r, body, "whisper-1", "/v1/audio/transcriptions")
}

// HandleVideoGenerations handles /v1/videos/generations.
func (h *MediaHandler) HandleVideoGenerations(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		middleware.WriteBodyReadError(w, r, err)
		return
	}
	defer r.Body.Close()
	h.forwardMediaRequest(w, r, body, "sora", "/v1/videos/generations")
}

// HandleVideoEdits handles /v1/videos/edits.
func (h *MediaHandler) HandleVideoEdits(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		middleware.WriteBodyReadError(w, r, err)
		return
	}
	defer r.Body.Close()
	h.forwardMediaRequest(w, r, body, "sora", "/v1/videos/edits")
}

// HandleVideoExtensions handles /v1/videos/extensions.
func (h *MediaHandler) HandleVideoExtensions(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		middleware.WriteBodyReadError(w, r, err)
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
	body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var req struct {
		URL      string `json:"url"`
		Model    string `json:"model"`
		Provider string `json:"provider"`
		Format   string `json:"format"`
	}
	if err := json.Unmarshal(body, &req); err != nil || strings.TrimSpace(req.URL) == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing or invalid url")
		return
	}

	parsedURL, err := url.Parse(req.URL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid url format")
		return
	}
	if targetPrivate(r.Context(), parsedURL.Hostname()) {
		handlerutil.WriteJSON(w, http.StatusForbidden, map[string]any{
			"error": map[string]string{"message": "private IP blocked by SSRF protection", "type": "permission_denied", "code": "address_forbidden"},
		})
		return
	}

	// A provider named without a model is the model: /v1/web/fetch carries the
	// extractor in `model`, and a caller that sent only `provider` was being
	// dispatched to the default instead of the one it named.
	if req.Model == "" && req.Provider != "" {
		var rawMap map[string]any
		if err := json.Unmarshal(body, &rawMap); err == nil && rawMap != nil {
			rawMap["model"] = req.Provider
			if rebuilt, mErr := json.Marshal(rawMap); mErr == nil {
				body = rebuilt
			}
		}
	}
	h.forwardMediaRequest(w, r, body, "jina-reader", "/v1/web/fetch")
}

// targetPrivate reports whether host names an address the gateway must not
// fetch on a caller's behalf. A literal IP is judged directly; a name is
// resolved once, before dispatch. ALLOW_PRIVATE_NETWORKING is the operator's
// own switch and is answered first, so both branches agree: a literal IP and a
// name that resolves to the same address get the same verdict.
//
// This check is a refusal the caller can see, not the boundary itself: the
// dialer's socket-level guard is what actually stops the connection, and it
// also covers the rebind between this lookup and the dial. Checking here means
// the common case answers 403 with a reason instead of surfacing as a transport
// error from a provider several layers down.
func targetPrivate(ctx context.Context, host string) bool {
	if host == "" {
		return true
	}
	if proxy.IsPrivateNetworkingAllowed() {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return proxy.IsPrivateIP(ip)
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		// Unresolvable is not this check's call to make: let the dispatch
		// produce the transport error, which names the real problem.
		return false
	}
	for _, ip := range ips {
		if proxy.IsPrivateIP(ip) {
			return true
		}
	}
	return false
}

func (h *MediaHandler) forwardMediaRequest(w http.ResponseWriter, r *http.Request, body []byte, defaultModel, endpoint string) {
	model := defaultModel
	var reqBody struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &reqBody); err == nil && reqBody.Model != "" {
		model = reqBody.Model
	} else if isMultipartRequest(r) {
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
	// Per-API-key access control. This is the single funnel for images, TTS,
	// STT, video, search, scrape and fetch, so gating here covers all of them
	// with the same decision the chat lane uses. It runs before any connection
	// selection so a denied model never reaches a provider.
	//
	// It also runs before the media caps. A cap answers 413 with the limit and
	// the measured figure; sent to a key that may not call the model at all,
	// that is a policy detail the key has no business learning — the answer it
	// is owed is 403.
	if !h.ChatH.EnforceModelAccess(w, r, model) {
		return
	}
	// Media input caps and request-level usage, at the one funnel every media
	// lane passes through. This still sits before model resolution and before
	// connection selection, which is what matters: an oversized request never
	// reaches an upstream call, because the call is what gets billed.
	if _, refused := guardMediaInput(w, r.Header.Get("Content-Type"), body); refused {
		return
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
			// Gemini image models answer the native generateContent shape, not
			// the OpenAI images endpoint the generic forward would dial.
			if (endpoint == "/v1/images/generations" || endpoint == "/images/generations") && (subInfo.Provider == "gemini" || subInfo.Provider == "gm") {
				if err := h.handleGeminiImage(w, r, body, subInfo); err == nil {
					return
				} else {
					lastErr = err.Error()
					continue
				}
			}
			// Codex image models are Responses-API models, not an image endpoint.
			if (endpoint == "/v1/images/generations" || endpoint == "/images/generations") && (subInfo.Provider == "codex" || subInfo.Provider == "cx") {
				if err := h.handleCodexImage(w, r, body, subInfo); err == nil {
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
			// ElevenLabs Scribe: the vendor rejects the generic OpenAI-compatible
			// multipart body (model_id / language_code / xi-api-key).
			if (endpoint == "/v1/audio/transcriptions" || endpoint == "/audio/transcriptions") && (subInfo.Provider == "elevenlabs" || subInfo.Provider == "el") {
				if err := h.handleElevenLabsSTT(w, r, body, subInfo); err == nil {
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

			conn, connData, err := h.ChatH.GetBestConnectionWithContext(r.Context(), subInfo.Provider, subInfo.ConnectionID, nil, subInfo.Model)
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
			// The endpoint the caller used is a route on this gateway, not a
			// path on the vendor's host: appending it to BaseURL built URLs the
			// vendor never serves (tavily got api.tavily.com/search/v1/search,
			// serper got google.serper.dev/v1/search). Each vendor publishes its
			// own search/fetch URL and verb in the registry.
			targetURL := strings.TrimRight(providerCfg.BaseURL, "/") + endpoint
			method := r.Method
			switch endpoint {
			case "/v1/search":
				if providerCfg.SearchURL != "" {
					targetURL = providerCfg.SearchURL
					method = strings.ToUpper(providerCfg.SearchMethod)
					if method == "" {
						method = http.MethodPost
					}
				}
			case "/v1/web/fetch":
				if providerCfg.FetchURL != "" {
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
				// A cancelled caller is not a failed model. Trying the next
				// member of the combo would make a fresh upstream connection
				// for a client that has already gone.
				if r.Context().Err() != nil {
					handlerutil.WriteJSONError(w, 499, "client closed request")
					return
				}
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
			copyUpstreamHeaders(w.Header(), resp.Header)
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
	// Gemini image models answer the native generateContent shape, not the
	// OpenAI images endpoint the generic forward would dial.
	if (endpoint == "/v1/images/generations" || endpoint == "/images/generations") && (modelInfo.Provider == "gemini" || modelInfo.Provider == "gm") {
		if err := h.handleGeminiImage(w, r, body, modelInfo); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
		}
		return
	}
	// Codex image models are Responses-API models, not an image endpoint.
	if (endpoint == "/v1/images/generations" || endpoint == "/images/generations") && (modelInfo.Provider == "codex" || modelInfo.Provider == "cx") {
		if err := h.handleCodexImage(w, r, body, modelInfo); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
		}
		return
	}

	// UC web: the persona image API needs a minted Clerk session, not a bearer
	// key, so the generic forward cannot reach it.
	if (endpoint == "/v1/images/generations" || endpoint == "/images/generations") && (modelInfo.Provider == "uc-web" || modelInfo.Provider == "uc") {
		if err := h.handleUcWebImage(w, r, body, modelInfo); err != nil {
			writeUcMediaError(w, err)
		}
		return
	}

	// UC web video: generation posts to the persona video API and polls the
	// result URL, so it shares the image lane's session plumbing.
	if (endpoint == "/v1/videos/generations" || endpoint == "/videos/generations") && (modelInfo.Provider == "uc-web" || modelInfo.Provider == "uc") {
		if err := h.handleUcWebVideo(w, r, body, modelInfo); err != nil {
			writeUcMediaError(w, err)
		}
		return
	}

	// veoaifree.com free tier: WordPress AJAX with a scraped CSRF nonce, not
	// an OpenAI-compatible endpoint.
	if (endpoint == "/v1/images/generations" || endpoint == "/images/generations") && (modelInfo.Provider == "veoaifree-web" || modelInfo.Provider == "veo-free") {
		if err := h.handleVeoFreeWebImage(w, r, body, modelInfo); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
		}
		return
	}
	if (endpoint == "/v1/videos/generations" || endpoint == "/videos/generations") && (modelInfo.Provider == "veoaifree-web" || modelInfo.Provider == "veo-free") {
		if err := h.handleVeoFreeWebVideo(w, r, body, modelInfo); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
		}
		return
	}

	// grok.com Imagine: the Cloudflare-gated conversations/new endpoint and
	// the assets.grok.com download live in the chat executor's transport, so
	// the lane delegates there (upstream imageProvider useExecutor: true).
	if (endpoint == "/v1/images/generations" || endpoint == "/images/generations") && (modelInfo.Provider == "grok-web" || modelInfo.Provider == "gw") {
		if err := h.handleGrokWebImage(w, r, body, modelInfo); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
		}
		return
	}
	if (endpoint == "/v1/videos/generations" || endpoint == "/videos/generations") && (modelInfo.Provider == "grok-web" || modelInfo.Provider == "gw") {
		if err := h.handleGrokWebVideo(w, r, body, modelInfo); err != nil {
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

	// ElevenLabs Scribe: the vendor rejects the generic OpenAI-compatible
	// multipart body (model_id / language_code / xi-api-key).
	if (endpoint == "/v1/audio/transcriptions" || endpoint == "/audio/transcriptions") && (modelInfo.Provider == "elevenlabs" || modelInfo.Provider == "el") {
		if err := h.handleElevenLabsSTT(w, r, body, modelInfo); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
		}
		return
	}

	// Codex OAuth transcription: a fixed ChatGPT endpoint addressed with the
	// Codex account's own identity, so no connection URL override applies and
	// only the model "transcribe" is valid here.
	if isTranscriptionEndpoint(endpoint) && (modelInfo.Provider == "codex" || modelInfo.Provider == "cx") {
		if modelInfo.Model != codexSTTModel {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, codexSTTModelHintMsg)
			return
		}
		if err := h.handleCodexSTT(w, r, body, modelInfo); err != nil {
			var stErr *codexSTTError
			if errors.As(err, &stErr) {
				handlerutil.WriteJSONError(w, stErr.status, stErr.message)
			} else {
				handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
			}
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
	conn, connData, err := h.ChatH.GetBestConnectionWithContext(r.Context(), modelInfo.Provider, preferredConnID, nil, modelInfo.Model)
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
			handlerutil.WriteJSONError(w, http.StatusNotFound, mediaConnectionError(err, modelInfo.Provider))
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
	if !isMultipartRequest(r) {
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

	copyUpstreamHeaders(w.Header(), resp.Header)
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
		h.ChatH.LogUsage(r.Context(), logInfo, nil, latencyMs, body, nil)
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

// buildSystemoneURL joins a saved host override with the registry path
// without doubling a "/v1" or "/v1/systemone" the host already includes
// (upstream buildSystemoneUrl). An override with no registry path keeps the
// lane's "/systemone" suffix; one that already names the endpoint is used
// as-is.
func buildSystemoneURL(override, cfgURL, cfgPath string) string {
	base := strings.TrimRight(strings.TrimSpace(override), "/")
	if base == "" {
		return cfgURL
	}
	if strings.HasSuffix(base, "/systemone") {
		return base
	}
	if cfgPath == "" {
		return base + "/systemone"
	}
	path := strings.TrimRight(cfgPath, "/")
	if strings.HasSuffix(base, path) {
		return base
	}
	prefix := path[:strings.LastIndex(path, "/")]
	if prefix != "" && strings.HasSuffix(base, prefix) {
		return base + path[len(prefix):]
	}
	return base + path
}
