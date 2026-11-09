package media

import (
	"bytes"
	"strings"
	"time"

	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"

	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/handlers/shared"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/usagetracker"
)

// MusicGenerationRequest is the request body for ElevenLabs music generation.
// https://elevenlabs.io/docs/api-reference/music/create-music
type MusicGenerationRequest struct {
	Prompt          string   `json:"prompt"`
	DurationSeconds *float64 `json:"duration_seconds,omitempty"`
	Model           string   `json:"model,omitempty"`
}

// HandleAudioMusic handles POST /audio/music and POST /v1/audio/music
// for ElevenLabs music generation.
func (h *MediaHandler) HandleAudioMusic(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	if err := r.Body.Close(); err != nil {
		log.Warn("media", "elevenlabs music: close request body", "error", err)
	}

	var req MusicGenerationRequest
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing required field: prompt")
		return
	}

	// Default to elevenlabs provider if not specified via model
	provider := "elevenlabs"
	if req.Model != "" {
		// Allow model to specify provider (e.g., "elevenlabs/music-v1")
		if idx := strings.Index(req.Model, "/"); idx > 0 {
			provider = req.Model[:idx]
		}
	}

	conn, connData, err := h.ChatH.GetBestConnectionWithContext(r.Context(), provider, "", nil, req.Model)
	if err != nil || conn == nil {
		handlerutil.WriteJSONError(w, http.StatusServiceUnavailable, "no active connection for elevenlabs")
		return
	}

	apiKey := chat.ExtractAPIKey(connData)
	if apiKey == "" {
		handlerutil.WriteJSONError(w, http.StatusUnauthorized, "no API key found for elevenlabs connection")
		return
	}

	providerCfg, err := h.ChatH.GetProviderConfig("elevenlabs", connData)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "get elevenlabs config: "+err.Error())
		return
	}

	// Build request body for ElevenLabs music API
	upstreamBody, err := json.Marshal(map[string]any{
		"prompt":           req.Prompt,
		"duration_seconds": req.DurationSeconds,
		"model":            req.Model,
	})
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "build elevenlabs music request: "+err.Error())
		return
	}

	targetURL := "https://api.elevenlabs.io/v1/music"
	if connData != nil && strings.TrimSpace(connData.BaseURL) != "" {
		targetURL = strings.TrimRight(strings.TrimSpace(connData.BaseURL), "/") + "/v1/music"
	} else if providerCfg != nil && strings.TrimSpace(providerCfg.BaseURL) != "" {
		targetURL = strings.TrimRight(strings.TrimSpace(providerCfg.BaseURL), "/") + "/v1/music"
	}
	httpReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, bytes.NewReader(upstreamBody))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "create elevenlabs music request: "+err.Error())
		return
	}

	httpReq.Header.Set("Content-Type", "application/json")
	authHeader := providerCfg.AuthHeader
	if authHeader == "" {
		authHeader = "xi-api-key"
	}
	handlerutil.SetAuthHeader(httpReq, apiKey, authHeader, providerCfg.AuthScheme)
	for k, v := range providerCfg.StaticHeaders {
		if httpReq.Header.Get(k) == "" {
			httpReq.Header.Set(k, v)
		}
	}

	client, clientErr := h.ChatH.GetClientForConnection(connData)
	if clientErr != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, clientErr.Error())
		return
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "elevenlabs music request failed: "+err.Error())
		return
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "read response: "+err.Error())
		return
	}

	if resp.StatusCode != http.StatusOK {
		snippet := string(respBody[:min(300, len(respBody))])
		log.Warn("media", "elevenlabs music upstream error", "status", resp.StatusCode, "body", snippet)
		handlerutil.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("elevenlabs music failed with status %d: %s", resp.StatusCode, snippet))
		return
	}

	// Stream audio/mpeg response back to client
	w.Header().Set("Content-Type", "audio/mpeg")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(respBody)

	if h.Repo != nil {
		_ = h.Repo.UpdateConnectionLastUsed(conn.ID)
		h.ChatH.LogUsage(r.Context(), &shared.UsageLogInfo{
			Provider:     "elevenlabs",
			Model:        req.Model,
			ConnectionID: conn.ID,
			APIKey:       apiKey,
			Endpoint:     "/v1/audio/music",
		}, nil, 0, body, nil)
	}
	usagetracker.GetTracker().PushRecent(usagetracker.RecentRequest{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Model:     req.Model,
		Provider:  "elevenlabs",
		Status:    "ok",
	}, h.Repo)
}
