package dashboard

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"io"
	"net/http"
	"time"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/middleware"
	"9router/proxy/internal/models"
)

// The operator's playground: one chat turn from the dashboard against a model
// the gateway routes, without an engine API key.
//
// A dashboard caller sends {model, messages, temperature?, maxTokens?, keyId?}
// and gets the same Chat Completions JSON the public lane would return. The
// turn reaches it the way the Gemini surface already does
// (chat/gemini_v1beta.go): a synthetic request through the engine's own
// HandleChatCompletions. Routing, combo fallback, persona injection, the
// limiter and usage accounting are therefore the production paths, not a
// second implementation that can drift.
//
// Attribution: the picked key travels on the synthetic request's context, the
// same place RequireApiKey puts it, so the usage row names the key as the
// client (chat/usage.go), its budget is measured, and its model allowlist is
// enforced by the gate the public lane uses. An empty keyId is the global
// scope: a keyless caller, which the engine treats as unrestricted for model
// policy (effectiveAllowedModels returns empty for a nil key).
func (h *DashboardHandler) HandlePlaygroundChat(w http.ResponseWriter, r *http.Request) {
	if h.ChatH == nil {
		handlerutil.WriteJSONError(w, http.StatusServiceUnavailable, "chat engine unavailable")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer func() { _ = r.Body.Close() }()

	var req struct {
		Model       string `json:"model"`
		Messages    []any  `json:"messages"`
		Stream      bool   `json:"stream"`
		Temperature *any   `json:"temperature"`
		MaxTokens   *int   `json:"maxTokens"`
		KeyID       string `json:"keyId"`
	}
	if len(body) == 0 || json.Unmarshal(body, &req) != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Model == "" || len(req.Messages) == 0 {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "model and messages are required")
		return
	}

	// The key is resolved here, not trusted from the caller: only an existing,
	// active row may scope the turn. A missing row is a caller mistake, not a
	// silent downgrade to the global scope.
	var key *models.APIKey
	if req.KeyID != "" {
		if h.Repo == nil {
			handlerutil.WriteJSONError(w, http.StatusServiceUnavailable, "repository unavailable")
			return
		}
		key, err = h.Repo.GetApiKeyByID(req.KeyID)
		if err != nil || key == nil || key.IsActive != 1 {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "unknown or inactive key")
			return
		}
		if key.ExpiresAt != nil && *key.ExpiresAt != "" {
			expires, parseErr := time.Parse(time.RFC3339, *key.ExpiresAt)
			if parseErr != nil || !time.Now().Before(expires) {
				handlerutil.WriteJSONError(w, http.StatusBadRequest, "expired API key")
				return
			}
		}
	}

	// The exact body the public lane would have received, rebuilt from the
	// dashboard's own fields so the panel and the wire agree.
	payload := map[string]any{
		"model":    req.Model,
		"messages": req.Messages,
		"stream":   req.Stream,
	}
	if req.MaxTokens != nil {
		payload["max_tokens"] = *req.MaxTokens
	}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	// Deterministic: chat.go hashes the raw body for the prompt cache, so the
	// same turn must always produce the same bytes to hit it.
	encoded, err := json.Marshal(payload, json.Deterministic(true))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to encode request")
		return
	}

	ctx := r.Context()
	if key != nil {
		ctx = context.WithValue(ctx, middleware.ApiKeyContextKey, key)
	}
	inner := r.Clone(ctx)
	inner.Body = io.NopCloser(bytes.NewReader(encoded))
	inner.ContentLength = int64(len(encoded))
	inner.URL.Path = "/v1/chat/completions"
	inner.Header = inner.Header.Clone()
	inner.Header.Set("Content-Type", "application/json")
	// The dashboard session authorised this request; the engine lane would
	// authorise on a bearer token, which must not leak a dashboard credential
	// upstream through a header the executor forwards.
	inner.Header.Del("Authorization")

	h.ChatH.HandleChatCompletions(w, inner)
}
