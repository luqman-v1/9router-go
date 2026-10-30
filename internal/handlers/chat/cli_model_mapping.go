package chat

import (
	"net/http"
	"strings"

	"9router/proxy/internal/db"
	"9router/proxy/internal/log"
)

// cliMappingSourceKeys expands a slot name into the model strings a Claude
// Code client may actually send for it. The bare slot is what the tool sends
// for default routing; the family-qualified forms arrive when the user picks
// an explicit variant (e.g. "claude-opus-5[1m]" strips to claude-opus-5).
// Port of Cartethyia's cliMappingSourceKeys (route-catalog.ts).
func cliMappingSourceKeys(slot string) []string {
	switch slot {
	case "opus":
		return []string{"opus", "claude-opus-4", "claude-opus-5", "claude-opus"}
	case "sonnet":
		return []string{"sonnet", "claude-sonnet-4-5", "claude-sonnet-5", "claude-sonnet"}
	case "haiku":
		return []string{"haiku", "claude-haiku-4-5", "claude-haiku-5", "claude-haiku"}
	default:
		return []string{slot}
	}
}

// applyCliModelMapping rewrites the requested model through a key's CLI tool
// mapping table before resolveModel sees it. Both gates must pass — the key
// must carry the routing:cli_mapping scope AND the request's User-Agent must
// look like the mapped tool — or the model resolves through ordinary aliases.
// Port of Cartethyia's ProxyRequestPreparer.prepare CLI gate
// (src/transport/request/preparer.ts) + route-catalog.ts merge order.
//
// Returns the effective model (mapped or original). Logs when a rewrite
// happens so usage rows can be attributed.
func (h *ChatHandler) applyCliModelMapping(r *http.Request, requestedModel string) string {
	if requestedModel == "" {
		return requestedModel
	}
	ua := r.Header.Get("User-Agent")
	if ua == "" {
		return requestedModel
	}
	toolID := db.DetectCliToolID(ua)
	if toolID == "" {
		return requestedModel
	}
	key := requestKey(r)
	if key == nil || !db.HasCliMappingScope(key) {
		return requestedModel
	}
	slot := normalizeCliSlot(requestedModel)
	if slot == "" {
		return requestedModel
	}
	if h == nil || h.Repo == nil {
		return requestedModel
	}
	target, err := h.Repo.ResolveCliModel(key.ID, toolID, slot)
	if err != nil || target == "" {
		return requestedModel
	}
	log.Info("chat", "cli model mapping applied", "key", key.ID, "tool", toolID, "slot", requestedModel, "target", target)
	return target
}

// normalizeCliSlot reduces a full model id to its slot when the full name has
// no mapping of its own: a trailing variant marker is stripped first
// (claude-opus-5[1m] → claude-opus-5), then a family-slot fallback
// (claude-opus-5 → opus). Port of Cartethyia's normalizeAliasKey.
func normalizeCliSlot(model string) string {
	stripped := stripModelContextMarker(model)
	if !strings.Contains(stripped, "/") {
		return stripped
	}
	return stripped
}
