package dashboard

import (
	json "encoding/json/v2"
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/google/uuid"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
)

// HandleGetCombos handles GET /api/combos.
// Returns a list of combos from Repo.GetCombos().
func (h *DashboardHandler) HandleGetCombos(w http.ResponseWriter, r *http.Request) {
	combos, err := h.Repo.GetCombos()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if combos == nil {
		combos = []*models.Combo{}
	}
	handlerutil.WriteJSON(w, http.StatusOK, combos)
}

// HandleCreateCombo handles POST /api/combos.
// Parses id, name, kind, models (json array/string), strategy. Calls CreateCombo.
func (h *DashboardHandler) HandleCreateCombo(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var req struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Kind     string `json:"kind"`
		Models   any    `json:"models"`
		Strategy string `json:"strategy"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if req.Name == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing combo name")
		return
	}
	if req.ID == "" {
		req.ID = uuid.New().String()
	}
	if req.Strategy == "" {
		req.Strategy = "fallback"
	}

	modelsJSON := "[]"
	if req.Models != nil {
		switch m := req.Models.(type) {
		case string:
			modelsJSON = m
		default:
			b, err := json.Marshal(m)
			if err == nil {
				modelsJSON = string(b)
			}
		}
	}
	// A combo name is addressed bare, so it must not be shadowed by a model
	// alias (consulted first) or read as a custom model id in /v1/models.
	if h.guardNameCollision(w, nsCombo, req.Name) {
		return
	}
	if err := h.Repo.CreateCombo(req.ID, req.Name, req.Kind, modelsJSON, req.Strategy); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok", "id": req.ID})
}

// HandleUpdateCombo handles PUT /api/combos/{id}.
// Updates combo.
func (h *DashboardHandler) HandleUpdateCombo(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing combo id")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var req struct {
		Name     string `json:"name"`
		Kind     string `json:"kind"`
		Models   any    `json:"models"`
		Strategy string `json:"strategy"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	existing, err := h.Repo.GetComboById(id)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if existing == nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "combo not found")
		return
	}

	name := req.Name
	if name == "" {
		name = existing.Name
	}
	kind := req.Kind
	if kind == "" && existing.Kind != nil {
		kind = *existing.Kind
	}
	if existing.Kind != nil && *existing.Kind == autoFreeComboKind {
		if err := validateLockedReorder(existing, req, name, kind); err != nil {
			handlerutil.WriteJSONError(w, http.StatusForbidden, err.Error())
			return
		}
	}
	modelsJSON := existing.Models
	if req.Models != nil {
		switch m := req.Models.(type) {
		case string:
			modelsJSON = m
		default:
			b, err := json.Marshal(m)
			if err == nil {
				modelsJSON = string(b)
			}
		}
	}
	strategy := req.Strategy
	if strategy == "" {
		strategy = existing.Strategy
	}
	// Renaming a combo moves the bare name it answers to, so a rename gets the
	// same check as a create. Leaving the name alone is unaffected: the guard
	// skips the combo space when the caller is writing a combo.
	if name != existing.Name && h.guardNameCollision(w, nsCombo, name) {
		return
	}
	if err := h.Repo.UpdateCombo(id, name, kind, modelsJSON, strategy); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok", "id": id})
}

// autoFreeComboKind marks a combo that the dashboard auto-generates from the
// registry's free-tier models. Such combos are locked: they can be reordered
// (fallback order matters) but neither deleted nor edited by hand.
const autoFreeComboKind = "auto-free"

// AutoFreeComboID is the stable id of the auto-generated free-tier combo.
const AutoFreeComboID = "auto-free-tier"

// HandleAutoFreeCombo handles POST /api/combos/auto-free.
// (Re)builds the locked free-tier combo from registry models of providers the
// user actually has a connection for, so the combo never references
// unreachable providers.
func (h *DashboardHandler) HandleAutoFreeCombo(w http.ResponseWriter, r *http.Request) {
	models, err := h.freeTierComboModels(r.Context())
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(models) == 0 {
		handlerutil.WriteJSONError(w, http.StatusConflict,
			"no free-tier models found among providers with connections")
		return
	}

	modelsJSON, err := json.Marshal(models)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to encode models")
		return
	}

	name := "Auto Free Tier"
	existing, err := h.Repo.GetComboById(AutoFreeComboID)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if existing == nil {
		if err := h.Repo.CreateCombo(AutoFreeComboID, name, autoFreeComboKind, string(modelsJSON), "fallback"); err != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
	} else if err := h.Repo.UpdateCombo(AutoFreeComboID, name, autoFreeComboKind, string(modelsJSON), "fallback"); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"id":     AutoFreeComboID,
		"models": models,
	})
}

// freeTierComboModels returns "alias/model" ids for every free-tier registry
// model of providers with at least one connection, ordered by provider alias
// then model id so regenerating yields a stable, diffable list.
func (h *DashboardHandler) freeTierComboModels(ctx context.Context) ([]string, error) {
	active, err := h.Repo.GetConnectedProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("freeTierComboModels: list providers: %w", err)
	}

	var out []string
	for providerID := range active {
		for _, modelID := range providers.GetProviderModels(providerID) {
			if !providers.IsFreeTierModel(modelID) {
				continue
			}
			out = append(out, providers.GetProviderAlias(providerID)+"/"+modelID)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// validateLockedReorder allows only a pure permutation of the locked combo's
// models: the dashboard may reorder the fallback chain, but the model set,
// name and kind stay the provider's registry truth.
func validateLockedReorder(existing *models.Combo, req struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Models   any    `json:"models"`
	Strategy string `json:"strategy"`
}, name, kind string) error {
	if name != "" && name != existing.Name {
		return fmt.Errorf("auto free-tier combo name is locked")
	}
	if kind != "" && kind != *existing.Kind {
		return fmt.Errorf("auto free-tier combo kind is locked")
	}

	// A partial update (strategy only, no models field) leaves the model set
	// untouched — the dashboard card's strategy dropdown works this way. Only a
	// request that actually sends models must prove it is a pure permutation.
	if req.Models == nil {
		return nil
	}

	current, err := comboModels(existing.Models)
	if err != nil {
		return err
	}
	next, err := comboModels(req.Models)
	if err != nil {
		return err
	}
	if len(next) != len(current) || !sameModelSet(current, next) {
		return fmt.Errorf("auto free-tier combo models are locked; reorder only")
	}
	return nil
}

// comboModels extracts the model id array of a combo's stored models field,
// accepting either the raw JSON string or an already-decoded array value.
func comboModels(v any) ([]string, error) {
	if v == nil {
		return nil, nil
	}
	switch m := v.(type) {
	case string:
		if strings.TrimSpace(m) == "" {
			return nil, nil
		}
		var arr []string
		if err := json.Unmarshal([]byte(m), &arr); err != nil {
			return nil, fmt.Errorf("combo models not a JSON array: %w", err)
		}
		return arr, nil
	case []string:
		return m, nil
	default:
		b, err := json.Marshal(m)
		if err != nil {
			return nil, fmt.Errorf("combo models invalid: %w", err)
		}
		var arr []string
		if err := json.Unmarshal(b, &arr); err != nil {
			return nil, fmt.Errorf("combo models not a JSON array: %w", err)
		}
		return arr, nil
	}
}

// sameModelSet reports whether two arrays hold the same ids in any order.
func sameModelSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, id := range a {
		seen[id]++
	}
	for _, id := range b {
		if seen[id] == 0 {
			return false
		}
		seen[id]--
	}
	return true
}

// HandleDeleteCombo handles DELETE /api/combos/{id}.
// Deletes combo, unless it is the locked auto free-tier combo.
func (h *DashboardHandler) HandleDeleteCombo(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing combo id")
		return
	}

	if combo, _ := h.Repo.GetComboById(id); combo != nil && combo.Kind != nil && *combo.Kind == autoFreeComboKind {
		handlerutil.WriteJSONError(w, http.StatusForbidden, "auto free-tier combo is locked")
		return
	}

	if err := h.Repo.DeleteCombo(id); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok", "id": id})
}
