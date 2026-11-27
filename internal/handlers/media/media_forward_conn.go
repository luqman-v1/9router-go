package media

import (
	"bytes"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/handlers/shared"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/usagetracker"
)

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
			backoff = h.Repo.GetConnectionBackoffLevel(r.Context(), conn.ID)
		}
		snippet := string(errBody[:min(500, len(errBody))])
		classification := providers.ClassifyErrorForProvider(resp.StatusCode, snippet, backoff, modelInfo.Provider, errBody)
		shouldRotate := classification.ShouldFallback
		if isVideoCreateEndpoint(endpoint) && resp.StatusCode >= 500 {
			shouldRotate = false
		}
		if shouldRotate && conn != nil && h.Repo != nil && !handlerutil.IsProbeContext(r.Context()) {
			cooldownSec := max(classification.CooldownMs/1000, 1)
			_ = h.Repo.LockConnectionModel(r.Context(), conn.ID, modelInfo.Model, cooldownSec, classification.NewBackoffLevel)
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
				_ = h.Repo.UnlockConnectionModel(r.Context(), conn.ID, modelInfo.Model)
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
		if errors.Is(err, io.EOF) {
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
