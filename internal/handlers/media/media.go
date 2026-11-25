package media

import (
	"9router/proxy/internal/constants"
	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/handlers/shared"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/middleware"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
	"9router/proxy/internal/usagetracker"
	"bytes"
	"context"
	"encoding/base64"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
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
	// Base is the guarded transport, not a clone of http.DefaultTransport: that
	// clone has no socket-level check at all, so the hostname pre-flight and the
	// redirect policy were the entire SSRF surface, and a name that resolves
	// public at check time and private at dial time walked straight through.
	// The guarded dialer judges the address it is about to connect to and still
	// honours ALLOW_PRIVATE_NETWORKING and the per-connection opt-in, so a LAN
	// provider keeps working.
	base := proxy.NewSafeTransport()
	base.ResponseHeaderTimeout = 2 * time.Minute
	transport := proxy.NewFallbackTransport(base)
	return &MediaHandler{
		Repo:       repo,
		Client:     &http.Client{Transport: transport, Timeout: 0, CheckRedirect: checkRedirectPublic},
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

// kindFromEndpoint infers the media serviceKind from the API endpoint.
func kindFromEndpoint(endpoint string) string {
	switch {
	case strings.HasPrefix(endpoint, "/v1/images/") || strings.HasPrefix(endpoint, "/images/"):
		return "image"
	case strings.HasPrefix(endpoint, "/v1/videos/") || strings.HasPrefix(endpoint, "/videos/"):
		return "video"
	case endpoint == "/v1/audio/speech" || endpoint == "/audio/speech":
		return "tts"
	case strings.HasPrefix(endpoint, "/v1/audio/") || strings.HasPrefix(endpoint, "/audio/"):
		return "stt"
	case endpoint == "/v1/embeddings" || endpoint == "/embeddings":
		return "embedding"
	case endpoint == "/v1/search" || endpoint == "/search":
		return "webSearch"
	case endpoint == "/v1/web/fetch" || endpoint == "/v1/scrape" || endpoint == "/scrape":
		return "webFetch"
	default:
		return "media"
	}
}

// IsConnectionMediaEligible checks if a connection is active and eligible for media routing.
func IsConnectionMediaEligible(conn *models.ProviderConnection, serviceKind ...string) bool {
	if conn == nil || conn.IsActive != 1 {
		return false
	}
	return conn.IsMediaActive(serviceKind...)
}

// SelectMediaConnections returns all active connections for provider eligible for media routing.
func (h *MediaHandler) SelectMediaConnections(provider string, serviceKind ...string) ([]*models.ProviderConnection, error) {
	if h.Repo == nil {
		return nil, nil
	}
	conns, err := h.Repo.GetProviderConnections(provider, true)
	if err != nil {
		return nil, err
	}
	var eligible []*models.ProviderConnection
	for _, c := range conns {
		if IsConnectionMediaEligible(c, serviceKind...) {
			eligible = append(eligible, c)
		}
	}
	return eligible, nil
}

type jsonMediaAttemptErr struct {
	status    int
	rawBody   []byte
	jsonMsg   string
	retryable bool
}

func (e *jsonMediaAttemptErr) write(w http.ResponseWriter) {
	if e.rawBody != nil {
		w.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
		w.WriteHeader(e.status)
		_, _ = w.Write(e.rawBody)
		return
	}
	handlerutil.WriteJSONError(w, e.status, e.jsonMsg)
}

func (h *MediaHandler) forwardJSONMediaLane(
	w http.ResponseWriter,
	r *http.Request,
	rawBody []byte,
	finalBody []byte,
	modelInfo *chat.ModelInfo,
	endpoint string,
	applyCopilotToken bool,
	buildURL func(string) string,
) {
	pinned := modelInfo.ConnectionID
	if pinned == "" {
		pinned = imagePinnedConnectionID(r)
	}
	usePinned := pinned != ""
	excludeIDs := []string{}
	tried := make(map[string]struct{})
	var lastErr *jsonMediaAttemptErr
	for {
		if r.Context().Err() != nil {
			if lastErr != nil {
				lastErr.write(w)
				return
			}
			handlerutil.WriteJSONError(w, http.StatusBadGateway, r.Context().Err().Error())
			return
		}
		conn, connData, err := h.ChatH.GetBestConnectionWithContext(r.Context(), modelInfo.Provider, pinned, excludeIDs, modelInfo.Model)
		if err != nil || conn == nil || connData == nil {
			if lastErr != nil {
				lastErr.write(w)
				return
			}
			handlerutil.WriteJSONError(w, http.StatusNotFound, mediaConnectionError(err, modelInfo.Provider))
			return
		}
		if _, seen := tried[conn.ID]; seen {
			if lastErr != nil {
				lastErr.write(w)
				return
			}
			handlerutil.WriteJSONError(w, http.StatusNotFound, mediaConnectionError(nil, modelInfo.Provider))
			return
		}
		tried[conn.ID] = struct{}{}

		attemptErr := h.tryJSONMediaLaneConn(w, r, rawBody, finalBody, modelInfo, endpoint, applyCopilotToken, buildURL, conn, connData)
		if attemptErr == nil {
			return
		}
		lastErr = attemptErr
		if usePinned || !attemptErr.retryable || r.Context().Err() != nil {
			lastErr.write(w)
			return
		}
		excludeIDs = append(excludeIDs, conn.ID)
	}
}

func (h *MediaHandler) tryJSONMediaLaneConn(
	w http.ResponseWriter,
	r *http.Request,
	rawBody []byte,
	finalBody []byte,
	modelInfo *chat.ModelInfo,
	endpoint string,
	applyCopilotToken bool,
	buildURL func(string) string,
	conn *models.ProviderConnection,
	connData *chat.ConnectionData,
) *jsonMediaAttemptErr {
	providerCfg, err := h.ChatH.GetProviderConfig(modelInfo.Provider, connData)
	if err != nil {
		return &jsonMediaAttemptErr{status: http.StatusInternalServerError, jsonMsg: err.Error(), retryable: false}
	}

	apiKey := chat.ExtractAPIKey(connData)
	if apiKey == "" {
		return &jsonMediaAttemptErr{status: http.StatusUnauthorized, jsonMsg: "no API key found", retryable: true}
	}

	targetURL := buildURL(providerCfg.BaseURL)
	// Cloudflare System One embeds the account and the (full) model id in the
	// path, and forwards a short selector in the body (see finalBody above).
	if strings.Contains(targetURL, "{accountId}") {
		accountID := ""
		if connData != nil {
			if aid, ok := connData.ProviderSpecificData["accountId"].(string); ok && aid != "" {
				accountID = aid
			}
		}
		if accountID == "" {
			accountID = strings.TrimSpace(os.Getenv("CLOUDFLARE_ACCOUNT_ID"))
		}
		targetURL = strings.Replace(targetURL, "{accountId}", accountID, 1)
	}
	targetURL = strings.ReplaceAll(targetURL, "{model}", modelInfo.Model)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, bytes.NewReader(finalBody))
	if err != nil {
		return &jsonMediaAttemptErr{status: http.StatusInternalServerError, jsonMsg: fmt.Sprintf("create request: %v", err), retryable: false}
	}

	req.Header.Set(constants.HeaderContentType, constants.ContentTypeJSON)
	handlerutil.SetAuthHeader(req, apiKey, providerCfg.AuthHeader, providerCfg.AuthScheme)
	for k, v := range providerCfg.StaticHeaders {
		if req.Header.Get(k) == "" {
			req.Header.Set(k, v)
		}
	}
	if applyCopilotToken && providers.ResolveAlias(modelInfo.Provider) == "github" {
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
		if copilotToken, ok := connData.ProviderSpecificData["copilotToken"].(string); ok && copilotToken != "" {
			req.Header.Set("Authorization", "Bearer "+copilotToken)
		}
	}

	// The connection's own client, not h.Client: an assigned proxy pool — a
	// CONNECT proxy or an edge relay — is the operator's egress choice, and this
	// lane used to skip it entirely and answer from the host's real IP.
	client, clientErr := h.ChatH.GetClientForConnection(connData)
	if clientErr != nil {
		return &jsonMediaAttemptErr{status: http.StatusBadGateway, jsonMsg: clientErr.Error(), retryable: true}
	}

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return &jsonMediaAttemptErr{
			status:    http.StatusBadGateway,
			jsonMsg:   fmt.Sprintf("upstream error: %v", err),
			retryable: r.Context().Err() == nil,
		}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if errBody == nil {
			errBody = []byte{}
		}
		snippet := string(errBody[:min(500, len(errBody))])
		backoff := 0
		if h.Repo != nil {
			backoff = h.Repo.GetConnectionBackoffLevel(conn.ID)
		}
		classification := providers.ClassifyErrorForProvider(resp.StatusCode, snippet, backoff, modelInfo.Provider, errBody)
		if classification.ShouldFallback && h.Repo != nil && !handlerutil.IsProbeContext(r.Context()) {
			cooldownSec := max(classification.CooldownMs/1000, 1)
			_ = h.Repo.LockConnectionModel(conn.ID, modelInfo.Model, cooldownSec, classification.NewBackoffLevel)
		}
		return &jsonMediaAttemptErr{
			status:    resp.StatusCode,
			rawBody:   errBody,
			retryable: classification.ShouldFallback,
		}
	}

	w.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)

	if h.Repo != nil {
		_ = h.Repo.UpdateConnectionLastUsed(conn.ID)
		if !handlerutil.IsProbeContext(r.Context()) {
			_ = h.Repo.UnlockConnectionModel(conn.ID, modelInfo.Model)
		}
	}
	latencyMs := time.Since(start).Milliseconds()
	logInfo := &shared.UsageLogInfo{
		Provider:     modelInfo.Provider,
		Model:        modelInfo.Model,
		ConnectionID: conn.ID,
		APIKey:       apiKey,
		Endpoint:     endpoint,
	}
	h.ChatH.LogUsage(r.Context(), logInfo, nil, latencyMs, rawBody, nil)
	return nil
}

// HandleEmbeddings forwards /v1/embeddings requests to upstream providers.
func (h *MediaHandler) HandleEmbeddings(w http.ResponseWriter, r *http.Request) {
	r = r.WithContext(handlerutil.WithServiceKind(r.Context(), "embedding"))
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer func() { _ = r.Body.Close() }()

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

	finalBody := handlerutil.UpdateModelInBody(body, modelInfo.Model)
	// Google Gemini embeddings forward dimensions as outputDimensionality (upstream #1366).
	if providers.ResolveAlias(modelInfo.Provider) == "gemini" {
		finalBody = forwardGeminiEmbeddingDimensions(finalBody)
	}
	if isGitHubCopilotProvider(modelInfo.Provider) {
		finalBody = wrapCopilotEmbeddingInput(finalBody)
	}
	// NVIDIA asymmetric embedding models require an input_type hint; symmetric
	// ones (nvclip) and non-embedding models (parakeet-ctc-1.1b-asr) must not
	// receive it. Gate on the exact registry set, not a suffix.
	if providers.ResolveAlias(modelInfo.Provider) == "nvidia" && nvidiaAsymmetricEmbeddingModels[modelInfo.Model] {
		finalBody = forwardNvidiaEmbeddingInputType(finalBody)
	}

	h.forwardJSONMediaLane(w, r, body, finalBody, modelInfo, "/embeddings", true, buildEmbeddingsURL)
}

// HandleRerank forwards /v1/rerank requests to upstream providers.
//
// Rerank is the same shape as embeddings — a model in, a JSON body out, no
// streaming — so it shares forwardJSONMediaLane with HandleEmbeddings and derives
// its /rerank endpoint via buildRerankURL.
func (h *MediaHandler) HandleRerank(w http.ResponseWriter, r *http.Request) {
	r = r.WithContext(handlerutil.WithServiceKind(r.Context(), "embedding"))
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer func() { _ = r.Body.Close() }()

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

	finalBody := handlerutil.UpdateModelInBody(body, modelInfo.Model)
	h.forwardJSONMediaLane(w, r, body, finalBody, modelInfo, "/v1/rerank", false, buildRerankURL)
}

// buildRerankURL derives the rerank lane from a provider's base URL. A base
// that already names a chat or embeddings lane is rewritten to the rerank
// sibling, so a connection saved with either spelling still resolves.
func buildRerankURL(baseURL string) string {
	for _, lane := range []string{"/chat/completions", "/embeddings"} {
		if strings.Contains(baseURL, lane) {
			return strings.Replace(baseURL, lane, "/rerank", 1)
		}
	}
	return strings.TrimRight(baseURL, "/") + "/rerank"
}

// HandleImages handles /v1/images/generations.
func (h *MediaHandler) HandleImages(w http.ResponseWriter, r *http.Request) {
	r = r.WithContext(handlerutil.WithServiceKind(r.Context(), "image"))
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
	r = r.WithContext(handlerutil.WithServiceKind(r.Context(), "tts"))
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
	r = r.WithContext(handlerutil.WithServiceKind(r.Context(), "systemone"))
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()
	var reqBody struct {
		Model     string         `json:"model"`
		State     any            `json:"state"`
		Questions map[string]any `json:"questions"`
		Fields    map[string]any `json:"fields"`
	}
	if err := json.Unmarshal(body, &reqBody); err != nil || reqBody.Model == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing model")
		return
	}
	provider := ""
	modelID := reqBody.Model
	if idx := strings.Index(reqBody.Model, "/"); idx >= 0 {
		provider = reqBody.Model[:idx]
		modelID = reqBody.Model[idx+1:]
	}
	shape := providers.GetProviderModelPayloadShape(provider, modelID)
	switch shape {
	case "fields":
		if reqBody.Fields == nil {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "Missing required field: fields")
			return
		}
	case "questions":
		if reqBody.Questions == nil {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "Missing required field: questions")
			return
		}
	default:
		if reqBody.Questions == nil {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "Missing required field: questions")
			return
		}
	}
	model := reqBody.Model
	// fields-only payloads (extraction models) do not require questions.
	if !h.ChatH.EnforceModelAccess(w, r, model) {
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
	h.forwardSystemoneRequest(w, r, body, model)
}

// forwardSystemoneRequest resolves model, rotates across candidate connections on
// retryable failures, and forwards the body to the provider systemone endpoint.
func (h *MediaHandler) forwardSystemoneRequest(w http.ResponseWriter, r *http.Request, body []byte, model string) {
	modelInfo, err := h.ChatH.ResolveModel(model)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	preferredConnID := preferredMediaConnID(r, modelInfo)
	usePinned := preferredConnID != ""
	finalModel := ""
	if h.Repo != nil {
		if customs, err := h.Repo.GetCustomModels(); err == nil {
			for _, cm := range customs {
				if cm != nil && (cm.ProviderAlias == modelInfo.Provider || providers.ResolveAlias(cm.ProviderAlias) == providers.ResolveAlias(modelInfo.Provider)) && cm.ID == modelInfo.Model && cm.UpstreamModelID != "" {
					finalModel = cm.UpstreamModelID
					break
				}
			}
		}
	}
	if finalModel == "" {
		finalModel = providers.GetProviderModelUpstreamID(modelInfo.Provider, modelInfo.Model)
	}
	finalBody := handlerutil.UpdateModelInBody(body, finalModel)

	excludeIDs := []string{}
	tried := make(map[string]struct{})
	var lastErr *mediaForwardAttemptErr

	for {
		if r.Context().Err() != nil {
			if lastErr != nil {
				lastErr.write(w)
				return
			}
			handlerutil.WriteJSONError(w, http.StatusBadGateway, r.Context().Err().Error())
			return
		}
		conn, connData, err := h.ChatH.GetBestConnectionWithContext(r.Context(), modelInfo.Provider, preferredConnID, excludeIDs, modelInfo.Model)
		if err != nil || connData == nil {
			if len(excludeIDs) == 0 {
				if cfg, ok := providers.KnownProviders[modelInfo.Provider]; ok && (cfg.NoAuth || cfg.DefaultAPIKey != "") {
					apiKey := cfg.DefaultAPIKey
					if apiKey == "" {
						apiKey = "public"
					}
					conn = nil
					connData = &chat.ConnectionData{
						APIKey:      apiKey,
						ProxyPoolID: h.ChatH.ResolveProviderProxyPoolID(modelInfo.Provider),
					}
				}
			}
			if connData == nil {
				if lastErr != nil {
					lastErr.write(w)
					return
				}
				// The selector's own error says whether the provider is simply
				// parked in cooldown and when it frees up. Substituting a generic
				// "no active connections" here throws that away, and the
				// dashboard's model sweep then cannot tell a cooldown from a
				// broken account.
				handlerutil.WriteJSONError(w, http.StatusNotFound, mediaConnectionError(err, modelInfo.Provider))
				return
			}
		}
		if conn != nil {
			if _, seen := tried[conn.ID]; seen {
				if lastErr != nil {
					lastErr.write(w)
					return
				}
				handlerutil.WriteJSONError(w, http.StatusNotFound, mediaConnectionError(nil, modelInfo.Provider))
				return
			}
			tried[conn.ID] = struct{}{}
		}

		attemptErr := h.trySystemoneConn(w, r, finalBody, modelInfo, conn, connData)
		if attemptErr == nil {
			return
		}
		lastErr = attemptErr
		if conn == nil || usePinned || !attemptErr.retryable || r.Context().Err() != nil {
			lastErr.write(w)
			return
		}
		excludeIDs = append(excludeIDs, conn.ID)
	}
}

func (h *MediaHandler) trySystemoneConn(
	w http.ResponseWriter,
	r *http.Request,
	finalBody []byte,
	modelInfo *chat.ModelInfo,
	conn *models.ProviderConnection,
	connData *chat.ConnectionData,
) *mediaForwardAttemptErr {
	providerCfg, err := h.ChatH.GetProviderConfig(modelInfo.Provider, connData)
	if err != nil {
		return &mediaForwardAttemptErr{status: http.StatusInternalServerError, jsonMsg: err.Error(), retryable: false}
	}
	apiKey := chat.ExtractAPIKey(connData)
	if apiKey == "" {
		if providerCfg != nil && providerCfg.DefaultAPIKey != "" {
			apiKey = providerCfg.DefaultAPIKey
		} else {
			return &mediaForwardAttemptErr{status: http.StatusUnauthorized, jsonMsg: "no API key found", retryable: conn != nil}
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
	// Cloudflare System One embeds the account and the (full) model id in the
	// path, and forwards a short selector in the body (see finalBody above).
	if strings.Contains(targetURL, "{accountId}") {
		accountID := ""
		if connData != nil {
			if aid, ok := connData.ProviderSpecificData["accountId"].(string); ok && aid != "" {
				accountID = aid
			}
		}
		if accountID == "" {
			accountID = strings.TrimSpace(os.Getenv("CLOUDFLARE_ACCOUNT_ID"))
		}
		targetURL = strings.Replace(targetURL, "{accountId}", accountID, 1)
	}
	targetURL = strings.ReplaceAll(targetURL, "{model}", modelInfo.Model)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, bytes.NewReader(finalBody))
	if err != nil {
		return &mediaForwardAttemptErr{status: http.StatusInternalServerError, jsonMsg: "failed to create request", retryable: false}
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
		} else if conn != nil {
			mediaSessionKey += ":conn:" + conn.ID
		}
		req.Header.Set("x-opencode-session", proxy.StableOpenCodeSessionID(mediaSessionKey))
	}
	client, clientErr := h.ChatH.GetClientForConnection(connData)
	if clientErr != nil {
		return &mediaForwardAttemptErr{status: http.StatusBadGateway, jsonMsg: clientErr.Error(), retryable: true}
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
					_ = resp.Body.Close()
				}
				resp = resp2
				err = nil
			}
		}
	}
	if err != nil {
		return &mediaForwardAttemptErr{
			status:    http.StatusBadGateway,
			jsonMsg:   "upstream request failed: " + err.Error(),
			retryable: r.Context().Err() == nil,
		}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if errBody == nil {
			errBody = []byte{}
		}
		backoff := 0
		if conn != nil && h.Repo != nil {
			backoff = h.Repo.GetConnectionBackoffLevel(conn.ID)
		}
		snippet := string(errBody[:min(500, len(errBody))])
		classification := providers.ClassifyErrorForProvider(resp.StatusCode, snippet, backoff, modelInfo.Provider, errBody)
		if classification.ShouldFallback && conn != nil && h.Repo != nil && !handlerutil.IsProbeContext(r.Context()) {
			cooldownSec := max(classification.CooldownMs/1000, 1)
			_ = h.Repo.LockConnectionModel(conn.ID, modelInfo.Model, cooldownSec, classification.NewBackoffLevel)
		}
		return &mediaForwardAttemptErr{
			status:    resp.StatusCode,
			headers:   resp.Header.Clone(),
			rawBody:   errBody,
			retryable: classification.ShouldFallback,
		}
	}

	copyUpstreamHeaders(w.Header(), resp.Header)
	const maxSystemoneBody = 16 << 20
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxSystemoneBody))
	if err != nil {
		return &mediaForwardAttemptErr{status: http.StatusBadGateway, jsonMsg: err.Error(), retryable: r.Context().Err() == nil}
	}
	outBody := respBody
	// Only attempt Cloudflare envelope unwrap when the body fit within the cap;
	// an oversized/truncated body falls back to passthrough without unwrapping.
	if len(respBody) < maxSystemoneBody && resp.StatusCode == http.StatusOK {
		var env struct {
			Result  jsonv1.RawMessage `json:"result"`
			Answers jsonv1.RawMessage `json:"answers"`
		}
		if err := json.Unmarshal(respBody, &env); err == nil && len(env.Result) > 0 {
			topHasAnswers := len(bytes.TrimSpace(env.Answers)) > 0 && string(bytes.TrimSpace(env.Answers)) != "null"
			if !topHasAnswers {
				var result struct {
					Answers jsonv1.RawMessage `json:"answers"`
				}
				if err := json.Unmarshal(env.Result, &result); err == nil {
					resHasAnswers := len(bytes.TrimSpace(result.Answers)) > 0 && string(bytes.TrimSpace(result.Answers)) != "null"
					if resHasAnswers {
						outBody = env.Result
						w.Header().Set("Content-Length", strconv.Itoa(len(outBody)))
					}
				}
			}
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(outBody)
	if resp.StatusCode == http.StatusOK && conn != nil && h.Repo != nil {
		_ = h.Repo.UpdateConnectionLastUsed(conn.ID)
		if !handlerutil.IsProbeContext(r.Context()) {
			_ = h.Repo.UnlockConnectionModel(conn.ID, modelInfo.Model)
		}
	}
	return nil
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

	preferredConnID := preferredMediaConnID(r, modelInfo)
	usePinned := preferredConnID != ""
	excludeIDs := []string{}
	tried := make(map[string]struct{})
	var lastErr *mediaForwardAttemptErr

	for {
		if r.Context().Err() != nil {
			if lastErr != nil {
				lastErr.write(w)
				return
			}
			handlerutil.WriteJSONError(w, http.StatusBadGateway, r.Context().Err().Error())
			return
		}
		conn, connData, err := h.ChatH.GetBestConnectionWithContext(r.Context(), modelInfo.Provider, preferredConnID, excludeIDs, modelInfo.Model)
		if err != nil || connData == nil {
			if lastErr != nil {
				lastErr.write(w)
				return
			}
			handlerutil.WriteJSONError(w, http.StatusNotFound, mediaConnectionError(err, modelInfo.Provider))
			return
		}
		if conn != nil {
			if _, seen := tried[conn.ID]; seen {
				if lastErr != nil {
					lastErr.write(w)
					return
				}
				handlerutil.WriteJSONError(w, http.StatusNotFound, mediaConnectionError(nil, modelInfo.Provider))
				return
			}
			tried[conn.ID] = struct{}{}
		}

		attemptErr := h.tryMiMoSpeechConn(w, r, payload, req.ResponseFmt, modelInfo, conn, connData)
		if attemptErr == nil {
			return
		}
		lastErr = attemptErr
		if conn == nil || usePinned || !attemptErr.retryable || r.Context().Err() != nil {
			lastErr.write(w)
			return
		}
		excludeIDs = append(excludeIDs, conn.ID)
	}
}

func (h *MediaHandler) tryMiMoSpeechConn(
	w http.ResponseWriter,
	r *http.Request,
	payload []byte,
	responseFmt string,
	modelInfo *chat.ModelInfo,
	conn *models.ProviderConnection,
	connData *chat.ConnectionData,
) *mediaForwardAttemptErr {
	providerCfg, err := h.ChatH.GetProviderConfig(modelInfo.Provider, connData)
	if err != nil {
		return &mediaForwardAttemptErr{status: http.StatusInternalServerError, jsonMsg: err.Error(), retryable: false}
	}
	apiKey := chat.ExtractAPIKey(connData)
	if apiKey == "" {
		return &mediaForwardAttemptErr{status: http.StatusUnauthorized, jsonMsg: "no API key found", retryable: conn != nil}
	}

	client, clientErr := h.ChatH.GetClientForConnection(connData)
	if clientErr != nil {
		return &mediaForwardAttemptErr{status: http.StatusBadGateway, jsonMsg: clientErr.Error(), retryable: true}
	}
	if client == nil {
		client = h.Client
	}

	upstreamReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, providerCfg.BaseURL, bytes.NewReader(payload))
	if err != nil {
		return &mediaForwardAttemptErr{status: http.StatusInternalServerError, jsonMsg: fmt.Sprintf("create request: %v", err), retryable: false}
	}
	upstreamReq.Header.Set(constants.HeaderContentType, constants.ContentTypeJSON)
	upstreamReq.Header.Set(constants.HeaderAuthorization, "Bearer "+apiKey)

	resp, err := client.Do(upstreamReq)
	if err != nil {
		return &mediaForwardAttemptErr{
			status:    http.StatusBadGateway,
			jsonMsg:   fmt.Sprintf("upstream error: %v", err),
			retryable: r.Context().Err() == nil,
		}
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return &mediaForwardAttemptErr{status: http.StatusBadGateway, jsonMsg: err.Error(), retryable: r.Context().Err() == nil}
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
		backoff := 0
		if conn != nil && h.Repo != nil {
			backoff = h.Repo.GetConnectionBackoffLevel(conn.ID)
		}
		snippet := string(respBody[:min(500, len(respBody))])
		classification := providers.ClassifyErrorForProvider(resp.StatusCode, snippet, backoff, modelInfo.Provider, respBody)
		if classification.ShouldFallback && conn != nil && h.Repo != nil && !handlerutil.IsProbeContext(r.Context()) {
			cooldownSec := max(classification.CooldownMs/1000, 1)
			_ = h.Repo.LockConnectionModel(conn.ID, modelInfo.Model, cooldownSec, classification.NewBackoffLevel)
		}
		return &mediaForwardAttemptErr{status: http.StatusBadGateway, jsonMsg: msg, retryable: classification.ShouldFallback}
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
		return &mediaForwardAttemptErr{status: http.StatusBadGateway, jsonMsg: msg, retryable: false}
	}

	// response_format=json returns {audio, format}; default returns raw audio bytes.
	if responseFmt == "json" {
		if conn != nil && h.Repo != nil {
			_ = h.Repo.UpdateConnectionLastUsed(conn.ID)
			if !handlerutil.IsProbeContext(r.Context()) {
				_ = h.Repo.UnlockConnectionModel(conn.ID, modelInfo.Model)
			}
		}
		handlerutil.WriteJSON(w, http.StatusOK, map[string]string{"audio": audio, "format": format})
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(audio)
	if err != nil {
		return &mediaForwardAttemptErr{status: http.StatusBadGateway, jsonMsg: "MiMo TTS returned invalid audio", retryable: false}
	}
	if conn != nil && h.Repo != nil {
		_ = h.Repo.UpdateConnectionLastUsed(conn.ID)
		if !handlerutil.IsProbeContext(r.Context()) {
			_ = h.Repo.UnlockConnectionModel(conn.ID, modelInfo.Model)
		}
	}
	w.Header().Set("Content-Type", "audio/"+format)
	w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
	return nil
}

// HandleAudioTranscriptions handles /v1/audio/transcriptions.
func (h *MediaHandler) HandleAudioTranscriptions(w http.ResponseWriter, r *http.Request) {
	r = r.WithContext(handlerutil.WithServiceKind(r.Context(), "stt"))
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
	r = r.WithContext(handlerutil.WithServiceKind(r.Context(), "video"))
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
	r = r.WithContext(handlerutil.WithServiceKind(r.Context(), "video"))
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
	r = r.WithContext(handlerutil.WithServiceKind(r.Context(), "video"))
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

// defaultWebSearchModel returns "search-combo" if available, then any webSearch combo, then falls back to tavily/search.
func (h *MediaHandler) defaultWebSearchModel() string {
	if h.Repo != nil {
		if combo, err := h.Repo.GetComboByName("search-combo"); err == nil && combo != nil && combo.Models != "" {
			return "search-combo"
		}
		if combos, err := h.Repo.GetCombos(); err == nil {
			for _, c := range combos {
				if c != nil && c.Kind != nil && *c.Kind == "webSearch" && c.Models != "" {
					return c.Name
				}
			}
		}
	}
	return "gpt-4o"
}

// defaultWebFetchModel returns "fetch-combo" if available, then any webFetch combo, then falls back to jina-reader.
func (h *MediaHandler) defaultWebFetchModel() string {
	if h.Repo != nil {
		if combo, err := h.Repo.GetComboByName("fetch-combo"); err == nil && combo != nil && combo.Models != "" {
			return "fetch-combo"
		}
		if combos, err := h.Repo.GetCombos(); err == nil {
			for _, c := range combos {
				if c != nil && c.Kind != nil && *c.Kind == "webFetch" && c.Models != "" {
					return c.Name
				}
			}
		}
	}
	return "jina-reader"
}

// HandleSearch handles /v1/search.
func (h *MediaHandler) HandleSearch(w http.ResponseWriter, r *http.Request) {
	r = r.WithContext(handlerutil.WithServiceKind(r.Context(), "webSearch"))
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()
	h.forwardMediaRequest(w, r, body, h.defaultWebSearchModel(), "/v1/search")
}

// HandleScrape handles /v1/scrape.
func (h *MediaHandler) HandleScrape(w http.ResponseWriter, r *http.Request) {
	r = r.WithContext(handlerutil.WithServiceKind(r.Context(), "webFetch"))
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()
	h.forwardMediaRequest(w, r, body, h.defaultWebFetchModel(), "/v1/scrape")
}

// HandleWebFetch handles /v1/web/fetch.
func (h *MediaHandler) HandleWebFetch(w http.ResponseWriter, r *http.Request) {
	r = r.WithContext(handlerutil.WithServiceKind(r.Context(), "webFetch"))
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
	h.forwardMediaRequest(w, r, body, h.defaultWebFetchModel(), "/v1/web/fetch")
}

// targetPrivate reports whether host names an address the gateway must not
// fetch on a caller's behalf. A literal IP is judged directly; a name is
// resolved once, before dispatch. ALLOW_PRIVATE_NETWORKING is the operator's
// own switch and is answered first, so both branches agree: a literal IP and a
// name that resolves to the same address get the same verdict.
//
// This check is a refusal the caller can see, not the boundary itself: while
// ALLOW_PRIVATE_NETWORKING=false the dialer's socket-level guard is what
// actually stops the connection, and it also covers the rebind between this
// lookup and the dial. With the default (on) that guard is skipped, so this
// pre-flight is the only check. Checking here means the common case answers
// 403 with a reason instead of surfacing as a transport error from a provider
// several layers down.
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

// maxRedirectHops restates net/http's default ceiling: CheckRedirect replaces
// the stdlib policy, so a chain that would have been cut at ten hops would
// otherwise loop until the request context died.
const maxRedirectHops = 10

// checkRedirectPublic is the CheckRedirect policy for every media client. A
// public entry point must not be able to walk the gateway into a private
// address with a 302: resolveSearchURL refuses a private search base URL before
// the first request, but the host it dialled can answer with a redirect to
// 127.0.0.1, 169.254.169.254 or an RFC1918 address, and with
// ALLOW_PRIVATE_NETWORKING on (the default) the dialer's socket-level guard is
// skipped, so this hop is the only refusal. Every hop is checked, not just the
// first: a chain of public hosts ending in loopback is the same fetch.
func checkRedirectPublic(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirectHops {
		return fmt.Errorf("stopped after %d redirects", maxRedirectHops)
	}
	if err := handlerutil.AssertPublicURL(req.URL.String()); err != nil {
		return fmt.Errorf("redirect refused: %s: %w", req.URL.Redacted(), err)
	}
	return nil
}

// guardMediaRedirects returns a copy of client that refuses a redirect hop
// landing on an internal address. A copy, because the client the chat handler
// hands out is shared by every lane, and one lane may not change the policy
// the others dial with.
func guardMediaRedirects(client *http.Client) *http.Client {
	clone := *client
	clone.CheckRedirect = checkRedirectPublic
	return &clone
}

// resolveSearchURL picks the search endpoint a provider dials. The provider's
// own configured base URL (registry default) is trusted as admin-controlled;
// anything else — a per-connection override stored by the dashboard, or a
// client-supplied baseUrl — is client-controlled and must be public http(s)
// only, otherwise a custom SearXNG (or any web search) base URL is an SSRF
// primitive: an attacker points it at 127.0.0.1:11434, 169.254.169.254 or a
// LAN host and the gateway fetches it on their behalf.
//
// Matches upstream resolveBaseUrl / assertPublicUrl: invalid URL, non-http(s)
// scheme, internal/private/loopback/link-local/metadata addresses are all
// refused. The guard runs here so a rejected override answers with a clear
// error instead of reaching the dialer (which under ALLOW_PRIVATE_NETWORKING=1
// — the default — lets every address class through).
func resolveSearchURL(cfg *providers.ProviderConfig, psd map[string]any, targetURL string) (string, error) {
	override := psdStr(psd, "baseUrl")
	if override != "" {
		u, err := url.Parse(override)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return "", fmt.Errorf("invalid search base URL: %q", override)
		}
		if u.Hostname() == "" {
			return "", fmt.Errorf("invalid search base URL: empty host")
		}
		if handlerutil.AssertPublicURL(override) != nil {
			return "", fmt.Errorf("search base URL is not allowed: %q", override)
		}
		return strings.TrimRight(override, "/"), nil
	}
	if cfg.SearchURL != "" {
		return cfg.SearchURL, nil
	}
	// No override and no configured search lane: keep the derived
	// targetURL (baseURL + endpoint) so generic search providers still
	// reach baseURL + /v1/search instead of an empty string.
	return targetURL, nil
}

// psdStr reads a string field from provider-specific data, mirroring the
// dashboard's copy so the media handler does not import the dashboard package.
func psdStr(psd map[string]any, keys ...string) string {
	if psd == nil {
		return ""
	}
	for _, k := range keys {
		if v, ok := psd[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func (h *MediaHandler) forwardMediaRequest(w http.ResponseWriter, r *http.Request, body []byte, defaultModel, endpoint string) {
	if handlerutil.GetServiceKind(r.Context()) == "" {
		r = r.WithContext(handlerutil.WithServiceKind(r.Context(), kindFromEndpoint(endpoint)))
	}
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
	if (endpoint == "/v1/search" || endpoint == "/v1/scrape") && (model == "search" || model == "default" || model == "web" || model == "webSearch") {
		model = h.defaultWebSearchModel()
	}
	if endpoint == "/v1/web/fetch" && (model == "fetch" || model == "default" || model == "web" || model == "webFetch") {
		model = h.defaultWebFetchModel()
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
			if (endpoint == "/v1/images/generations" || endpoint == "/images/generations" ||
				endpoint == "/v1/images/edits" || endpoint == "/images/edits") && (subInfo.Provider == "antigravity" || subInfo.Provider == "ag") {
				if err := h.handleAntigravityImage(w, r, body, subInfo); err == nil {
					return
				} else {
					lastErr = err.Error()
					continue
				}
			}
			// Gemini image models answer the native generateContent shape, not
			// the OpenAI images endpoint the generic forward would dial.
			if (endpoint == "/v1/images/generations" || endpoint == "/images/generations" ||
				endpoint == "/v1/images/edits" || endpoint == "/images/edits") && (subInfo.Provider == "gemini" || subInfo.Provider == "gm") {
				if err := h.handleGeminiImage(w, r, body, subInfo); err == nil {
					return
				} else {
					lastErr = err.Error()
					continue
				}
			}
			// Codex image models are Responses-API models, not an image endpoint.
			if (endpoint == "/v1/images/generations" || endpoint == "/images/generations" ||
				endpoint == "/v1/images/edits" || endpoint == "/images/edits") && (subInfo.Provider == "codex" || subInfo.Provider == "cx") {
				if err := h.handleCodexImage(w, r, body, subInfo); err == nil {
					return
				} else {
					lastErr = err.Error()
					continue
				}
			}
			// Cloudflare AI image models answer /ai/run/<model>, which the
			// generic forwarder cannot reach — dedicated lane for all of them.
			if (endpoint == "/v1/images/generations" || endpoint == "/images/generations" ||
				endpoint == "/v1/images/edits" || endpoint == "/images/edits") && isCloudflareImageModel(subInfo) {
				if err := h.handleCloudflareImage(w, r, body, subInfo); err == nil {
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
			if isVideoCreateEndpoint(endpoint) && isXAIVideoProvider(subInfo.Provider) {
				finalBody = formatXAIVideoPayload(finalBody)
			}
			// Cloudflare AI's flux-1-schnell schema rejects width/height — handled
			// by the cloudflare image lane, not here.

			// The endpoint the caller used is a route on this gateway, not a
			// path on the vendor's host: appending it to BaseURL built URLs the
			// vendor never serves (tavily got api.tavily.com/search/v1/search,
			// serper got google.serper.dev/v1/search). Each vendor publishes its
			// own search/fetch URL and verb in the registry.
			targetURL := strings.TrimRight(providerCfg.BaseURL, "/") + endpoint
			method := r.Method
			switch endpoint {
			case "/v1/search":
				targetURL, err = resolveSearchURL(providerCfg, connData.ProviderSpecificData, targetURL)
				if err != nil {
					lastErr = err.Error()
					continue
				}
				method = strings.ToUpper(providerCfg.SearchMethod)
				if method == "" {
					method = http.MethodPost
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
			// The client is the chat handler's shared one; guard its redirects
			// so a combo search that answers 302 to a private address is
			// refused rather than followed.
			client = guardMediaRedirects(client)
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
			defer func() { _ = resp.Body.Close() }()
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
	if (endpoint == "/v1/images/generations" || endpoint == "/images/generations" ||
		endpoint == "/v1/images/edits" || endpoint == "/images/edits") && (modelInfo.Provider == "antigravity" || modelInfo.Provider == "ag") {
		if err := h.handleAntigravityImage(w, r, body, modelInfo); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
		}
		return
	}
	// Gemini image models answer the native generateContent shape, not the
	// OpenAI images endpoint the generic forward would dial.
	if (endpoint == "/v1/images/generations" || endpoint == "/images/generations" ||
		endpoint == "/v1/images/edits" || endpoint == "/images/edits") && (modelInfo.Provider == "gemini" || modelInfo.Provider == "gm") {
		if err := h.handleGeminiImage(w, r, body, modelInfo); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
		}
		return
	}
	// Codex image models are Responses-API models, not an image endpoint.
	if (endpoint == "/v1/images/generations" || endpoint == "/images/generations" ||
		endpoint == "/v1/images/edits" || endpoint == "/images/edits") && (modelInfo.Provider == "codex" || modelInfo.Provider == "cx") {
		if err := h.handleCodexImage(w, r, body, modelInfo); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
		}
		return
	}

	// Cloudflare AI image models answer /ai/run/<model>, which the generic
	// forwarder cannot reach — dedicated lane for all of them.
	if (endpoint == "/v1/images/generations" || endpoint == "/images/generations" ||
		endpoint == "/v1/images/edits" || endpoint == "/images/edits") && isCloudflareImageModel(modelInfo) {
		if err := h.handleCloudflareImage(w, r, body, modelInfo); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
		}
		return
	}
	// UC web: the persona image API needs a minted Clerk session, not a bearer
	// key, so the generic forward cannot reach it.
	if (endpoint == "/v1/images/generations" || endpoint == "/images/generations" ||
		endpoint == "/v1/images/edits" || endpoint == "/images/edits") && (modelInfo.Provider == "uc-web" || modelInfo.Provider == "uc") {
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
	if (endpoint == "/v1/images/generations" || endpoint == "/images/generations" ||
		endpoint == "/v1/images/edits" || endpoint == "/images/edits") && (modelInfo.Provider == "veoaifree-web" || modelInfo.Provider == "veo-free") {
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
	if (endpoint == "/v1/images/generations" || endpoint == "/images/generations" ||
		endpoint == "/v1/images/edits" || endpoint == "/images/edits") && (modelInfo.Provider == "grok-web" || modelInfo.Provider == "gw") {
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

	preferredConnID := preferredMediaConnID(r, modelInfo)

	baseFinalBody := body
	if !isMultipartRequest(r) {
		baseFinalBody = handlerutil.UpdateModelInBody(body, modelInfo.Model)
	} else if model != "" && model != modelInfo.Model {
		// Upstream parity (videoGeneration byte-exact multipart): never
		// rewrite inside the binary payload — a byte.Replace could corrupt
		// file bytes that happen to match. Rebuild only the model part.
		if rebuilt, err := rewriteMultipartModelPart(body, r.Header.Get("Content-Type"), model, modelInfo.Model); err == nil {
			baseFinalBody = rebuilt
		} else {
			log.Warn("media", "multipart model rewrite skipped", "endpoint", endpoint, "error", err)
		}
	}
	if isVideoCreateEndpoint(endpoint) && isXAIVideoProvider(modelInfo.Provider) && !isMultipartRequest(r) {
		baseFinalBody = formatXAIVideoPayload(baseFinalBody)
	}

	usePinned := preferredConnID != ""
	excludeIDs := []string{}
	tried := make(map[string]struct{})
	var lastErr *mediaForwardAttemptErr

	for {
		if r.Context().Err() != nil {
			if lastErr != nil {
				lastErr.write(w)
				return
			}
			handlerutil.WriteJSONError(w, http.StatusBadGateway, r.Context().Err().Error())
			return
		}
		conn, connData, err := h.ChatH.GetBestConnectionWithContext(r.Context(), modelInfo.Provider, preferredConnID, excludeIDs, modelInfo.Model)
		if err != nil || connData == nil {
			if len(excludeIDs) == 0 {
				if cfg, ok := providers.KnownProviders[modelInfo.Provider]; ok && (cfg.NoAuth || cfg.DefaultAPIKey != "") {
					apiKey := cfg.DefaultAPIKey
					if apiKey == "" {
						apiKey = "public"
					}
					conn = nil
					connData = &chat.ConnectionData{
						APIKey:      apiKey,
						ProxyPoolID: h.ChatH.ResolveProviderProxyPoolID(modelInfo.Provider),
					}
				}
			}
			if connData == nil {
				if lastErr != nil {
					lastErr.write(w)
					return
				}
				log.Warn("media", "no connection", "endpoint", endpoint, "provider", modelInfo.Provider, "model", modelInfo.Model, "error", err)
				handlerutil.WriteJSONError(w, http.StatusNotFound, mediaConnectionError(err, modelInfo.Provider))
				return
			}
		}
		if conn != nil {
			if _, seen := tried[conn.ID]; seen {
				if lastErr != nil {
					lastErr.write(w)
					return
				}
				handlerutil.WriteJSONError(w, http.StatusNotFound, mediaConnectionError(nil, modelInfo.Provider))
				return
			}
			tried[conn.ID] = struct{}{}
		}

		attemptErr := h.tryMediaForwardConn(w, r, body, baseFinalBody, modelInfo, endpoint, conn, connData)
		if attemptErr == nil {
			return
		}
		lastErr = attemptErr
		if conn == nil || usePinned || !attemptErr.retryable || r.Context().Err() != nil {
			lastErr.write(w)
			return
		}
		excludeIDs = append(excludeIDs, conn.ID)
	}
}

func preferredMediaConnID(r *http.Request, modelInfo *chat.ModelInfo) string {
	if id := r.Header.Get("x-connection-id"); id != "" {
		return id
	}
	if id := r.Header.Get("x-provider-connection-id"); id != "" {
		return id
	}
	return modelInfo.ConnectionID
}

type mediaForwardAttemptErr struct {
	status    int
	headers   http.Header
	rawBody   []byte
	jsonMsg   string
	retryable bool
}

func (e *mediaForwardAttemptErr) write(w http.ResponseWriter) {
	if e.rawBody != nil {
		if e.headers != nil {
			copyUpstreamHeaders(w.Header(), e.headers)
		}
		w.WriteHeader(e.status)
		_, _ = w.Write(e.rawBody)
		return
	}
	handlerutil.WriteJSONError(w, e.status, e.jsonMsg)
}

func (h *MediaHandler) tryMediaForwardConn(
	w http.ResponseWriter,
	r *http.Request,
	body []byte,
	baseFinalBody []byte,
	modelInfo *chat.ModelInfo,
	endpoint string,
	conn *models.ProviderConnection,
	connData *chat.ConnectionData,
) (attemptErr *mediaForwardAttemptErr) {
	providerCfg, err := h.ChatH.GetProviderConfig(modelInfo.Provider, connData)
	if err != nil {
		log.Error("media", "get provider config failed", "endpoint", endpoint, "provider", modelInfo.Provider, "error", err)
		return &mediaForwardAttemptErr{status: http.StatusInternalServerError, jsonMsg: err.Error(), retryable: false}
	}

	connIDStr := ""
	if conn != nil {
		connIDStr = conn.ID[:min(8, len(conn.ID))]
	}
	apiKey := chat.ExtractAPIKey(connData)
	if apiKey == "" {
		if providerCfg != nil && providerCfg.DefaultAPIKey != "" {
			apiKey = providerCfg.DefaultAPIKey
		} else {
			log.Warn("media", "no api key", "endpoint", endpoint, "provider", modelInfo.Provider, "conn", connIDStr)
			return &mediaForwardAttemptErr{status: http.StatusUnauthorized, jsonMsg: "no API key found", retryable: conn != nil}
		}
	}

	client, clientErr := h.ChatH.GetClientForConnection(connData)
	if clientErr != nil {
		return &mediaForwardAttemptErr{status: http.StatusBadGateway, jsonMsg: clientErr.Error(), retryable: true}
	}
	// The client is the chat handler's shared one; the guard rides on a copy
	// so a redirect on this fetch cannot walk into a private address.
	client = guardMediaRedirects(client)

	connID := ""
	if conn != nil {
		connID = conn.ID
	}
	usagetracker.GetTracker().TrackPending(modelInfo.Model, modelInfo.Provider, connID, true, false)
	defer func() {
		usagetracker.GetTracker().TrackPending(modelInfo.Model, modelInfo.Provider, connID, false, attemptErr != nil)
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
	case "/v1/images/generations", "/images/generations", "/v1/images/edits", "/images/edits":
		if !hasCustomBaseURL && providerCfg.ImageURL != "" {
			targetURL = providerCfg.ImageURL
		} else {
			targetURL = strings.TrimSuffix(baseURL, "/v1") + endpoint
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
	if endpoint == "/v1/search" {
		searchURL, err := resolveSearchURL(providerCfg, connData.ProviderSpecificData, targetURL)
		if err != nil {
			return &mediaForwardAttemptErr{status: http.StatusBadRequest, jsonMsg: err.Error(), retryable: false}
		}
		targetURL = searchURL
		if providerCfg.SearchMethod != "" {
			method = strings.ToUpper(providerCfg.SearchMethod)
		} else {
			method = http.MethodPost
		}
	}

	attemptBody := baseFinalBody
	if endpoint == "/v1/search" && modelInfo.Provider == "keenable" {
		// Keenable rejects unknown params (model, mode). Forward only query.
		var sReq struct {
			Query string `json:"query"`
			Mode  string `json:"mode"`
		}
		if err := json.Unmarshal(body, &sReq); err == nil && sReq.Query != "" {
			if b, err := json.Marshal(map[string]string{"query": sReq.Query}); err == nil {
				attemptBody = b
			}
		}
	}
	var fetchFmt string
	if endpoint == "/v1/web/fetch" && providerCfg.FetchURL != "" {
		targetURL = providerCfg.FetchURL
		if providerCfg.FetchMethod != "" {
			method = providerCfg.FetchMethod
		} else {
			method = "POST"
		}
		var fReq struct {
			URL    string `json:"url"`
			Format string `json:"format"`
		}
		_ = json.Unmarshal(body, &fReq)
		fetchFmt = strings.ToLower(strings.TrimSpace(fReq.Format))
		if fetchFmt == "" {
			fetchFmt = "markdown"
		}

		switch modelInfo.Provider {
		case "ollama":
			if fReq.URL != "" {
				if b, err := json.Marshal(map[string]string{"url": fReq.URL}); err == nil {
					attemptBody = b
				}
			}
		case "jina-reader", "jina":
			if fetchFmt != "markdown" && fetchFmt != "text" && fetchFmt != "html" {
				rawErr, _ := json.Marshal(map[string]any{
					"error": fmt.Sprintf("Unsupported format for %s. Supported: markdown, text, html", modelInfo.Provider),
				})
				return &mediaForwardAttemptErr{
					status:    http.StatusBadRequest,
					headers:   http.Header{constants.HeaderContentType: {constants.ContentTypeJSON}},
					rawBody:   rawErr,
					retryable: false,
				}
			}
			if fReq.URL != "" {
				if b, err := json.Marshal(map[string]string{"url": fReq.URL}); err == nil {
					attemptBody = b
				}
			}
		case "tavily":
			if fetchFmt != "markdown" && fetchFmt != "text" {
				rawErr, _ := json.Marshal(map[string]any{
					"error": "Unsupported format for tavily. Supported: markdown, text",
				})
				return &mediaForwardAttemptErr{
					status:    http.StatusBadRequest,
					headers:   http.Header{constants.HeaderContentType: {constants.ContentTypeJSON}},
					rawBody:   rawErr,
					retryable: false,
				}
			}
			tBody := map[string]any{
				"urls":          []string{fReq.URL},
				"extract_depth": "basic",
				"format":        fetchFmt,
			}
			if b, err := json.Marshal(tBody); err == nil {
				attemptBody = b
			}
		case "keenable":
			// Keenable fetch is GET /v1/fetch with url as a query parameter.
			if method == http.MethodGet && fReq.URL != "" {
				u, err := url.Parse(providerCfg.FetchURL)
				if err == nil {
					q := u.Query()
					q.Set("url", fReq.URL)
					if fReq.Format != "" {
						q.Set("format", fReq.Format)
					}
					u.RawQuery = q.Encode()
					targetURL = u.String()
				}
				attemptBody = nil
			}
		}
	}

	req, err := http.NewRequestWithContext(r.Context(), method, targetURL, bytes.NewReader(attemptBody))
	if err != nil {
		log.Error("media", "create request failed", "endpoint", endpoint, "error", err)
		return &mediaForwardAttemptErr{status: http.StatusInternalServerError, jsonMsg: "failed to create request", retryable: false}
	}

	req.Header = r.Header.Clone()
	req.Header.Del("Host")
	req.Header.Del("Content-Length")
	for k, v := range providerCfg.StaticHeaders {
		req.Header.Set(k, v)
	}
	if fetchFmt != "" && (modelInfo.Provider == "jina-reader" || modelInfo.Provider == "jina") {
		req.Header.Set("x-respond-with", fetchFmt)
	}
	handlerutil.SetAuthHeader(req, apiKey, providerCfg.AuthHeader, providerCfg.AuthScheme)

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
		return &mediaForwardAttemptErr{
			status:    http.StatusBadGateway,
			jsonMsg:   "upstream request failed",
			retryable: r.Context().Err() == nil && !isVideoCreateEndpoint(endpoint),
		}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if errBody == nil {
			errBody = []byte{}
		}
		log.Warn("media", "upstream error", "endpoint", endpoint, "provider", modelInfo.Provider, "model", modelInfo.Model, "conn", connIDStr, "status", resp.StatusCode, "body", string(errBody[:min(1024, len(errBody))]))
		backoff := 0
		if conn != nil && h.Repo != nil {
			backoff = h.Repo.GetConnectionBackoffLevel(conn.ID)
		}
		snippet := string(errBody[:min(500, len(errBody))])
		classification := providers.ClassifyErrorForProvider(resp.StatusCode, snippet, backoff, modelInfo.Provider, errBody)
		shouldRotate := classification.ShouldFallback
		if isVideoCreateEndpoint(endpoint) && resp.StatusCode >= 500 {
			shouldRotate = false
		}
		if shouldRotate && conn != nil && h.Repo != nil && !handlerutil.IsProbeContext(r.Context()) {
			cooldownSec := max(classification.CooldownMs/1000, 1)
			_ = h.Repo.LockConnectionModel(conn.ID, modelInfo.Model, cooldownSec, classification.NewBackoffLevel)
		}
		return &mediaForwardAttemptErr{
			status:    resp.StatusCode,
			headers:   resp.Header.Clone(),
			rawBody:   errBody,
			retryable: shouldRotate,
		}
	}

	copyUpstreamHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)

	if resp.StatusCode == http.StatusOK && conn != nil {
		if h.Repo != nil {
			_ = h.Repo.UpdateConnectionLastUsed(conn.ID)
			if !handlerutil.IsProbeContext(r.Context()) {
				_ = h.Repo.UnlockConnectionModel(conn.ID, modelInfo.Model)
			}
		}
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
	return nil
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

// forwardGeminiEmbeddingDimensions maps incoming OpenAI "dimensions" to
// Google Gemini's "outputDimensionality" property (upstream decolua/9router#1366 parity).
func forwardGeminiEmbeddingDimensions(body []byte) []byte {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}
	dimsRaw, exists := payload["dimensions"]
	if !exists || dimsRaw == nil {
		return body
	}
	dims := 0
	switch v := dimsRaw.(type) {
	case float64:
		dims = int(v)
	case int:
		dims = v
	case int64:
		dims = int(v)
	}
	if dims <= 0 {
		return body
	}
	payload["outputDimensionality"] = dims
	if updated, err := json.Marshal(payload); err == nil {
		return updated
	}
	return body
}

// isGitHubCopilotProvider reports whether the resolved provider is GitHub Copilot.
func isGitHubCopilotProvider(provider string) bool {
	return providers.ResolveAlias(provider) == "github"
}

// wrapCopilotEmbeddingInput wraps a bare "input" string into an array of
// strings ([input]), which Copilot requires. It leaves an existing array
// untouched and leaves bodies without "input" alone.
func wrapCopilotEmbeddingInput(body []byte) []byte {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil || payload == nil {
		return body
	}
	input, ok := payload["input"]
	if !ok {
		return body
	}
	if _, isArray := input.([]any); isArray {
		return body
	}
	payload["input"] = []any{input}
	out, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return out
}

// nvidiaAsymmetricEmbeddingModels lists NVIDIA embedding models that require an
// input_type hint (asymmetric query/passage). Symmetric models (nvclip) and
// non-embedding models (parakeet-ctc-1.1b-asr) are intentionally excluded.
var nvidiaAsymmetricEmbeddingModels = map[string]bool{
	"nvidia/nemotron-3.5-embed-8b":                   true,
	"nemotron-3.5-embed-8b":                          true,
	"nvidia/nemotron-3.5-embed-1b":                   true,
	"nemotron-3.5-embed-1b":                          true,
	"nvidia/nemotron-3-embed-1b":                     true,
	"nemotron-3-embed-1b":                            true,
	"nvidia/nv-embedqa-mistral-7b-v2":                true,
	"nv-embedqa-mistral-7b-v2":                       true,
	"nvidia/embed-qa-4":                              true,
	"embed-qa-4":                                     true,
	"nvidia/llama-3.2-nemoretriever-1b-vlm-embed-v1": true,
	"llama-3.2-nemoretriever-1b-vlm-embed-v1":        true,
	"nvidia/llama-3.2-nv-embedqa-1b-v1":              true,
	"llama-3.2-nv-embedqa-1b-v1":                     true,
}

// forwardNvidiaEmbeddingInputType injects input_type for NVIDIA's asymmetric
// embedding models: preserve a client-supplied "query"/"passage", otherwise
// default to "query" (upstream decolua/9router@a851a89c4).
func forwardNvidiaEmbeddingInputType(body []byte) []byte {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil || payload == nil {
		return body
	}
	raw, _ := payload["input_type"].(string)
	val := strings.TrimSpace(strings.ToLower(raw))
	if val != "query" && val != "passage" {
		val = "query"
	}
	payload["input_type"] = val
	out, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return out
}

// isXAIVideoProvider reports whether the resolved provider is xAI or grok-cli.
func isXAIVideoProvider(provider string) bool {
	canon := providers.ResolveAlias(provider)
	return canon == "xai" || canon == "grok-cli"
}

// formatXAIVideoPayload ensures the start frame is formatted as { "url": ... }
// for xAI video generation and edits, and forwards reference_images when present.
// xAI API fails with HTTP 422 if image is a bare string.
func formatXAIVideoPayload(body []byte) []byte {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil || payload == nil {
		return body
	}

	modified := false
	if img, ok := payload["image"]; ok {
		switch v := img.(type) {
		case string:
			payload["image"] = map[string]any{"url": v}
			modified = true
		case map[string]any:
			// Already an object (e.g. {"url": ...} or {"file_id": ...}), keep untouched
		default:
			// If invalid type like number/boolean, delete it so xAI doesn't error on type
			delete(payload, "image")
			modified = true
		}
	}

	if refImgs, ok := payload["reference_images"]; ok {
		if _, isSlice := refImgs.([]any); !isSlice {
			delete(payload, "reference_images")
			modified = true
		}
	}

	if !modified {
		return body
	}
	out, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return out
}
