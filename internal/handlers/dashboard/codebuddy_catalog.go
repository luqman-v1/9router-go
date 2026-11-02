package dashboard

import (
	"strings"

	"9router/proxy/internal/providers"
)

// CodeBuddy-family model discovery.
//
// CodeBuddy (copilot.tencent.com / www.codebuddy.ai) exposes no model listing
// route at all: probed live against a
// valid account, every candidate path (/v2/models, /v1/models, /v2/model,
// /v2/models/list, …) answers 404. The upstream "Import from /models" button can
// therefore never work for them, and the generic branch reported "provider
// codebuddy does not support models listing" — a dead button in the dashboard.
//
// The catalogue is served from the Go registry (providers.ProviderModels), the
// same list the chat router resolves against. Import therefore offers exactly
// what a request can reach, with no second list to drift from the router's.
//
// WorkBuddy is deliberately NOT handled here. It is a separate service that this
// build does not register at all — no KnownProviders entry, no ProviderModels
// entry, no executor — so it cannot be connected to and a branch for it would
// only ever return an empty list. It joins the branch when it is registered.
// codebuddyNonChatTokens are the non-chat surfaces a chat-completions client can
// never call. Matched on segment boundaries only, so a legitimate id that merely
// contains the letters is never rejected — the same rule
// providers.IsDiscoverableAntigravityModel uses.
var codebuddyNonChatTokens = []string{
	"image", "imagen", "audio", "tts", "embedding", "embed", "video", "veo", "rerank",
}

// isCodebuddyFamily reports whether a provider id (canonical or alias) addresses
// any CodeBuddy variant or WorkBuddy.
func isCodebuddyFamily(providerID string) bool {
	switch strings.ToLower(strings.TrimSpace(providerID)) {
	case "codebuddy-cn", "codebuddy-intl", "cbcn", "cbai":
		return true
	default:
		return false
	}
}

// isDiscoverableCodebuddyModel reports whether a catalogue id should be offered
// in Import. It rejects the non-chat surfaces above, for the same reason the
// Antigravity filter does: a model a chat client can never call is a row that
// fails on first use.
func isDiscoverableCodebuddyModel(modelID string) bool {
	id := strings.TrimSpace(modelID)
	if id == "" {
		return false
	}
	lower := strings.ToLower(id)
	for _, token := range codebuddyNonChatTokens {
		if lower == token ||
			strings.HasPrefix(lower, token+"-") ||
			strings.HasSuffix(lower, "-"+token) ||
			strings.Contains(lower, "-"+token+"-") {
			return false
		}
	}
	return true
}

// codebuddyCatalogModels resolves the importable catalogue for a CodeBuddy-family
// connection, filtered through the discoverability rule.
func codebuddyCatalogModels(providerID string) []string {
	canonical := providers.ResolveAlias(providerID)
	if canonical == "" {
		canonical = providerID
	}
	ids := providers.GetProviderModels(canonical)
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !isDiscoverableCodebuddyModel(id) {
			continue
		}
		out = append(out, id)
	}
	return out
}
