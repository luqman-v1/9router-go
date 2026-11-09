package dashboard

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"

	"9router/proxy/internal/combos/smart"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
	"github.com/google/uuid"
)

// HandleGetCombos handles GET /api/combos.
//
// Every combo carries its smart rules and the members those rules currently
// own. The second half is derived rather than stored, so the dashboard can
// badge a managed member without having to guess which of the list the
// operator pinned by hand.
func (h *DashboardHandler) HandleGetCombos(w http.ResponseWriter, r *http.Request) {
	combos, err := h.Repo.GetCombos()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if combos == nil {
		combos = []*models.Combo{}
	}
	rulesByID, err := h.Repo.ComboRulesByID(r.Context())
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, combo := range combos {
		raw, ok := rulesByID[combo.ID]
		if !ok {
			continue
		}
		combo.MatchRules = raw
		rules, err := smart.ParseRules(raw)
		if err != nil {
			// An unreadable rule list must not take the whole Combos view down:
			// the combo still routes on its stored models, and the rules editor
			// shows the raw column so the operator can fix it.
			log.Warn("smart-combo", "stored rules unreadable", "combo", combo.Name, "error", err)
			continue
		}
		combo.AutoMembers = smart.AutoMembers(rules)
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
		ID         string `json:"id"`
		Name       string `json:"name"`
		Kind       string `json:"kind"`
		Models     any    `json:"models"`
		Strategy   string `json:"strategy"`
		MatchRules any    `json:"matchRules"`
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
	modelIDs, ok := normalizeComboModelIDs(req.Models)
	if !ok {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "models must contain valid model IDs")
		return
	}
	if len(modelIDs) > 0 {
		encoded, err := json.Marshal(modelIDs)
		if err != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		modelsJSON = string(encoded)
	}

	// The rules are validated before anything is written: a combo created with
	// a broken pattern would look saved and then silently match nothing.
	rulesJSON, _, code, msg := decodeComboRules(req.MatchRules)
	if code != "" {
		writeSmartRuleError(w, code, msg)
		return
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
	if rulesJSON != "" {
		if err := h.Repo.SetComboRules(r.Context(), req.ID, rulesJSON); err != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	// A combo created with rules is populated immediately, so the operator sees
	// the members the patterns selected rather than an empty list.
	h.reconcileSmartCombosBestEffort(r.Context(), "combo created")

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
		Name       string `json:"name"`
		Kind       string `json:"kind"`
		Models     any    `json:"models"`
		Strategy   string `json:"strategy"`
		MatchRules any    `json:"matchRules"`
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
	modelsJSON := existing.Models
	// An absent models field leaves the stored set alone; a present one is
	// normalized the same way a create is, so an update cannot write the
	// object rows a create refuses.
	if req.Models != nil {
		modelIDs, ok := normalizeComboModelIDs(req.Models)
		if !ok {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "models must contain valid model IDs")
			return
		}
		encoded, err := json.Marshal(modelIDs)
		if err != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		modelsJSON = string(encoded)
	}
	strategy := req.Strategy
	if strategy == "" {
		strategy = existing.Strategy
	}
	// The rules are validated before anything is written.
	rulesJSON, hasRules, code, msg := decodeComboRules(req.MatchRules)
	if code != "" {
		writeSmartRuleError(w, code, msg)
		return
	}
	if hasRules && req.Models == nil {
		// Two things happen to the stored model list when the rule list is
		// replaced and the caller sent no list of its own:
		//
		//  1. it is normalized, because the reconcile reads that column as a
		//     plain string array and a legacy object row would be unreadable;
		//  2. the members the removed rules owned are dropped. Without this a
		//     replaced rule list would orphan its old members into pins, and a
		//     model the operator just un-matched would sit in the combo forever
		// with nothing left to take it out.
		members, ok := normalizeComboModelIDs(existing.Models)
		if !ok {
			members, _ = comboModels(existing.Models)
		}
		// GetComboById deliberately does not read the rules column — the
		// resolver's own read stays exactly what it was — so the stored list is
		// fetched separately here.
		storedRules, storedErr := h.Repo.GetComboRules(r.Context(), id)
		oldRules, oldErr := smart.ParseRules(storedRules)
		newRules, newErr := smart.ParseRules(rulesJSON)
		if storedErr == nil && oldErr == nil && newErr == nil {
			// stringDiff(before, after): the second result is what before had
			// and after lacks, which is exactly the set the removed rules owned.
			_, orphans := stringDiff(smart.AutoMembers(oldRules), smart.AutoMembers(newRules))
			if len(orphans) > 0 {
				kept := make([]string, 0, len(members))
				for _, m := range members {
					if slices.Contains(orphans, m) {
						continue
					}
					kept = append(kept, m)
				}
				members = kept
			}
		}
		if encoded, err := json.Marshal(members); err == nil {
			modelsJSON = string(encoded)
		}
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
	if hasRules {
		if err := h.Repo.SetComboRules(r.Context(), id, rulesJSON); err != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	// An edited rule list is applied immediately, so the card shows the members
	// the new patterns select instead of waiting for the next model write.
	h.reconcileSmartCombosBestEffort(r.Context(), "combo updated")

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok", "id": id})
}

// autoFreeComboKind marks a combo that the dashboard auto-generates from the
// registry's free-tier models. The kind is informational: it tags the combo as
// machine-generated so the card can badge it, and it is what Auto Free Tier
// rewrites on every rebuild. It grants no lock — a generated combo is an
// ordinary row that can be renamed, edited and deleted like any other, and
// rebuilding after a delete recreates it.
const autoFreeComboKind = "auto-free"

// AutoFreeComboID is the stable id of the auto-generated free-tier combo.
const AutoFreeComboID = "auto-free-tier"

// HandleAutoFreeCombo handles POST /api/combos/auto-free.
// (Re)builds the free-tier combo from registry models of providers the user
// actually has a connection for, so the combo never references unreachable
// providers. The write is an upsert on AutoFreeComboID, so rebuilding an
// edited combo replaces the edits — which is why the rebuild is an explicit
// button rather than something that happens on connection changes.
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
//
// The free-tier marker is a naming convention, not a service kind: ":free" /
// "/free" / "-free" also lands on embedding, image, tts and stt models
// (openrouter's "nvidia/llama-nemotron-embed-vl-1b-v2:free" is one). The
// registry carries the real kind, so a combo that cannot serve a chat turn
// never enters the fallback chain — same gate usableModelFamilies applies to
// the auto-family combos.
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
			if kind := providers.GetProviderModelKind(providerID, modelID); kind != "" && kind != "llm" {
				continue
			}
			out = append(out, providers.GetProviderAlias(providerID)+"/"+modelID)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
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

// normalizeComboModelIDs turns the models field of a combo write into plain
// model id strings, or reports false when an entry carries no id at all.
//
// The dashboard's own client only ever sends strings, but the same endpoint is
// called by the legacy TUI, which sends the model objects it holds in memory:
// {fullModel} or {provider, model}. Persisted verbatim those objects are stored
// as objects in a column the reader (comboModels) only unmarshals as []string,
// so the row comes back unreadable and takes the whole Combos view down with
// it. Coercion happens at the API boundary instead of on read, because a row
// already carrying objects cannot be told apart from a well-formed one — and
// upstream does not migrate those either.
//
// A JSON array string is accepted as well: the client type is
// `models: string | string[]`, and that string form is what the column holds.
func normalizeComboModelIDs(v any) ([]string, bool) {
	if v == nil {
		return nil, true
	}
	if s, ok := v.(string); ok {
		if strings.TrimSpace(s) == "" {
			return nil, false
		}
		var decoded any
		if err := json.Unmarshal([]byte(s), &decoded); err != nil {
			return nil, false
		}
		return normalizeComboModelIDs(decoded)
	}
	entries, ok := v.([]any)
	if !ok {
		// A typed slice (a Go caller, or a round-tripped value) is not what
		// json.Unmarshal produces, but the shapes are the same.
		if typed, ok := v.([]string); ok {
			entries = make([]any, len(typed))
			for i, id := range typed {
				entries[i] = id
			}
		} else {
			return nil, false
		}
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		id, ok := comboModelID(entry)
		if !ok {
			return nil, false
		}
		ids = append(ids, id)
	}
	return ids, true
}

// comboModelID coerces one entry of a combo model list. A string is the id
// itself; an object is one of the two legacy shapes, fullModel winning over
// provider+model because the TUI writes both and fullModel is the resolved one.
func comboModelID(entry any) (string, bool) {
	switch e := entry.(type) {
	case string:
		if strings.TrimSpace(e) == "" {
			return "", false
		}
		return e, true
	case map[string]any:
		if full, ok := e["fullModel"].(string); ok && strings.TrimSpace(full) != "" {
			return full, true
		}
		provider, pok := e["provider"].(string)
		model, mok := e["model"].(string)
		if pok && mok && strings.TrimSpace(provider) != "" && strings.TrimSpace(model) != "" {
			return provider + "/" + model, true
		}
	}
	return "", false
}

// HandleDeleteCombo handles DELETE /api/combos/{id}.
func (h *DashboardHandler) HandleDeleteCombo(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing combo id")
		return
	}

	if err := h.Repo.DeleteCombo(id); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok", "id": id})
}
