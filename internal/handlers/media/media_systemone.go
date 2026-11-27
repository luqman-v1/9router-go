package media

import (
	"bytes"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
)

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
			backoff = h.Repo.GetConnectionBackoffLevel(r.Context(), conn.ID)
		}
		snippet := string(errBody[:min(500, len(errBody))])
		classification := providers.ClassifyErrorForProvider(resp.StatusCode, snippet, backoff, modelInfo.Provider, errBody)
		if classification.ShouldFallback && conn != nil && h.Repo != nil && !handlerutil.IsProbeContext(r.Context()) {
			cooldownSec := max(classification.CooldownMs/1000, 1)
			_ = h.Repo.LockConnectionModel(r.Context(), conn.ID, modelInfo.Model, cooldownSec, classification.NewBackoffLevel)
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
			_ = h.Repo.UnlockConnectionModel(r.Context(), conn.ID, modelInfo.Model)
		}
	}
	return nil
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
