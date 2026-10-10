package chat

import (
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
func (h *ChatHandler) prepareInboundThinking(provider, model string, body []byte) ([]byte, string, *thinking.Intent) {
	wireModel, suffixIntent := thinking.ParseSuffix(model)
	needsWire := wireModel != model || suffixIntent != nil
	if !needsWire && len(providers.ModelStrip(provider, model)) == 0 &&
		h.providerThinkingMode(provider) == "" && !thinking.BodyMentionsThinking(body) {
		return body, wireModel, nil
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		return body, wireModel, suffixIntent
	}
	translator.StripContentTypes(m, providers.ModelStrip(provider, wireModel))
	h.injectProviderThinking(provider, m)
	thinking.NormalizeThinkingConfig(m)
	intent := suffixIntent
	if intent == nil {
		intent = thinking.CaptureThinking(m)
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body, wireModel, intent
	}
	return out, wireModel, intent
}

// injectProviderThinking applies settings.providerThinking[provider].mode,
// mirroring upstream chatCore.js:117-131. Only when the client hasn't set
// the field; "auto" (and "") means no override.
func (h *ChatHandler) injectProviderThinking(provider string, m map[string]any) {
	mode := h.providerThinkingMode(provider)
	if mode == "" || mode == "auto" {
		return
	}
	switch mode {
	case "on":
		if _, has := m["thinking"]; !has {
			m["thinking"] = map[string]any{"type": "enabled", "budget_tokens": 10000}
		}
	case "off":
		if _, has := m["thinking"]; !has {
			m["thinking"] = map[string]any{"type": "disabled"}
		}
	default:
		if _, has := m["reasoning_effort"]; !has {
			m["reasoning_effort"] = mode
		}
	}
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
