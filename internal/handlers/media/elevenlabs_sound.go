package media

import (
	"bytes"
	json "encoding/json/v2"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/handlers/shared"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
)

// SoundGenerationRequest is the caller's body for /v1/audio/sound-generation.
type SoundGenerationRequest struct {
	Text            string   `json:"text"`
	DurationSeconds *float64 `json:"duration_seconds,omitempty"`
	PromptInfluence *float64 `json:"prompt_influence,omitempty"`
	Model           string   `json:"model,omitempty"`
}

// elevenLabsSoundGenerationURL picks the vendor endpoint a request goes to.
// A connection that named its own host wins; otherwise the provider registry's
// host is used, with the sound-generation path appended.
func elevenLabsSoundGenerationURL(connData *chat.ConnectionData) string {
	if connData != nil && strings.TrimSpace(connData.BaseURL) != "" {
		return strings.TrimRight(strings.TrimSpace(connData.BaseURL), "/") + "/v1/sound-generation"
	}
	return "https://api.elevenlabs.io/v1/sound-generation"
}

type elevenLabsSoundAttemptErr struct {
	status    int
	rawBody   []byte
	jsonMsg   string
	retryable bool
}

func (e *elevenLabsSoundAttemptErr) write(w http.ResponseWriter) {
	if e.rawBody != nil {
		w.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
		w.WriteHeader(e.status)
		_, _ = w.Write(e.rawBody)
		return
	}
	handlerutil.WriteJSONError(w, e.status, e.jsonMsg)
}

// HandleAudioSoundGeneration generates a sound effect via the ElevenLabs
// sound-generation API and streams the raw audio back to the caller.
func (h *MediaHandler) HandleAudioSoundGeneration(w http.ResponseWriter, r *http.Request) {
	defer func() {
		_ = r.Body.Close()
	}()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	var req SoundGenerationRequest
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing required field: text")
		return
	}

	provider := "elevenlabs"
	if req.Model != "" {
		if name, _, found := strings.Cut(req.Model, "/"); found {
			provider = name
		}
	}
	model := "sound-generation"
	if req.Model != "" {
		_, modelName, found := strings.Cut(req.Model, "/")
		if found && strings.TrimSpace(modelName) != "" {
			model = modelName
		}
	}

	upstreamBody, err := json.Marshal(map[string]any{
		"text":             req.Text,
		"duration_seconds": req.DurationSeconds,
		"prompt_influence": req.PromptInfluence,
	})
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	pinned := imagePinnedConnectionID(r)
	usePinned := pinned != ""
	excludeIDs := []string{}
	tried := make(map[string]struct{})
	var lastErr *elevenLabsSoundAttemptErr
	for {
		if r.Context().Err() != nil {
			if lastErr != nil {
				lastErr.write(w)
				return
			}
			handlerutil.WriteJSONError(w, http.StatusBadGateway, r.Context().Err().Error())
			return
		}
		conn, connData, err := h.ChatH.GetBestConnectionWithContext(r.Context(), provider, pinned, excludeIDs, model)
		if err != nil || conn == nil || connData == nil {
			if lastErr != nil {
				lastErr.write(w)
				return
			}
			log.Warn("media", "no elevenlabs connection", "error", err)
			handlerutil.WriteJSONError(w, http.StatusServiceUnavailable, mediaConnectionError(err, provider))
			return
		}
		if _, seen := tried[conn.ID]; seen {
			if lastErr != nil {
				lastErr.write(w)
				return
			}
			handlerutil.WriteJSONError(w, http.StatusServiceUnavailable, mediaConnectionError(nil, provider))
			return
		}
		tried[conn.ID] = struct{}{}

		attemptErr := h.tryElevenLabsSoundConn(w, r, body, upstreamBody, provider, model, conn, connData)
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

func (h *MediaHandler) tryElevenLabsSoundConn(
	w http.ResponseWriter,
	r *http.Request,
	body []byte,
	upstreamBody []byte,
	provider string,
	model string,
	conn *models.ProviderConnection,
	connData *chat.ConnectionData,
) *elevenLabsSoundAttemptErr {
	apiKey := chat.ExtractAPIKey(connData)
	if apiKey == "" {
		return &elevenLabsSoundAttemptErr{status: http.StatusUnauthorized, jsonMsg: "no API key found", retryable: true}
	}

	targetURL := elevenLabsSoundGenerationURL(connData)
	httpReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, bytes.NewReader(upstreamBody))
	if err != nil {
		return &elevenLabsSoundAttemptErr{status: http.StatusInternalServerError, jsonMsg: "failed to create request", retryable: false}
	}
	httpReq.Header.Set(constants.HeaderContentType, constants.ContentTypeJSON)
	httpReq.Header.Set("xi-api-key", apiKey)

	client, clientErr := h.ChatH.GetClientForConnection(connData)
	if clientErr != nil {
		return &elevenLabsSoundAttemptErr{status: http.StatusBadGateway, jsonMsg: clientErr.Error(), retryable: true}
	}

	start := time.Now()
	resp, err := client.Do(httpReq)
	if err != nil {
		log.Warn("media", "elevenlabs sound-generation upstream failed", "error", err)
		return &elevenLabsSoundAttemptErr{
			status:    http.StatusBadGateway,
			jsonMsg:   "upstream request failed: " + err.Error(),
			retryable: r.Context().Err() == nil,
		}
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		if errBody == nil {
			errBody = []byte{}
		}
		snippet := strings.TrimSpace(string(errBody))
		log.Warn("media", "elevenlabs sound-generation upstream error", "status", resp.StatusCode, "body", snippet)
		backoff := 0
		if h.Repo != nil {
			backoff = h.Repo.GetConnectionBackoffLevel(conn.ID)
		}
		classification := providers.ClassifyError(resp.StatusCode, snippet, backoff, errBody)
		if classification.ShouldFallback && h.Repo != nil && !handlerutil.IsProbeContext(r.Context()) {
			cooldownSec := max(classification.CooldownMs/1000, 1)
			_ = h.Repo.LockConnectionModel(conn.ID, model, cooldownSec, classification.NewBackoffLevel)
		}
		return &elevenLabsSoundAttemptErr{
			status:    resp.StatusCode,
			rawBody:   errBody,
			retryable: classification.ShouldFallback,
		}
	}

	w.Header().Set("Content-Type", "audio/mpeg")
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, resp.Body); err != nil && r.Context().Err() == nil {
		log.Warn("media", "copy sound-generation response failed", "error", err)
	}

	if h.Repo != nil {
		_ = h.Repo.UpdateConnectionLastUsed(conn.ID)
		if !handlerutil.IsProbeContext(r.Context()) {
			_ = h.Repo.UnlockConnectionModel(conn.ID, model)
		}
	}
	latencyMs := time.Since(start).Milliseconds()
	logInfo := &shared.UsageLogInfo{
		Provider:     provider,
		Model:        model,
		ConnectionID: conn.ID,
		APIKey:       apiKey,
		Endpoint:     "/v1/audio/sound-generation",
	}
	h.ChatH.LogUsage(r.Context(), logInfo, nil, latencyMs, body, nil)
	return nil
}
