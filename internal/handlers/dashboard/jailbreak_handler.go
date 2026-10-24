package dashboard

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
)

// Jailbreak management: agent prompt presets library, plus per-model preset binding.
// Injection is opt-in, default off.

func (h *DashboardHandler) HandleGetJailbreak(w http.ResponseWriter, r *http.Request) {
	doc, err := h.Repo.GetJailbreakDoc()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, doc)
}

// A jailbreak prompt is often an uploaded .md document rather than a typed
// line, so the ceiling is generous while still bounded.
const maxJailbreakBodyBytes = 512 * 1024

func (h *DashboardHandler) HandlePutJailbreak(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxJailbreakBodyBytes+1))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	var doc db.JailbreakDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Reject a bad keyword or exclude here so the operator sees 400 with the
	// reason, not a 500 from the storage layer.
	if err := validateJailbreakRules(&doc); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.Repo.SaveJailbreakDoc(&doc); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "doc": doc})
}

func (h *DashboardHandler) HandleListJailbreakPresets(w http.ResponseWriter, r *http.Request) {
	presets, err := h.Repo.ListJailbreakPresets()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, presets)
}

func (h *DashboardHandler) HandlePutJailbreakPreset(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxJailbreakBodyBytes+1))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	var doc db.JailbreakDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if doc.ID == "" {
		base := doc.Name
		if base == "" {
			base = doc.Source
		}
		doc.ID = slugify(base)
	}
	if doc.Name == "" {
		if doc.Source != "" {
			doc.Name = doc.Source
		} else {
			doc.Name = doc.ID
		}
	}
	if err := validateJailbreakRules(&doc); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.Repo.SaveJailbreakPreset(&doc); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "preset": doc})
}

func (h *DashboardHandler) HandleDeleteJailbreakPreset(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing preset id")
		return
	}
	if err := h.Repo.DeleteJailbreakPreset(id); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
}

// validateJailbreakRules checks a preset's include and exclude rules before
// they are stored. Both lists share one grammar and one set of limits.
func validateJailbreakRules(doc *db.JailbreakDoc) error {
	keywords, err := db.NormalizeJailbreakKeywords(doc.Keywords)
	if err != nil {
		return err
	}
	excludes, err := db.NormalizeJailbreakKeywords(doc.Excludes)
	if err != nil {
		return err
	}
	doc.Keywords, doc.Excludes = keywords, excludes
	return nil
}

// HandleGetJailbreakExcludes serves the global deny list, which overrides
// every preset and every exact binding.
func (h *DashboardHandler) HandleGetJailbreakExcludes(w http.ResponseWriter, r *http.Request) {
	excludes, err := h.Repo.GetJailbreakExcludes()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if excludes == nil {
		excludes = []db.JailbreakKeyword{}
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"excludes": excludes})
}

func (h *DashboardHandler) HandlePutJailbreakExcludes(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxJailbreakBodyBytes+1))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	var req struct {
		Excludes []db.JailbreakKeyword `json:"excludes"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	excludes, err := db.NormalizeJailbreakKeywords(req.Excludes)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.Repo.SaveJailbreakExcludes(excludes); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "excludes": excludes})
}

func (h *DashboardHandler) HandleListJailbreakModels(w http.ResponseWriter, r *http.Request) {
	configs, err := h.Repo.ListJailbreakModelConfigs()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, configs)
}

func (h *DashboardHandler) HandleSetJailbreakModel(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4*1024))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	// The id travels in the body rather than the path on purpose: model ids
	// contain "/" and ":" ("tkh/mimo-v2.6-flash:free"), and chi hands back the
	// still-encoded segment for those, so a path parameter silently stored a key
	// that never matched the real id again — the toggle looked on, then came
	// back off after a reload.
	var req struct {
		ID       string `json:"id"`
		Enabled  *bool  `json:"enabled"`
		PresetID string `json:"presetId"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ID == "" || len(req.ID) > 300 {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid model id")
		return
	}
	enabled := false
	if req.Enabled != nil {
		enabled = *req.Enabled
	} else if req.PresetID != "" {
		enabled = req.PresetID != "off" && req.PresetID != "none"
	}
	presetID := req.PresetID
	if enabled && (presetID == "" || presetID == "on") {
		presetID = "default"
	}
	if err := h.Repo.SetJailbreakModelPreset(req.ID, presetID, enabled); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"id":       req.ID,
		"enabled":  enabled,
		"presetId": presetID,
	})
}

func slugify(s string) string {
	s = strings.TrimSuffix(s, ".md")
	s = strings.TrimSuffix(s, ".markdown")
	s = strings.TrimSuffix(s, ".txt")
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteRune('-')
		}
	}
	res := strings.Trim(b.String(), "-")
	if res == "" {
		return fmt.Sprintf("preset-%d", time.Now().UnixNano())
	}
	return res
}
