package dashboard

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/url"
	"strings"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/providers"
)

// HandleGetProviderOverrides handles GET /api/providers/{id}/overrides.
//
// The response carries the operator's own headers plus what the registry
// already sends, so the UI can pre-fill exactly what goes on the wire instead
// of asking the operator to guess. Upstream builds the same pair from
// PROVIDERS[canonical].headers (v0.5.95,
// src/app/api/providers/[id]/overrides/route.js).
func (h *DashboardHandler) HandleGetProviderOverrides(w http.ResponseWriter, r *http.Request) {
	provider := overrideProviderParam(r)
	if provider == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing provider id")
		return
	}
	canonical := providers.ResolveAlias(provider)

	override, err := h.Repo.GetProviderOverride(canonical)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	headers := map[string]string{}
	if override != nil {
		headers = override.Headers
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"provider":       canonical,
		"headers":        headers,
		"builtinHeaders": builtinProviderHeaders(canonical),
		"blockedHeaders": blockedOverrideHeaderNames(),
	})
}

// HandleSaveProviderOverrides handles PUT /api/providers/{id}/overrides with
// body { headers: {name: value} }. An empty payload clears the override, which
// is why there is no DELETE route: the payload already says "nothing".
func (h *DashboardHandler) HandleSaveProviderOverrides(w http.ResponseWriter, r *http.Request) {
	provider := overrideProviderParam(r)
	if provider == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing provider id")
		return
	}
	canonical := providers.ResolveAlias(provider)

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var payload struct {
		Headers map[string]string `json:"headers"`
	}
	// A malformed body is a clear error, not a silent clear: an operator who
	// typed a payload and got back "saved" with nothing applied would not find
	// out until a request went out wrong.
	if err := json.Unmarshal(body, &payload); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	override, err := db.NormalizeProviderOverrides(payload.Headers)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.Repo.SetProviderOverride(canonical, override); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	saved := map[string]string{}
	if override != nil {
		saved = override.Headers
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"provider": canonical,
		"headers":  saved,
	})
}

// overrideProviderParam reads and unescapes the {id} path segment. An
// OpenAI-compatible node id contains slashes and colons, so the raw segment
// arrives percent-encoded from the dashboard.
func overrideProviderParam(r *http.Request) string {
	raw := getURLParam(r, "id")
	if unescaped, err := url.PathUnescape(raw); err == nil && unescaped != "" {
		return strings.TrimSpace(unescaped)
	}
	return strings.TrimSpace(raw)
}

// builtinProviderHeaders returns the registry headers one provider sends, so
// the UI's baseline is the registry rather than a second copy of it.
func builtinProviderHeaders(canonical string) map[string]string {
	cfg, ok := providers.KnownProviders[providers.ResolveAlias(canonical)]
	if !ok {
		return map[string]string{}
	}
	out := make(map[string]string, len(cfg.StaticHeaders))
	for k, v := range cfg.StaticHeaders {
		out[k] = v
	}
	return out
}

// blockedOverrideHeaderNames lists the headers an override may never carry, so
// the UI can disable those fields and say why instead of answering 400.
func blockedOverrideHeaderNames() []string {
	return []string{
		"authorization", "connection", "content-length", "content-type",
		"cookie", "host", "transfer-encoding",
	}
}
