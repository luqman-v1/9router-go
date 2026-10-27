package dashboard

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
)

// Prompt Injection management: global, per API key, per model, per key+model
// injection entries, plus a template library.
//
// All routes are gated behind the dashboard auth middleware.

func (h *DashboardHandler) HandleListPromptInjections(w http.ResponseWriter, r *http.Request) {
	targetType := r.URL.Query().Get("targetType")
	injections, err := h.Repo.ListPromptInjections(targetType)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, injections)
}

func (h *DashboardHandler) HandleGetPromptInjection(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing id")
		return
	}
	inj, err := h.Repo.GetPromptInjection(id)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if inj == nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "not found")
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, inj)
}

func (h *DashboardHandler) HandlePutPromptInjection(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	var d db.PromptInjectionDoc
	if err := json.Unmarshal(body, &d); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if d.Mode != "system" && d.Mode != "script" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "mode must be system or script")
		return
	}
	if d.TargetType == "" {
		d.TargetType = "global"
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if d.CreatedAt == "" {
		d.CreatedAt = now
	}
	d.UpdatedAt = now

	if err := h.Repo.SavePromptInjection(&d); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "injection": d})
}

func (h *DashboardHandler) HandleDeletePromptInjection(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing id")
		return
	}
	if err := h.Repo.DeletePromptInjection(id); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
}

// Templates

func (h *DashboardHandler) HandleListPromptInjectionTemplates(w http.ResponseWriter, r *http.Request) {
	templates, err := h.Repo.ListPromptInjectionTemplates()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, templates)
}

func (h *DashboardHandler) HandlePutPromptInjectionTemplate(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	var t db.PromptInjectionTemplate
	if err := json.Unmarshal(body, &t); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if t.Name == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := h.Repo.SavePromptInjectionTemplate(&t); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "template": t})
}

func (h *DashboardHandler) HandleDeletePromptInjectionTemplate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing id")
		return
	}
	if err := h.Repo.DeletePromptInjectionTemplate(id); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
}

// Settings

func (h *DashboardHandler) HandleGetPromptInjectionSetting(w http.ResponseWriter, r *http.Request) {
	enabled := h.Repo.GetPromptInjectionEnabled()
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"promptInjectionEnabled": enabled})
}

func (h *DashboardHandler) HandlePutPromptInjectionSetting(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		PromptInjectionEnabled bool `json:"promptInjectionEnabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.Repo.SetPromptInjectionEnabled(payload.PromptInjectionEnabled); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "promptInjectionEnabled": payload.PromptInjectionEnabled})
}
