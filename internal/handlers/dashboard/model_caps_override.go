package dashboard

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/url"
	"strings"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/providers"
)

// Per-model capability overrides (the "model editor").
//
// GET returns the stored sparse override plus the block detection currently
// resolves, so the UI can show what Auto means for that model instead of
// leaving the operator to guess. PUT takes {capsOverride: {...}}; an empty
// object clears the row, which is why there is no DELETE route.

// HandleGetModelCapsOverride handles
// GET /api/providers/{id}/models/{model}/caps.
func (h *DashboardHandler) HandleGetModelCapsOverride(w http.ResponseWriter, r *http.Request) {
	provider := overrideProviderParam(r)
	model := modelParam(r)
	if provider == "" || model == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing provider id or model")
		return
	}
	canonical := providers.ResolveAlias(provider)

	stored := map[string]any{}
	row, err := h.Repo.GetModelCapOverride(canonical, model)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if row != nil {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(row.CapsOverride), &parsed); err == nil && parsed != nil {
			stored = parsed
		}
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"provider":     canonical,
		"model":        model,
		"capsOverride": stored,
		// What Auto resolves to right now, so the placeholder values in the
		// editor are the real detected numbers rather than a second estimate.
		"detected":  providers.GetCapabilitiesDetailForModel(canonical, model),
		"fields":    providers.CapsOverrideFields(),
		"maxTokens": providers.ModelCapsOverrideMaxTokens,
	})
}

// HandleSaveModelCapsOverride handles
// PUT /api/providers/{id}/models/{model}/caps with body {capsOverride: {...}}.
func (h *DashboardHandler) HandleSaveModelCapsOverride(w http.ResponseWriter, r *http.Request) {
	provider := overrideProviderParam(r)
	model := modelParam(r)
	if provider == "" || model == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing provider id or model")
		return
	}
	canonical := providers.ResolveAlias(provider)

	body, err := io.ReadAll(io.LimitReader(r.Body, 32*1024))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer func() { _ = r.Body.Close() }()

	var payload struct {
		CapsOverride map[string]any `json:"capsOverride"`
	}
	// A malformed body is a clear error, not a silent clear: an operator who
	// typed a payload and got back "saved" with nothing applied would not find
	// out until a request went out wrong.
	if err := json.Unmarshal(body, &payload); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if payload.CapsOverride == nil {
		// Absent field (as opposed to {}), which is a client bug, not a clear.
		payload.CapsOverride = map[string]any{}
	}

	override, err := providers.NormalizeModelCapsOverride(payload.CapsOverride)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	raw, err := json.Marshal(override)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to encode override")
		return
	}
	if override.Empty() {
		if err := h.Repo.DeleteModelCapOverride(canonical, model); err != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
	} else if err := h.Repo.SetModelCapOverride(canonical, model, raw); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The cache is keyed by the resolved capabilities, so a write has to retire
	// it or the next read keeps answering with the pre-edit block.
	providers.InvalidateModelCapsOverrideCache()

	saved := map[string]any{}
	if !override.Empty() {
		if err := json.Unmarshal(raw, &saved); err != nil {
			saved = map[string]any{}
		}
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"provider":     canonical,
		"model":        model,
		"capsOverride": saved,
		"detected":     providers.GetCapabilitiesDetailForModel(canonical, model),
	})
}

// modelParam reads and unescapes the {model} path segment. A model id carries
// slashes and colons ("anthropic/claude-x:free"), so the segment arrives
// percent-encoded from the dashboard.
func modelParam(r *http.Request) string {
	raw := getURLParam(r, "model")
	if unescaped, err := url.PathUnescape(raw); err == nil && unescaped != "" {
		return strings.TrimSpace(unescaped)
	}
	return strings.TrimSpace(raw)
}
