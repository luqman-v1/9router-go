package media

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"strings"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/middleware"
	"9router/proxy/internal/providers"
)

// HandleImages handles /v1/images/generations.
func (h *MediaHandler) HandleImages(w http.ResponseWriter, r *http.Request) {
	r = r.WithContext(handlerutil.WithServiceKind(r.Context(), "image"))
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer func() { _ = r.Body.Close() }()
	h.forwardMediaRequest(w, r, body, "dall-e-3", "/v1/images/generations")
}

// HandleVideoGenerations handles /v1/videos/generations.
func (h *MediaHandler) HandleVideoGenerations(w http.ResponseWriter, r *http.Request) {
	r = r.WithContext(handlerutil.WithServiceKind(r.Context(), "video"))
	body, err := io.ReadAll(r.Body)
	if err != nil {
		middleware.WriteBodyReadError(w, r, err)
		return
	}
	defer func() { _ = r.Body.Close() }()
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
	defer func() { _ = r.Body.Close() }()
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
	defer func() { _ = r.Body.Close() }()
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
