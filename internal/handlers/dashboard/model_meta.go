package dashboard

import (
	"net/http"
	"strings"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/providers"
)

// ModelMeta is everything the combo editor's sort dropdown needs about one
// model, in one read. The sorts are one-shot reorders of a stored list, so they
// run entirely in the browser — which means the browser needs the numbers, and
// fetching them per option would be four round trips for one dropdown.
//
// Every field is optional because every source is partial: the latency ring
// only knows models that have served a request, the registry only knows models
// it declares, and a custom model row only knows what the operator typed in.
// A zero means "not known", never "fastest" or "smallest".
type ModelMeta struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// TTFTMs and TotalMs are means over the retained requestDetails window.
	TTFTMs  int64 `json:"ttftMs"`
	TotalMs int64 `json:"totalMs"`
	// Samples is how many observations the means rest on. Zero means the model
	// has never served a request, which the sort treats as unproven rather
	// than as slow.
	Samples int64 `json:"samples"`
	// ContextWindow is the declared window, or 0 when nothing declared one.
	ContextWindow int `json:"contextWindow"`
	// Kind is the declared service kind ("llm", "embedding", "image", ...),
	// or "" when unknown. An unknown kind sorts with chat rather than last: a
	// custom model whose kind the catalog has not learned is still a chat
	// model in practice.
	Kind string `json:"kind"`
	// Vision, Reasoning and AudioInput are the capability flags the sort uses
	// to float capable models to the front.
	Vision     bool `json:"vision"`
	Reasoning  bool `json:"reasoning"`
	AudioInput bool `json:"audioInput"`
}

// HandleGetModelMeta handles GET /api/models/meta.
//
// It answers "what do I know about each of my models", which is what the combo
// editor's sort dropdown consumes. The response is a map keyed by
// "provider|model" so a caller can look a combo member up directly.
//
// Both spellings of a provider node are published — the node id the latency
// ring and the custom-model rows are keyed by, and the display prefix a combo
// member is spelled with — because that is the same duality /v1/models and the
// smart-rule candidates already carry. Publishing only one would leave the
// editor unable to match half its own members.
func (h *DashboardHandler) HandleGetModelMeta(w http.ResponseWriter, r *http.Request) {
	aggs, err := h.Repo.GetModelLatency()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	prefixMap, err := h.Repo.GetProviderNodePrefixMap()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	customs, err := h.Repo.GetCustomModels()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	out := make(map[string]ModelMeta)

	// 1. The latency ring, keyed by the provider the gateway recorded.
	for _, a := range aggs {
		key := a.Provider + "|" + a.Model
		entry := out[key]
		entry.Provider, entry.Model = a.Provider, a.Model
		entry.TTFTMs, entry.TotalMs, entry.Samples = a.TTFTMs, a.TotalMs, a.Samples
		out[key] = entry
	}

	// 2. Custom-model rows, which carry the operator's own declarations of
	//    kind, capabilities and window. These are keyed by the storage alias,
	//    which for a node is the node id.
	for _, cm := range customs {
		if cm == nil || cm.ID == "" || cm.ProviderAlias == "" {
			continue
		}
		key := cm.ProviderAlias + "|" + cm.ID
		entry := out[key]
		entry.Provider, entry.Model = cm.ProviderAlias, cm.ID
		if cm.ContextWindow > 0 {
			entry.ContextWindow = cm.ContextWindow
		}
		if kind := strings.ToLower(strings.TrimSpace(cm.Type)); kind != "" {
			entry.Kind = kind
		}
		if cm.Caps["vision"] {
			entry.Vision = true
		}
		if cm.Caps["reasoning"] {
			entry.Reasoning = true
		}
		if cm.Caps["audio"] || cm.Caps["audioInput"] {
			entry.AudioInput = true
		}
		out[key] = entry
	}

	// 3. The static registry, which knows the kind and capabilities of every
	//    model it declares. It is consulted last so a custom row's own
	//    declaration wins over the catalog's guess.
	for key, entry := range out {
		if entry.Kind == "" {
			if kind := providers.GetProviderModelKind(entry.Provider, entry.Model); kind != "" {
				entry.Kind = kind
			}
		}
		caps := providers.GetCapabilitiesForModel(entry.Provider, entry.Model)
		if !entry.Vision && caps.Vision {
			entry.Vision = true
		}
		if !entry.Reasoning && caps.Reasoning {
			entry.Reasoning = true
		}
		if !entry.AudioInput && caps.AudioInput {
			entry.AudioInput = true
		}
		out[key] = entry
	}

	// 4. Every entry is republished under the node's display prefix, so a
	//    member spelled "cutad/model" finds the numbers recorded under the
	//    node id. The prefix spelling is what a combo stores.
	prefixed := make(map[string]ModelMeta, len(out)*2)
	for key, entry := range out {
		prefixed[key] = entry
		if prefix, ok := prefixMap[entry.Provider]; ok && prefix != "" && prefix != entry.Provider {
			prefixed[prefix+"|"+entry.Model] = entry
		}
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"meta": prefixed})
}
