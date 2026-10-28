package dashboard

import (
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/google/uuid"

	"9router/proxy/internal/combopresets"
	"9router/proxy/internal/handlerutil"
)

// Combo presets (port of upstream src/app/api/combos/presets/route.js).
//
// The list itself lives in internal/combopresets; this file is only the HTTP
// edge. Cursor presets fall back to the static `cu` registry row, because the
// Go tree has no live Cursor catalog resolver — that fallback is the same path
// upstream takes when the live fetch fails, so the contract is unchanged.

// HandleGetComboPresets handles GET /api/combos/presets?source=cursor|claude.
// Returns the source's presets with the ones already stored flagged, so the
// dashboard can render "Create 12, skip 1" before writing anything.
func (h *DashboardHandler) HandleGetComboPresets(w http.ResponseWriter, r *http.Request) {
	source := r.URL.Query().Get("source")
	items, err := h.comboPresetItems(source)
	if err != nil {
		writePresetError(w, err)
		return
	}

	toCreate, toSkip := 0, 0
	for _, item := range items {
		if item.Exists {
			toSkip++
			continue
		}
		toCreate++
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"source":   source,
		"items":    items,
		"toCreate": toCreate,
		"toSkip":   toSkip,
	})
}

// HandleCreateComboPresets handles POST /api/combos/presets {source}.
// Creates the missing combos and skips the ones already stored, so a second
// call is a no-op rather than a name collision.
func (h *DashboardHandler) HandleCreateComboPresets(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer func() { _ = r.Body.Close() }()

	var req struct {
		Source string `json:"source"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
	}

	items, err := h.comboPresetItems(req.Source)
	if err != nil {
		writePresetError(w, err)
		return
	}

	created := make([]string, 0, len(items))
	skipped := make([]string, 0, len(items))
	for _, item := range items {
		if item.Exists {
			skipped = append(skipped, item.Name)
			continue
		}
		// A preset name lands in the same namespace as every other combo, so it
		// passes the same collision guard: an alias of the same name would make
		// the new combo unreachable the moment it was written.
		if h.guardNameCollision(w, nsCombo, item.Name) {
			return
		}
		modelsJSON, err := json.Marshal(item.Models)
		if err != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := h.Repo.CreateCombo(uuid.New().String(), item.Name, "", string(modelsJSON), "fallback"); err != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		created = append(created, item.Name)
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"source":       req.Source,
		"created":      created,
		"skipped":      skipped,
		"createdCount": len(created),
		"skippedCount": len(skipped),
	})
}

// comboPresetItems loads the stored combo names and builds the source's preset
// list against them.
func (h *DashboardHandler) comboPresetItems(source string) ([]combopresets.Item, error) {
	if !combopresets.IsValidSource(source) {
		return nil, combopresets.ErrUnknownSource
	}
	combos, err := h.Repo.GetCombos()
	if err != nil {
		return nil, fmt.Errorf("load combos: %w", err)
	}
	names := make([]string, 0, len(combos))
	for _, c := range combos {
		if c != nil {
			names = append(names, c.Name)
		}
	}
	return combopresets.Build(source, nil, names)
}

// writePresetError maps the builder's client-side rejection to 400 and any
// storage failure to 500.
func writePresetError(w http.ResponseWriter, err error) {
	if errors.Is(err, combopresets.ErrUnknownSource) {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
}
