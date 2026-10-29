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

	conn, connData, err := h.ChatH.GetBestConnectionWithContext(r.Context(), provider, "", nil, model)
	if err != nil || conn == nil {
		log.Warn("media", "no elevenlabs connection", "error", err)
		handlerutil.WriteJSONError(w, http.StatusServiceUnavailable, mediaConnectionError(err, provider))
		return
	}

	apiKey := chat.ExtractAPIKey(connData)
	if apiKey == "" {
		handlerutil.WriteJSONError(w, http.StatusUnauthorized, "no API key found")
		return
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

	targetURL := elevenLabsSoundGenerationURL(connData)
	httpReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, bytes.NewReader(upstreamBody))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to create request")
		return
	}
	httpReq.Header.Set(constants.HeaderContentType, constants.ContentTypeJSON)
	httpReq.Header.Set("xi-api-key", apiKey)

	client, clientErr := h.ChatH.GetClientForConnection(connData)
	if clientErr != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, clientErr.Error())
		return
	}

	start := time.Now()
	resp, err := client.Do(httpReq)
	if err != nil {
		log.Warn("media", "elevenlabs sound-generation upstream failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "upstream request failed: "+err.Error())
		return
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		snippet := strings.TrimSpace(string(errBody))
		log.Warn("media", "elevenlabs sound-generation upstream error", "status", resp.StatusCode, "body", snippet)
		w.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(errBody)
		return
	}

	w.Header().Set("Content-Type", "audio/mpeg")
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, resp.Body); err != nil && r.Context().Err() == nil {
		log.Warn("media", "copy sound-generation response failed", "error", err)
	}

	_ = h.Repo.UpdateConnectionLastUsed(conn.ID)
	latencyMs := time.Since(start).Milliseconds()
	logInfo := &shared.UsageLogInfo{
		Provider:     provider,
		Model:        model,
		ConnectionID: conn.ID,
		APIKey:       apiKey,
		Endpoint:     "/v1/audio/sound-generation",
	}
	h.ChatH.LogUsage(r.Context(), logInfo, nil, latencyMs, body, nil)
}
