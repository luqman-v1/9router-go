package dashboard

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"sort"
	"strings"

	"9router/proxy/internal/handlerutil"
)

// autoDisableRequest is the body of POST /api/models/auto-disable.
//
// `models` is the list the caller already knows is dead — the dashboard runs
// the probes and sends the ids, rather than this handler running a second
// sweep. That keeps one probe per model per operator action: a "Test all"
// followed by "disable the dead ones" costs one round of requests, not two.
//
// `dryRun` reports what would be disabled without writing anything, so the
// button can show the count before the operator commits to it.
type autoDisableRequest struct {
	Provider string   `json:"provider"`
	Models   []string `json:"models"`
	DryRun   bool     `json:"dryRun"`
}

// autoDisableResult reports what the call did, per model.
type autoDisableResult struct {
	Provider        string   `json:"provider"`
	Disabled        []string `json:"disabled"`
	AlreadyDisabled []string `json:"alreadyDisabled"`
	Total           int      `json:"total"`
	DryRun          bool     `json:"dryRun"`
}

// HandleAutoDisableModels handles POST /api/models/auto-disable.
//
// It folds a caller-supplied list of dead model ids into the provider's
// disabledModels KV row, leaving everything already in that row alone. The
// point of the endpoint is the merge: a caller that already holds the full
// list (the dashboard does, from the row it rendered) can PUT it directly,
// but a caller holding only "these three just failed" would otherwise have to
// read, merge and write — and a read-modify-write from the browser is how two
// tabs disable each other's models.
//
// A model that is already disabled is reported separately rather than
// silently dropped, so the response can say "2 newly disabled, 1 already off"
// instead of a count that does not match what the operator selected.
func (h *DashboardHandler) HandleAutoDisableModels(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}

	var req autoDisableRequest
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	// The read already finished, so a close error here cannot change the
	// answer — but errcheck still flags the bare defer, and the six files that
	// spell the discard out are the ones that pass the gate.
	defer func() { _ = r.Body.Close() }()
	req.Provider = strings.TrimSpace(req.Provider)
	if req.Provider == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing provider")
		return
	}

	// Normalise the incoming ids: trimmed, deduped, empties dropped. The
	// caller sends ids it read off its own list, so a duplicate means the
	// operator selected the same row twice, not that two models are dead.
	want := make([]string, 0, len(req.Models))
	seen := make(map[string]bool, len(req.Models))
	for _, id := range req.Models {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		want = append(want, id)
	}
	sort.Strings(want)

	current, err := h.disabledModelsFor(req.Provider)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	result := autoDisableResult{
		Provider:        req.Provider,
		Disabled:        []string{},
		AlreadyDisabled: []string{},
		Total:           len(want),
		DryRun:          req.DryRun,
	}
	merged := make([]string, 0, len(current)+len(want))
	merged = append(merged, current...)
	inCurrent := make(map[string]bool, len(current))
	for _, id := range current {
		inCurrent[id] = true
	}
	for _, id := range want {
		if inCurrent[id] {
			result.AlreadyDisabled = append(result.AlreadyDisabled, id)
			continue
		}
		inCurrent[id] = true
		merged = append(merged, id)
		result.Disabled = append(result.Disabled, id)
	}
	sort.Strings(merged)

	if req.DryRun || len(result.Disabled) == 0 {
		handlerutil.WriteJSON(w, http.StatusOK, result)
		return
	}

	encoded, err := json.Marshal(merged)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to marshal models")
		return
	}
	if err := h.Repo.SetKV("disabledModels", req.Provider, string(encoded)); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// A model that just left the listing has to leave every combo it was in,
	// the same way a manual disable does.
	h.reconcileSmartCombosBestEffort(r.Context(), "models auto-disabled")
	handlerutil.WriteJSON(w, http.StatusOK, result)
}

// disabledModelsFor reads one provider's disabled list, tolerating a missing
// row (a provider nobody has disabled anything on yet) and a malformed one
// (a row written by an older build) by answering an empty list rather than an
// error — the caller is about to overwrite it anyway.
func (h *DashboardHandler) disabledModelsFor(provider string) ([]string, error) {
	kv, err := h.Repo.GetKVScope("disabledModels")
	if err != nil {
		return nil, err
	}
	raw, ok := kv[provider]
	if !ok || strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil, nil
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			out = append(out, id)
		}
	}
	return out, nil
}
