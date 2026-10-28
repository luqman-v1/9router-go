package dashboard

import (
	json "encoding/json/v2"
	"errors"
	"net/http"
	"strings"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
)

// Bulk custom-model save (docs/provider-limits-plan.md D9).
//
// Pasting a list of ids is one action, so it is one request: N calls from the
// dashboard would leave a half-finished paste with nothing to report it, and the
// ids that were refused would exist only in the browser. The batch therefore
// answers with the ids it stored and one entry per line it did not, and the rows
// it does store land in a single transaction — "8 added" must not become 3 rows
// plus a crash.
//
// Capabilities come in once for the whole pasted list (D10): a per-line syntax
// like "id vision 8192" is not offered, because an operator who needs different
// settings can paste the batch and then edit one model. Every entry carries them
// anyway, so the endpoint stays general and a caller that already has per-entry
// values is not forced to collapse them.

// The two ways a batch body can be malformed, named so the 400 says which one it
// was instead of a bare "invalid JSON" for a body that was valid JSON.
var (
	errBatchModelsNotAList  = errors.New("models must be a list of ids or model objects")
	errBatchEntryUnreadable = errors.New("a model entry must be an id string or a model object")
)

// customModelBatchEntry is one line of a bulk save.
type customModelBatchEntry struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Type          string `json:"type"`
	Vision        bool   `json:"vision"`
	Reasoning     bool   `json:"reasoning"`
	ContextWindow int    `json:"contextWindow"`
	MaxOutput     int    `json:"maxOutput"`
}

// customModelBatchSkip explains one line the batch left out. Reason is the
// sentence the dashboard shows next to the id; Code is the machine-readable form
// of the same refusal, for a caller that wants to group them.
type customModelBatchSkip struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
	Code   string `json:"code"`
}

// saveCustomModelsBatch handles the array form of POST /api/models/custom.
func (h *DashboardHandler) saveCustomModelsBatch(w http.ResponseWriter, r *http.Request, data map[string]any) {
	alias := strings.TrimSpace(stringOf(data["providerAlias"]))
	if alias == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing providerAlias for a bulk save")
		return
	}
	batchType := strings.TrimSpace(stringOf(data["type"]))
	if batchType == "" {
		batchType = "llm"
	}

	entries, err := decodeBatchEntries(data["models"])
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	// One read of the scope: the batch has to know which keys already exist to
	// report them as skipped instead of silently overwriting what is there.
	existing, err := h.Repo.GetKVScope("customModels")
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	added := make([]string, 0, len(entries))
	skipped := make([]customModelBatchSkip, 0)
	writes := make([]db.KVEntry, 0, len(entries))
	seen := make(map[string]bool, len(entries))

	for _, entry := range entries {
		modelID := strings.TrimSpace(entry.ID)
		if modelID == "" {
			skipped = append(skipped, customModelBatchSkip{ID: entry.ID, Reason: "empty id", Code: "EMPTY_ID"})
			continue
		}
		// An id that still carries a separator is a line that was never split. Storing it
		// would create a model addressable only by reproducing the spaces exactly.
		if reason := invalidModelIDReason(modelID); reason != "" {
			skipped = append(skipped, customModelBatchSkip{ID: modelID, Reason: reason, Code: "INVALID_ID"})
			continue
		}
		modelType := strings.TrimSpace(entry.Type)
		if modelType == "" {
			modelType = batchType
		}
		key := customModelKey(alias, modelID, modelType)

		if seen[key] {
			skipped = append(skipped, customModelBatchSkip{ID: modelID, Reason: "repeated in this batch", Code: "DUPLICATE_IN_BATCH"})
			continue
		}
		if _, exists := existing[key]; exists {
			skipped = append(skipped, customModelBatchSkip{ID: modelID, Reason: "already exists on this provider", Code: "ALREADY_EXISTS"})
			continue
		}

		// The same rules a single save applies, asked without writing: a batch must
		// not accept a name the single-save path refuses, or a caller that retries
		// one line at a time would see it rejected only then.
		if code, detail, conflict := h.nameConflict(nsCustomModel, modelID); conflict {
			skipped = append(skipped, customModelBatchSkip{ID: modelID, Reason: detail, Code: code})
			continue
		}

		value, err := json.Marshal(customModelValue(alias, modelID, modelType, entry))
		if err != nil {
			skipped = append(skipped, customModelBatchSkip{ID: modelID, Reason: "could not encode the model", Code: "ENCODE_FAILED"})
			continue
		}
		seen[key] = true
		added = append(added, modelID)
		writes = append(writes, db.KVEntry{Key: key, Value: string(value)})
	}

	if err := h.Repo.SetKVMany(r.Context(), "customModels", writes); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// A bulk import is the path an operator uses to pull a whole /models list,
	// so it is the one most likely to hand a rule new members. One pass covers
	// the whole batch; it runs after the write is answered.
	h.reconcileSmartCombosBestEffort(r.Context(), "models imported")

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"added":   added,
		"skipped": skipped,
	})
}

// customModelKey is the kv key of one custom model: the same composition the
// single save builds, so a batch and a hand-added model land on one row rather
// than two spellings of it.
func customModelKey(alias, modelID, modelType string) string {
	return alias + "|" + modelID + "|" + modelType
}

// customModelValue is the stored JSON, shaped like the value the single save
// writes: the reader resolves providerAlias/id/type/caps and the two token limits
// off it, so a batch entry that omitted any of them would be a model the rest of
// the gateway cannot size.
func customModelValue(alias, modelID, modelType string, entry customModelBatchEntry) map[string]any {
	name := strings.TrimSpace(entry.Name)
	if name == "" {
		name = modelID
	}
	return map[string]any{
		"providerAlias": alias,
		"id":            modelID,
		"type":          modelType,
		"name":          name,
		"caps": map[string]any{
			"vision":    entry.Vision,
			"reasoning": entry.Reasoning,
		},
		"contextWindow": entry.ContextWindow,
		"maxOutput":     entry.MaxOutput,
	}
}

// decodeBatchEntries accepts both shapes a caller can reasonably send: plain ids,
// which is what a pasted list is, and objects for a caller that carries per-entry
// values. Anything else is a 400 rather than a silently dropped line.
func decodeBatchEntries(raw any) ([]customModelBatchEntry, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, errBatchModelsNotAList
	}
	entries := make([]customModelBatchEntry, 0, len(items))
	for _, item := range items {
		switch value := item.(type) {
		case string:
			entries = append(entries, customModelBatchEntry{ID: value})
		case map[string]any:
			encoded, err := json.Marshal(value)
			if err != nil {
				return nil, errBatchEntryUnreadable
			}
			var entry customModelBatchEntry
			if err := json.Unmarshal(encoded, &entry); err != nil {
				return nil, errBatchEntryUnreadable
			}
			entries = append(entries, entry)
		default:
			return nil, errBatchEntryUnreadable
		}
	}
	return entries, nil
}

// stringOf reads a JSON string without panicking on a non-string value.
func stringOf(v any) string {
	s, _ := v.(string)
	return s
}
