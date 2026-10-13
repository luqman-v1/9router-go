package thinking

import (
	"9router/proxy/internal/providers"
	json "encoding/json/v2"
	"strings"
)

// formatToNative maps a target wire format to its native thinking format
// when the capability declares none. Port of FORMAT_TO_NATIVE.
var formatToNative = map[string]string{
	"openai":           "openai",
	"openai-responses": "openai",
	"openai-response":  "openai",
	"codex":            "openai",
	"claude":           "claude-budget",
	"gemini":           "gemini-budget",
	"gemini-cli":       "gemini-budget",
	"vertex":           "gemini-budget",
	"antigravity":      "gemini-budget",
	"kiro":             "kiro",
	"commandcode":      "commandcode",
}

// nativeOnlyFormats are capability formats that never ride an OpenAI wire:
// an openai/openai-responses target falls back to its native writer instead.
// Port of NATIVE_ONLY_FORMATS.
var nativeOnlyFormats = map[string]bool{
	"gemini-level":    true,
	"gemini-budget":   true,
	"claude-budget":   true,
	"claude-adaptive": true,
	"kiro":            true,
}

// Apply normalises thinking for the resolved target format — port of
// applyThinking. Mutates and returns body.
//
// Order: suffix strip + model rewrite; capability lookup; strip when the
// model cannot reason; extract intent (override wins); resolve format;
// return untouched when no writer exists; strip all dialects; write one.
func Apply(body map[string]any, target, provider, model string, override *Intent) map[string]any {
	if len(body) == 0 {
		return body
	}
	clean, suffix := ParseSuffix(model)
	// Gap B: the suffix is a request override, not the model id. The
	// dispatch callers write the suffixed id into body["model"]; strip it
	// whenever present. (`model` may be the already-stripped wireModel, so
	// gate on the body value, not on clean != model.)
	if m, _ := body["model"].(string); m != "" {
		if stripped := StripSuffix(m); stripped != m {
			body["model"] = stripped
		}
	}
	cfg := override
	if cfg == nil {
		cfg = suffix
	}
	caps := providers.GetCapabilitiesForModel(provider, clean)
	// Model cannot reason → strip any stray thinking fields.
	if !caps.Reasoning {
		Strip(body)
		return body
	}
	if cfg == nil {
		cfg = Extract(body)
	}
	// No expressed intent on a reasoning-capable model: byte-identical.
	if cfg == nil {
		return body
	}
	fmt := resolveFormat(target, provider, clean, caps)
	if !hasWriter(fmt) {
		// Unreachable format: neither write nor strip.
		return body
	}
	var display string
	if t, ok := body["thinking"].(map[string]any); ok {
		display, _ = t["display"].(string)
	}
	if display == "" && override != nil {
		display = override.Display
	}
	Strip(body)
	applyFormat(fmt, body, cfg, caps, providers.GetThinkingLevels(provider, clean), display)
	return body
}

// ApplyJSON is Apply over raw bytes. Fast path returns the input unchanged
// when the body's own model carries no suffix and no thinking key is present;
// decode/marshal errors also return the input unchanged. Idempotent
// (strip-then-write), so combo/rotation re-entry is safe.
func ApplyJSON(body []byte, target, provider, model string, override *Intent) []byte {
	if override == nil && !strings.Contains(model, "(") &&
		!BodyMentionsThinking(body) && !bodyModelHasSuffix(body) {
		return body
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		return body
	}
	Apply(m, target, provider, model, override)
	out, err := json.Marshal(m, json.Deterministic(true))
	if err != nil {
		return body
	}
	return out
}

// bodyModelHasSuffix reports whether the body's own "model" value carries a
// "(…)" suffix. It scans the raw bytes for the model key instead of decoding:
// the first ")" before the next quote/comma/brace after "model" indicates one.
func bodyModelHasSuffix(body []byte) bool {
	s := string(body)
	for _, key := range []string{`"model"`, `"modelId"`, `"model_id"`} {
		idx := strings.Index(s, key)
		for idx != -1 {
			rest := s[idx+len(key):]
			rest = strings.TrimLeft(rest, " \t\n\r:")
			if len(rest) > 0 && rest[0] == '"' {
				end := strings.IndexByte(rest[1:], '"')
				if end != -1 && strings.Contains(rest[1:1+end], "(") && strings.HasSuffix(strings.TrimSpace(rest[1:1+end]), ")") {
					return true
				}
				break
			}
			next := strings.Index(s[idx+len(key):], key)
			if next == -1 {
				break
			}
			idx += len(key) + next
		}
	}
	return false
}

// resolveFormat ports resolveFormat verbatim.
func resolveFormat(target, provider, model string, caps providers.Capabilities) string {
	if target == "commandcode" {
		return "commandcode"
	}
	if provider != "" {
		if pf := providers.ThinkingFormatFor(provider); pf != "" {
			return pf
		}
	}
	isOpenAIWire := target == "openai" || target == "openai-responses"
	if caps.ThinkingFormat != "" && !(isOpenAIWire && nativeOnlyFormats[caps.ThinkingFormat]) {
		// Muse (Meta) strict Responses API rejects top-level reasoning_effort
		// and requires nested reasoning: { effort, summary }.
		if provider == "muse" && target == "openai-responses" {
			return "openai-responses"
		}
		return caps.ThinkingFormat
	}
	if f, ok := formatToNative[target]; ok {
		return f
	}
	return "openai"
}

// hasWriter reports whether a writer exists for fmt. Formats without one
// leave the body untouched (no strip).
func hasWriter(fmt string) bool {
	switch fmt {
	case "openai", "openai-responses", "claude-adaptive", "claude-budget",
		"gemini-level", "gemini-budget", "zai", "qwen", "kimi", "deepseek",
		"minimax", "hunyuan", "step", "tokenrouter", "kiro", "commandcode":
		return true
	default:
		return false
	}
}

// canDisable mirrors providers' tri-state default (nil = can disable).
func canDisable(caps providers.Capabilities) bool {
	return caps.ThinkingCanDisable == nil || *caps.ThinkingCanDisable
}
