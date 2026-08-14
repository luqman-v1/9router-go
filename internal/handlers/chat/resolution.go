package chat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/shared"
	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy/executor"
	"9router/proxy/internal/proxy/oauth"
)

// NewChatHandler creates a ChatHandler with the given repository and a streaming-capable HTTP client.
// Pass a TokenSaverConfig to enable token saver features, or nil for all-off defaults.
func NewChatHandler(repo *db.Repo, ts ...*shared.TokenSaverConfig) *ChatHandler {
	executor.RegisterAll()
	oauth.RegisterAll()
	cfg := &shared.TokenSaverConfig{}
	if len(ts) > 0 && ts[0] != nil {
		cfg = ts[0]
	}
	// Timeout: 0 is required so long SSE streams are not cut short, but a
	// ResponseHeaderTimeout bounds how long we wait for the upstream to
	// start responding — closing the "accept then go silent" gap without
	// killing a stream that has already begun.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 2 * time.Minute
	return &ChatHandler{
		Repo: repo,
		Client: &http.Client{
			Transport: transport,
			Timeout:   0, // no timeout for streaming support
		},
		TokenSaver:  cfg,
		stickyState: make(map[string]*comboStickyState),
	}
}

// ResolveModel resolves a model string through aliases, combos, and provider/model parsing.
// Exported so other handlers (media, responses, etc.) can resolve model names.
func (h *ChatHandler) ResolveModel(modelStr string) (*ModelInfo, error) {
	return h.resolveModel(modelStr)
}

// resolveProviderAlias resolves a provider alias to its canonical ID.
func resolveProviderAlias(alias string) string {
	if canonical, ok := providers.ProviderAliasMap[alias]; ok {
		return canonical
	}
	return alias
}

// resolveModelEntry parses a single "provider/model" string into a ModelInfo
// without combo or alias resolution (used when iterating combo entries).
// If the entry has no "/" (i.e. it's a combo name), it resolves the combo
// and returns its first concrete model with the combined model list.
func (h *ChatHandler) resolveModelEntry(entry string) *ModelInfo {
	if !strings.Contains(entry, "/") {
		combo, err := h.Repo.GetComboByName(entry)
		if err == nil && combo != nil && combo.Models != "" {
			var subModels []string
			if err := json.Unmarshal([]byte(combo.Models), &subModels); err == nil && len(subModels) > 0 {
				first := h.resolveModelEntry(subModels[0])
				if first != nil {
					first.ComboModels = subModels
					first.Strategy = combo.Strategy
					return first
				}
			}
		}
		return nil
	}
	parts := strings.SplitN(entry, "/", 2)
	provider := resolveProviderAlias(parts[0])
	if _, ok := providers.KnownProviders[provider]; !ok {
		if info := h.resolvePrefixProvider(provider, parts[1]); info != nil {
			return info
		}
	}
	return &ModelInfo{Provider: provider, Model: parts[1]}
}

// flattenComboModels recursively expands combo-name entries into concrete
// "provider/model" leaves, keeping order and deduping consecutive identical
// leaves so a nested combo can't create pointless rotation slots. Guards
// against cyclic combo references by skipping recursive cycles. Inner-combo
// strategies are not applied here; the top-level combo's strategy governs
// the flattened list.
func (h *ChatHandler) flattenComboModels(models []string) ([]string, error) {
	out := make([]string, 0, len(models))
	seen := make(map[string]bool)
	var walk func([]string) error
	walk = func(ms []string) error {
		for _, m := range ms {
			if !strings.Contains(m, "/") {
				if seen[m] {
					log.Warn("combo", "cyclic combo reference detected, skipping", "combo", m)
					continue
				}
				if combo, err := h.Repo.GetComboByName(m); err == nil && combo != nil && combo.Models != "" {
					var sub []string
					if err := json.Unmarshal([]byte(combo.Models), &sub); err == nil {
						seen[m] = true
						if err := walk(sub); err != nil {
							return err
						}
						delete(seen, m)
						continue
					}
				}
				if aliasTarget, err := h.Repo.GetModelAlias(m); err == nil && aliasTarget != "" && strings.Contains(aliasTarget, "/") {
					m = aliasTarget
				}
			}
			if len(out) == 0 || out[len(out)-1] != m {
				out = append(out, m)
			}
		}
		return nil
	}
	if err := walk(models); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("combo has no valid leaf models")
	}
	return out, nil
}

// resolveModel resolves a model string through aliases, combos, and provider/model parsing.
// Returns the first concrete ModelInfo found, or an error.
func (h *ChatHandler) resolveModel(modelStr string) (*ModelInfo, error) {
	if modelStr == "" {
		return nil, fmt.Errorf("missing model")
	}

	// 1. Standard format: "provider/model"
	if strings.Contains(modelStr, "/") {
		parts := strings.SplitN(modelStr, "/", 2)
		providerAlias := parts[0]
		model := parts[1]
		provider := resolveProviderAlias(providerAlias)

		if _, ok := providers.KnownProviders[provider]; !ok {
			if info := h.resolvePrefixProvider(provider, model); info != nil {
				return info, nil
			}
		}
		return &ModelInfo{Provider: provider, Model: model}, nil
	}

	// 2. Check if it's a model alias (e.g., "gpt-4o" -> "openai/gpt-4o")
	aliasTarget, err := h.Repo.GetModelAlias(modelStr)
	if err == nil && aliasTarget != "" {
		if strings.Contains(aliasTarget, "/") {
			parts := strings.SplitN(aliasTarget, "/", 2)
			provider := resolveProviderAlias(parts[0])
			if _, ok := providers.KnownProviders[provider]; !ok {
				if info := h.resolvePrefixProvider(provider, parts[1]); info != nil {
					return info, nil
				}
			}
			return &ModelInfo{
				Provider: provider,
				Model:    parts[1],
			}, nil
		}
	}

	// 3. Check if it's a combo name
	combo, err := h.Repo.GetComboByName(modelStr)
	if err == nil && combo != nil && combo.Models != "" {
		var modelStrings []string
		if err := json.Unmarshal([]byte(combo.Models), &modelStrings); err == nil && len(modelStrings) > 0 {
			// Flatten nested combos into concrete leaves so rotation covers
			// every reachable model (a nested combo entry used to collapse to
			// its first leaf, so combo-wombo -> free-tier never rotated).
			flattened, flatErr := h.flattenComboModels(modelStrings)
			if flatErr != nil {
				return nil, flatErr
			}
			if len(flattened) > 0 {
				firstInfo := h.resolveModelEntry(flattened[0])
				if firstInfo == nil {
					firstInfo, _ = h.resolveModel(flattened[0])
				}
				if firstInfo != nil {
					firstInfo.ComboModels = flattened
					firstInfo.Strategy = combo.Strategy
					return firstInfo, nil
				}
			}
		}
	}

	// 4. Check common providers as a fallback
	for _, provider := range []string{"openai", "anthropic", "deepseek"} {
		conns, err := h.Repo.GetProviderConnections(provider, true)
		if err == nil && len(conns) > 0 {
			return &ModelInfo{Provider: provider, Model: modelStr}, nil
		}
	}

	return nil, fmt.Errorf("could not resolve model: %s", modelStr)
}

// resolvePrefixProvider checks if a provider name is a providerNode prefix.
// If so, it finds the matching connection and returns a pinned ModelInfo.
func (h *ChatHandler) resolvePrefixProvider(prefix string, model string) *ModelInfo {
	node, _, err := h.Repo.GetProviderNodeByPrefix(prefix)
	if err != nil || node == nil {
		return nil
	}

	conn, _, err := h.getBestConnection(node.ID, "", nil, model)
	if err != nil || conn == nil {
		return nil
	}

	return &ModelInfo{
		Provider:     node.ID,
		Model:        model,
		ConnectionID: conn.ID,
	}
}
