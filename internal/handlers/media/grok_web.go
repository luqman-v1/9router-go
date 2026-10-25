package media

import (
	"context"
	"fmt"
	"net/http"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/proxy/executor"
)

// Grok Imagine's media lane, ported from upstream
// open-sse/handlers/imageProviders/grok-web.js (38a0c449, part of #624): the
// image provider delegates to the executor (`useExecutor: true`), because the
// Cloudflare-gated conversations/new endpoint and the asset download only
// exist there. This side mirrors that: the media handler resolves the
// connection's cookie and hands the turn to executor.GenerateGrokMedia.

// handleGrokWebImage serves /v1/images/generations for grok-web's
// grok-image model.
func (h *MediaHandler) handleGrokWebImage(w http.ResponseWriter, r *http.Request, body []byte, modelInfo *chat.ModelInfo) error {
	var req struct {
		Prompt string `json:"prompt"`
		Model  string `json:"model"`
		N      int    `json:"n"`
		Size   string `json:"size"`
	}
	_ = json.Unmarshal(body, &req)
	if req.Prompt == "" {
		return fmt.Errorf("missing required field: prompt")
	}
	n := req.N
	if n < 1 {
		n = 1
	}
	if n > 4 {
		n = 4
	}
	cookie, err := h.grokCookie(r.Context())
	if err != nil {
		return err
	}
	results, err := executor.GenerateGrokMedia(r.Context(), nil, cookie, req.Prompt, "image", req.Size, 0, n)
	if err != nil {
		return err
	}
	data := make([]map[string]any, 0, len(results))
	for _, m := range results {
		entry := map[string]any{"b64_json": m.B64}
		if m.Mime != "" {
			entry["mime_type"] = m.Mime
		}
		data = append(data, entry)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	out, _ := json.Marshal(map[string]any{"created": time.Now().Unix(), "data": data})
	_, _ = w.Write(out)
	return nil
}

// handleGrokWebVideo serves /v1/videos/generations for grok-web's
// grok-video model. One iteration only: video quota is ~1/day even on paid
// tiers, so the lane never loops.
func (h *MediaHandler) handleGrokWebVideo(w http.ResponseWriter, r *http.Request, body []byte, modelInfo *chat.ModelInfo) error {
	var req struct {
		Prompt  string  `json:"prompt"`
		Model   string  `json:"model"`
		Size    string  `json:"size"`
		Seconds float64 `json:"seconds"`
	}
	_ = json.Unmarshal(body, &req)
	if req.Prompt == "" {
		return fmt.Errorf("missing required field: prompt")
	}
	cookie, err := h.grokCookie(r.Context())
	if err != nil {
		return err
	}
	results, err := executor.GenerateGrokMedia(r.Context(), nil, cookie, req.Prompt, "video", req.Size, req.Seconds, 1)
	if err != nil {
		return err
	}
	if len(results) == 0 {
		return fmt.Errorf("grok-web returned no video asset")
	}
	return writeUcMediaJSON(w, results[0].B64, results[0].Mime)
}

// grokCookie resolves an active grok-web connection and returns its pasted
// Cookie header. The reference client handed to the generator is nil — a
// proxy assignment is resolved per chat turn on that path; the media lanes
// for the other web-cookie providers run the same way.
func (h *MediaHandler) grokCookie(ctx context.Context) (string, error) {
	conn, connData, err := h.ChatH.GetBestConnectionWithContext(ctx, "grok-web", "", nil, "")
	if err != nil || connData == nil {
		return "", fmt.Errorf("no active connection for grok-web: %w", err)
	}
	if h.Repo != nil && conn != nil {
		_ = h.Repo.UpdateConnectionLastUsed(conn.ID)
	}
	return chat.ExtractAPIKey(connData), nil
}
