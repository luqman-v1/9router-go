package dashboard

import (
	"net/http"
	"slices"
	"strings"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/pricing"
	"9router/proxy/internal/providers"
)

// isModelFree evaluates whether a model is free by combining suffix heuristic
// (IsFreeTierModel) and zero-cost pricing entries.
//
// Suffix precedence: a model explicitly identified with :free, /free, or -free
// in the catalog or registry is treated as free, even if the generic vendor table
// lists a fallback rate for the unadorned model name (e.g. deepseek-v4-flash-free).
func isModelFree(provider, modelID string) bool {
	if providers.IsFreeTierModel(modelID) {
		return true
	}
	if p, ok := pricing.GetPricingForModel(provider, modelID); ok {
		if p.InputPer1M == 0 && p.OutputPer1M == 0 {
			return true
		}
	}
	return false
}

// modelCaps is the per-model capability block the dashboard needs to render the
// icons next to a model row and to decide which thinking levels apply. It is the
// subset of /v1/models capabilities the provider detail page consumes, plus the
// thinking level list that upstream computes in getThinkingLevels and the
// dashboard uses for the "Thinking: <level>" picker and the "(level)" suffix.
type modelCaps struct {
	Vision         bool     `json:"vision"`
	Search         bool     `json:"search"`
	Reasoning      bool     `json:"reasoning"`
	ContextWindow  int      `json:"contextWindow"`
	MaxOutput      int      `json:"maxOutput"`
	ThinkingLevels []string `json:"thinkingLevels"`
	Free           bool     `json:"free"`
}

// HandleGetModelCaps handles GET /api/models/caps?provider=<id>.
//
// The model catalog is served to the dashboard as a static bundle, but
// capabilities and thinking levels are server-side state: they depend on the
// provider registry, the capability tables and the synced models.dev catalog, and
// upstream computes them on the server for exactly this reason. The provider is
// resolved by id or by alias, so both `/dashboard/providers/commandcode` and the
// `cmc` storage prefix resolve to the same block.
func (h *DashboardHandler) HandleGetModelCaps(w http.ResponseWriter, r *http.Request) {
	provider := r.URL.Query().Get("provider")
	if provider == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "provider is required")
		return
	}

	resolved := providers.ResolveAlias(provider)
	models := providers.GetProviderModels(resolved)
	if len(models) == 0 {
		if custom := h.customModelCaps(provider, resolved); len(custom) > 0 {
			handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
				"provider": provider,
				"caps":     custom,
			})
			return
		}
		if strings.HasPrefix(provider, "openai-compatible") || strings.HasPrefix(provider, "anthropic-compatible") {
			handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
				"provider": provider,
				"caps":     map[string]any{},
			})
			return
		}
		if h.Repo != nil {
			if node, _, err := h.Repo.GetProviderNodeByID(provider); err == nil && node != nil {
				handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
					"provider": provider,
					"caps":     map[string]any{},
				})
				return
			}
		}
		handlerutil.WriteJSONError(w, http.StatusNotFound, "unknown provider or no static models")
		return
	}

	caps := make(map[string]modelCaps, len(models))
	for _, model := range models {
		detail := providers.GetCapabilitiesDetailForModel(resolved, model)
		entry := modelCaps{
			Vision:        detail.Vision,
			Search:        detail.Search,
			Reasoning:     detail.Reasoning,
			ContextWindow: detail.ContextWindow,
			MaxOutput:     detail.MaxOutput,
			Free:          isModelFree(resolved, model),
		}
		if levels := providers.GetThinkingLevels(resolved, model); levels != nil {
			entry.ThinkingLevels = levels
		}
		caps[model] = entry
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"provider": resolved,
		"caps":     caps,
	})
}

// customModelCaps builds the capability block for the custom models registered
// on a provider. A provider node carrying only custom rows has no registry
// catalog at all, so before this the endpoint answered caps: {} and the
// dashboard never saw the models it had just been given — nor the limits they
// declare.
func (h *DashboardHandler) customModelCaps(provider, resolved string) map[string]modelCaps {
	if h.Repo == nil {
		return nil
	}
	customs, err := h.Repo.GetCustomModels()
	if err != nil {
		return nil
	}
	aliases := []string{provider, resolved}
	if node, _, err := h.Repo.GetProviderNodeByPrefix(provider); err == nil && node != nil {
		aliases = append(aliases, node.ID)
	}
	caps := map[string]modelCaps{}
	for _, cm := range customs {
		if cm == nil || !isLLMCustomModelType(cm.Type) || !slices.Contains(aliases, cm.ProviderAlias) {
			continue
		}
		// The same publication /v1/models does. Without it the row's saved
		// flags and limits never reach a capability lookup on this path.
		publishCustomModelCaps(cm)
		detail := providers.GetCapabilitiesDetailForModel(cm.ProviderAlias, cm.ID)
		entry := modelCaps{
			Vision:        detail.Vision,
			Search:        detail.Search,
			Reasoning:     detail.Reasoning,
			ContextWindow: detail.ContextWindow,
			MaxOutput:     detail.MaxOutput,
			Free:          isModelFree(cm.ProviderAlias, cm.ID),
		}
		if levels := providers.GetThinkingLevels(cm.ProviderAlias, cm.ID); levels != nil {
			entry.ThinkingLevels = levels
		}
		caps[cm.ID] = entry
	}
	return caps
}

// publishCustomModelCaps registers one custom model's saved block in the
// capability registry under the provider alias the row is stored against — the
// same publication /v1/models performs before it resolves a capability, so
// this endpoint and the discovery list cannot disagree about the same row.
func publishCustomModelCaps(cm *db.CustomModel) {
	var caps providers.Capabilities
	for name, on := range cm.Caps {
		switch name {
		case "vision":
			caps.Vision = on
		case "reasoning":
			caps.Reasoning = on
		case "search":
			caps.Search = on
		case "tools":
			caps.Tools = on
		case "image", "imageOutput":
			caps.ImageOutput = on
		case "audio":
			caps.AudioInput = on
		case "pdf":
			caps.PDF = on
		}
	}
	caps.ContextWindow = cm.ContextWindow
	caps.MaxOutput = cm.MaxOutput
	providers.SetCustomModelCaps(cm.ProviderAlias, cm.ID, caps)
}

// isLLMCustomModelType keeps image/audio/video custom rows out of a chat
// capability map, matching how /v1/models decides what is an LLM entry.
func isLLMCustomModelType(modelType string) bool {
	switch modelType {
	case "", "llm", "chat", "completion":
		return true
	}
	return false
}
