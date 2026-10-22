package chat

import (
	"context"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/thinking"
	"9router/proxy/internal/translator"
	json "encoding/json/v2"
	"strings"
)

// prepareInboundThinking is the inbound half of the unified pipeline: it
// strips content types the model rejects, injects the operator's
// providerThinking override, drops a thinking block the conversation shape
// invalidates, and captures the client's reasoning intent before any
// converter rewrites the body.
//
// It returns the (possibly rewritten) body, the suffix-free wire model, and
// the captured intent. The fast path leaves the body byte-identical.
//
// claudeClient says the client speaks the Claude Messages wire, so the
// operator override is written in the Claude dialect: that wire has no
// reasoning_effort field, and Anthropic answers an unknown top-level field
// with a 400.
func (h *ChatHandler) prepareInboundThinking(ctx context.Context, provider, model string, body []byte, claudeClient bool) ([]byte, string, *thinking.Intent) {
	wireModel, suffixIntent := thinking.ParseSuffix(model)
	if carried := thinking.SuffixIntentFromContext(ctx); carried != nil {
		suffixIntent = carried
	} else if carried := thinking.ModelSuffixIntent(body); carried != nil {
		suffixIntent = carried
	}
	needsWire := wireModel != model
	// The operator override is read once and reused below: the fast path
	// needs it to know whether the body has to be decoded at all.
	mode := h.providerThinkingMode(provider)
	if !needsWire && len(providers.ModelStrip(provider, model)) == 0 &&
		mode == "" && suffixIntent == nil && !thinking.BodyMentionsThinking(body) {
		return body, wireModel, nil
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		return body, wireModel, suffixIntent
	}
	changed := translator.StripContentTypes(m, providers.ModelStrip(provider, wireModel))
	if h.injectProviderThinkingMode(mode, m, claudeClient, provider, wireModel) {
		changed = true
	}
	if thinking.NormalizeThinkingConfig(m) {
		changed = true
	}
	intent := suffixIntent
	if intent == nil {
		intent = thinking.CaptureThinking(m)
	}
	// A body the inbound pass did not change keeps the client's bytes: no
	// re-serialisation, and no float64 round trip for the integers in it.
	if !changed {
		return body, wireModel, intent
	}
	out, err := json.Marshal(m, json.Deterministic(true))
	if err != nil {
		return body, wireModel, intent
	}
	return out, wireModel, intent
}

// injectProviderThinking applies settings.providerThinking[provider].mode,
// mirroring upstream chatCore.js:117-131. Only when the client hasn't set
// the field; "auto" (and "") means no override.
func (h *ChatHandler) injectProviderThinking(provider string, m map[string]any) bool {
	return h.injectProviderThinkingMode(h.providerThinkingMode(provider), m, false, provider, "")
}

// injectProviderThinkingMode applies an operator override that was already
// read, so the per-request path reads the settings once instead of twice.
// The bool reports whether a field was written.
//
// The dialect follows the client. A Claude client sends the Claude wire, and
// that wire has no reasoning_effort field: Anthropic answers an unknown
// top-level field with a 400. So the override is written as the Claude
// dialect there — a disabled thinking block, an adaptive block with an
// effort, or an enabled block with a budget.
//
// The guard is per dialect: the operator fills a gap the client left in that
// dialect, and where two dialects collide the precedence is Extract's, not
// the override's.
func (h *ChatHandler) injectProviderThinkingMode(mode string, m map[string]any, claudeClient bool, provider, model string) bool {
	mode, valid := thinking.NormalizeProviderThinkingMode(mode)
	if !valid || mode == "auto" {
		return false
	}
	switch mode {
	case "on":
		if _, has := m["thinking"]; !has {
			m["thinking"] = map[string]any{"type": "enabled", "budget_tokens": 10000}
			return true
		}
	case "off":
		if _, has := m["thinking"]; !has {
			m["thinking"] = map[string]any{"type": "disabled"}
			return true
		}
	default:
		if claudeClient {
			return thinking.ApplyClaudeThinkingOverride(m, mode, providers.GetCapabilitiesForModel(provider, model))
		}
		if _, has := m["reasoning_effort"]; !has {
			m["reasoning_effort"] = mode
			return true
		}
	}
	return false
}

// providerThinkingMode reads settings.providerThinking[provider].mode.
// A nil repo (tests) means no override.
func (h *ChatHandler) providerThinkingMode(provider string) string {
	if h == nil || h.Repo == nil {
		return ""
	}
	settings, err := h.Repo.GetSettings()
	if err != nil || settings == nil {
		return ""
	}
	pt, ok := settings.ProviderThinking[provider]
	if !ok {
		return ""
	}
	return strings.TrimSpace(pt.Mode)
}
