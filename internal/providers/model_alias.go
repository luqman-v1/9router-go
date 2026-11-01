package providers

import "strings"

// OutputAlias resolves the short alias a model id should be exported under,
// mirroring the upstream helper in `src/sse/services/allowedModels.js`:
//
//	const outputAlias = (
//	  conn?.providerSpecificData?.prefix   // 1. the node's configured prefix
//	  || getProviderAlias(providerId)      // 2. the static alias for the provider
//	  || staticAlias                       // 3. the provider id itself
//	).trim();
//
// Every exported model id is then built as `${outputAlias}/${modelId}`, so the
// prefix always wins over the provider's internal id. That is the whole point:
// a custom endpoint is stored under a generated key
// ("openai-compatible-chat-<uuid>", built upstream as
// `${PREFIX}${apiType}-${randomUUID()}`) but must be advertised to clients as
// `atri/glm-4.7`, never as `<uuid>/glm-4.7`.
//
// `prefix` is the node's configured prefix (empty when the caller has no node);
// `providerID` is the raw provider/node key. Returns the provider id unchanged
// when nothing more specific is known, so unknown providers keep working.
func OutputAlias(providerID, prefix string) string {
	if p := strings.TrimSpace(prefix); p != "" {
		return p
	}
	norm := strings.ToLower(strings.TrimSpace(providerID))
	if canonical, ok := ProviderAliasMap[norm]; ok {
		return canonical
	}
	// A generated compatible node id has no static alias; fall back to the raw
	// id only as a last resort, exactly like upstream's `staticAlias`.
	return strings.TrimSpace(providerID)
}

// NormalizeModelAlias maps a stored custom-model provider alias onto the short
// alias used for export.
//
// Upstream accepts a custom model whose `providerAlias` is any of: the static
// alias, the output alias (prefix), or the raw provider id — see the guard in
// `allowedModels.js`:
//
//	if (alias !== staticAlias && alias !== outputAlias && alias !== providerId) return acc;
//
// A restore of a VansRouter backup therefore legitimately stores generated ids
// such as "anthropic-compatible-0b2ee40e-…" in `customModels.providerAlias`.
// Exporting that verbatim is what produced 1500 uuid-prefixed model names.
// This helper returns the alias to advertise: the node's prefix when the alias
// names a known node, otherwise the alias unchanged.
func NormalizeModelAlias(alias string, nodePrefixByID map[string]string) string {
	key := strings.TrimSpace(alias)
	if key == "" {
		return key
	}
	if prefix, ok := nodePrefixByID[key]; ok && strings.TrimSpace(prefix) != "" {
		return strings.TrimSpace(prefix)
	}
	return key
}

// IsGeneratedNodeID reports whether a provider key was auto-generated for a
// compatible endpoint rather than authored by the user. Upstream builds these
// in `src/app/api/provider-nodes/route.js`:
//
//	id: `${OPENAI_COMPATIBLE_PREFIX}${apiType}-${randomUUID()}`
//
// The suffix is always a UUID, which is what makes the key both unreadable and
// impossible to collide with a static alias. Callers use this to decide that a
// raw id must not be shown to a user (the node's `name`/`prefix` is the
// user-facing identity).
func IsGeneratedNodeID(providerID string) bool {
	key := strings.TrimSpace(providerID)
	switch {
	case strings.HasPrefix(key, "openai-compatible-"):
	case strings.HasPrefix(key, "anthropic-compatible-"):
	case strings.HasPrefix(key, "custom-embedding-"):
	default:
		return false
	}
	// Require the trailing UUID so a hand-written id that merely shares the
	// prefix (e.g. "openai-compatible-custom") is not misclassified. The uuid
	// itself contains dashes, so compare only its last 36 characters rather
	// than splitting on the final dash.
	if len(key) < 37 || key[len(key)-37] != '-' {
		return false
	}
	uuid := key[len(key)-36:]
	for i, c := range uuid {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
			if !isHex {
				return false
			}
		}
	}
	return true
}

// IsClaudeCodeNodeID reports whether a generated node id identifies a Claude Code
// compatible endpoint. Older nodes encode the variant in the id itself; newer
// ones record it in the data blob as compatMode, so a caller that has the blob
// should prefer that and use this only as a fallback.
func IsClaudeCodeNodeID(providerID string) bool {
	key := strings.TrimSpace(providerID)
	return strings.HasPrefix(key, "claude-code-compatible-")
}
