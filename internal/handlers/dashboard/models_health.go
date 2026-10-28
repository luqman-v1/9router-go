package dashboard

import (
	json "encoding/json/v2"
	"io"
	"net/http"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/modelhealth"
)

// HandleGetModelHealth handles GET /api/models/health.
//
// It answers the configuration and the audit log in one round trip, because
// the settings panel renders both: the switches and the history of what the
// scheduler did. Two endpoints would mean two requests for one screen.
func (h *DashboardHandler) HandleGetModelHealth(w http.ResponseWriter, r *http.Request) {
	raw, err := h.Repo.GetSettingsRaw()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	cfg := modelhealth.ParseConfig(raw["modelHealth"])

	audit := []modelhealth.AuditEntry{}
	if stored, err := h.Repo.GetKVScope(modelhealth.ScopeAudit); err == nil {
		audit = modelhealth.DecodeAudit(stored["log"])
	}

	// The failure counters are returned alongside, so the panel can show
	// "2 of 3 failures" next to a model without a second read.
	counters := map[string]modelhealth.Failures{}
	if failures, err := h.Repo.GetKVScope(modelhealth.ScopeFailures); err == nil {
		for key, value := range failures {
			counters[key] = modelhealth.DecodeFailures(value)
		}
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"config":   cfg.Marshal(),
		"audit":    audit,
		"failures": counters,
	})
}

// HandleSaveModelHealth handles PUT /api/models/health.
//
// The payload is merged into the stored value rather than replacing it, so a
// client that knows one field cannot wipe the others — the same contract the
// auto-ping settings use, and the reason a half-written payload used to
// switch the loop off.
func (h *DashboardHandler) HandleSaveModelHealth(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer func() { _ = r.Body.Close() }()

	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	raw, err := h.Repo.GetSettingsRaw()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	merged, err := modelhealth.MergeConfig(raw["modelHealth"], payload)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	// UpdateSettingsRaw merges, so only this key changes and the rest of the
	// settings row is carried over untouched.
	if err := h.Repo.UpdateSettingsRaw(map[string]any{"modelHealth": merged}); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"config": merged,
	})
}
