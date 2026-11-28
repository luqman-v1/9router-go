package media

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/handlers/shared"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
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
	case endpoint == "/v1/embeddings" || endpoint == "/embeddings" ||
		endpoint == "/v1/multimodal-embeddings" || endpoint == "/multimodal-embeddings":
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
			backoff = h.Repo.GetConnectionBackoffLevel(r.Context(), conn.ID)
		}
		classification := providers.ClassifyErrorForProvider(resp.StatusCode, snippet, backoff, modelInfo.Provider, errBody)
		if classification.ShouldFallback && h.Repo != nil && !handlerutil.IsProbeContext(r.Context()) {
			cooldownSec := max(classification.CooldownMs/1000, 1)
			_ = h.Repo.LockConnectionModel(r.Context(), conn.ID, modelInfo.Model, cooldownSec, classification.NewBackoffLevel)
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
	h.ChatH.LogUsage(r.Context(), logInfo, nil, latencyMs, rawBody, nil)
	return nil
}
