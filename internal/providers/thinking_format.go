package providers

import (
	"strings"
)

// ThinkingFormatFor reports the wire dialect a provider expects for
// reasoning fields ("" = the model's capability decides). Mirrors the
// upstream registry's transport.thinkingFormat; consumed by
// internal/thinking.resolveFormat.
func ThinkingFormatFor(provider string) string {
	if cfg, ok := KnownProviders[provider]; ok {
		return cfg.ThinkingFormat
	}
	return ""
}

// modelStripLists ports the `strip:[…]` declarations in PROVIDER_MODELS
// (upstream open-sse/providers/registry/kiro.js:73-74) — the only two that
// exist. Models that cannot accept a content type get those parts removed
// before translation.
var modelStripLists = map[string]map[string][]string{
	"kiro": {
		"deepseek-3.2":     {"image", "audio"},
		"qwen3-coder-next": {"image", "audio"},
	},
}

// ModelStrip returns the content types to strip from messages for a model,
// or nil when the model accepts everything. A vendor prefix ("zai-org/…")
// is cut before lookup. The chat handler passes the alias-resolved provider,
// so "kr" needs no separate key.
func ModelStrip(provider, model string) []string {
	lists, ok := modelStripLists[provider]
	if !ok {
		return nil
	}
	bare := model
	if _, after, cut := strings.CutLast(model, "/"); cut {
		bare = after
	}
	return lists[bare]
}
