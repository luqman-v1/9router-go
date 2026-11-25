package chat

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
)

func allComboModelsUnavailableError() *upstreamError {
	return &upstreamError{
		StatusCode: http.StatusServiceUnavailable,
		Body:       []byte(`{"error":{"message":"all combo models are currently disabled or unavailable","type":"service_unavailable","code":503}}`),
	}
}

// comboAvailability holds a per-dispatch snapshot of disabled models and
// provider connection activation status so filtering a combo never issues per-
// entry database queries inside a loop.
type comboAvailability struct {
	disabled       map[string]map[string]bool
	providerActive map[string]bool
	nodeIDToPrefix map[string]string
	prefixToNodeID map[string]string
}

func (h *ChatHandler) loadComboAvailability(ctx context.Context) *comboAvailability {
	avail := &comboAvailability{
		disabled: h.disabledModelIndex(),
	}
	if h.Repo == nil {
		return avail
	}
	if status, err := h.Repo.ProviderActiveStatus(ctx); err == nil {
		avail.providerActive = status
	}
	if prefixMap, err := h.Repo.GetProviderNodePrefixMap(); err == nil && len(prefixMap) > 0 {
		avail.nodeIDToPrefix = prefixMap
		avail.prefixToNodeID = make(map[string]string, len(prefixMap))
		for nodeID, prefix := range prefixMap {
			if prefix != "" {
				avail.prefixToNodeID[prefix] = nodeID
			}
		}
	}
	return avail
}

// isEntryAvailable reports whether one combo entry ("<provider>/<model>")
// belongs to an enabled provider and is not in disabledModels, matching purely
// on that provider's own exact identities without cross-provider aliasing.
func (a *comboAvailability) isEntryAvailable(entry string) bool {
	if a == nil {
		return true
	}
	prefix, modelID, ok := strings.Cut(entry, "/")
	if !ok || prefix == "" || modelID == "" {
		return true
	}

	providerKey := ""
	disabledKeys := make([]string, 0, 3)
	disabledKeys = append(disabledKeys, prefix)

	if nodeID, isNodePrefix := a.prefixToNodeID[prefix]; isNodePrefix {
		providerKey = nodeID
		disabledKeys = append(disabledKeys, nodeID)
	} else if nodePrefix, isNodeID := a.nodeIDToPrefix[prefix]; isNodeID {
		providerKey = prefix
		if nodePrefix != "" {
			disabledKeys = append(disabledKeys, nodePrefix)
		}
	} else {
		canon := resolveProviderAlias(prefix)
		providerKey = canon
		if canon != "" && canon != prefix {
			disabledKeys = append(disabledKeys, canon)
		}
		if alias := providers.GetProviderAlias(canon); alias != "" && alias != prefix && alias != canon {
			disabledKeys = append(disabledKeys, alias)
		}
	}

	for _, key := range disabledKeys {
		if set := a.disabled[key]; len(set) > 0 {
			if set[modelID] || set[prefix+"/"+modelID] {
				return false
			}
		}
	}

	if a.providerActive != nil {
		active, hasConns := a.providerActive[providerKey]
		if !hasConns && prefix != providerKey {
			active, hasConns = a.providerActive[prefix]
		}
		if hasConns && !active {
			return false
		}
	}
	return true
}

func (a *comboAvailability) filterModels(entries []string) ([]string, []string) {
	if len(entries) == 0 {
		return entries, nil
	}
	kept := make([]string, 0, len(entries))
	var skipped []string
	for _, entry := range entries {
		if a.isEntryAvailable(entry) {
			kept = append(kept, entry)
		} else {
			skipped = append(skipped, entry)
		}
	}
	return kept, skipped
}

// filterAvailableComboModels drops disabled/inactive entries from comboModels
// and adapterModels using a single batched availability snapshot.
func (h *ChatHandler) filterAvailableComboModels(ctx context.Context, comboModels []string, adapterModels []string) ([]string, []string) {
	if h.Repo == nil || len(comboModels) == 0 {
		return comboModels, adapterModels
	}
	avail := h.loadComboAvailability(ctx)
	keptModels, skippedModels := avail.filterModels(comboModels)
	keptAdapters := adapterModels
	if len(adapterModels) > 0 {
		keptAdapters, _ = avail.filterModels(adapterModels)
	}
	if len(skippedModels) > 0 {
		log.Debug("combo", "skipped disabled/unavailable combo models", "skipped", slices.Clone(skippedModels))
	}
	return keptModels, keptAdapters
}
