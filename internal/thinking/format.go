package thinking

import (
	"9router/proxy/internal/providers"
	"bytes"
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
//
// The bool reports whether the body changed. A caller that owns the bytes can
// then return the input unchanged instead of re-serialising it, which keeps
// the client's own bytes — and any integer above 2^53 in them — intact.
func Apply(body map[string]any, target, provider, model string, override *Intent) (map[string]any, bool) {
	if len(body) == 0 {
		return body, false
	}
	clean, suffix := ParseSuffix(model)
	changed := false
	// Gap B: the suffix is a request override, not the model id. The
	// dispatch callers write the suffixed id into body["model"]; strip it
	// whenever present. (`model` may be the already-stripped wireModel, so
	// gate on the body value, not on clean != model.)
	if m, _ := body["model"].(string); m != "" {
		if stripped := StripSuffix(m); stripped != m {
			body["model"] = stripped
			changed = true
		}
	}
	cfg := override
	if cfg == nil {
		cfg = suffix
	}
	caps := providers.GetCapabilitiesForModel(provider, clean)
	// Model cannot reason → strip any stray thinking fields.
	if !caps.Reasoning {
		return body, changed || Strip(body)
	}
	if cfg == nil {
		cfg = Extract(body)
	}
	// No expressed intent on a reasoning-capable model: byte-identical.
	if cfg == nil {
		return body, changed
	}
	wireFormat := resolveFormat(target, provider, clean, caps)
	if !hasWriter(wireFormat) {
		// Unreachable format: neither write nor strip.
		return body, changed
	}
	var display string
	if t, ok := body["thinking"].(map[string]any); ok {
		display, _ = t["display"].(string)
	}
	if display == "" && override != nil {
		display = override.Display
	}
	stripped := Strip(body)
	applyFormat(wireFormat, body, cfg, caps, providers.GetThinkingLevels(provider, clean), display)
	return body, changed || stripped || dialectKeyPresent(body)
}

// shouldSkipNormalization reports whether the fast path applies: no override,
// no model suffix, no thinking fields present. This avoids the decode/marshal
// cost on the common case where the client sends a plain request.
func shouldSkipNormalization(body []byte, model string, override *Intent) bool {
	if override != nil {
		return false
	}
	if strings.Contains(model, "(") {
		return false
	}
	if BodyMentionsThinking(body) {
		return false
	}
	if bodyModelHasSuffix(body) {
		return false
	}
	return true
}

// ApplyJSON is Apply over raw bytes. Fast path returns the input unchanged
// when the body's own model carries no suffix and no thinking key is present;
// a pass that changes nothing also returns the input unchanged, so a body the
// client sent is the body the upstream sees. Decode/marshal errors also
// return the input unchanged. Idempotent (strip-then-write), so combo and
// rotation re-entry is safe.
//
// The body is decoded into map[string]any, and json/v2 decodes an interface
// number as float64. A body the pass rewrites therefore loses an integer
// above 2^53 on the way out — the same limit upstream's JSON.parse has. A
// body the pass does not rewrite is never decoded, so it keeps its bytes.
func ApplyJSON(body []byte, target, provider, model string, override *Intent) []byte {
	if shouldSkipNormalization(body, model, override) {
		return body
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		return body
	}
	if _, changed := Apply(m, target, provider, model, override); !changed {
		return body
	}
	out, err := json.Marshal(m, json.Deterministic(true))
	if err != nil {
		return body
	}
	return out
}

// bodyModelHasSuffix reports whether the body's own "model" value carries a
// "(…)" suffix. It scans the raw bytes for the model key instead of decoding:
// the first ")" before the next quote/comma/brace after "model" indicates one.
// Scans bytes directly to avoid string allocation in the hot path.
//
// The scan is linear. A request body is read without a size cap, so a body
// that repeats the model key must not cost a quadratic walk.
func bodyModelHasSuffix(body []byte) bool {
	for _, key := range [][]byte{[]byte(`"model"`), []byte(`"modelId"`), []byte(`"model_id"`)} {
		for off := 0; off < len(body); {
			rel := bytes.Index(body[off:], key)
			if rel == -1 {
				break
			}
			i := off + rel + len(key)
			off = i
			i = skipJSONSpace(body, i)
			if i >= len(body) || body[i] != '"' {
				off = i
				continue
			}
			end := bytes.IndexByte(body[i+1:], '"')
			if end == -1 {
				break
			}
			value := body[i+1 : i+1+end]
			off = i + 1 + end + 1
			trimmed := bytes.TrimRight(value, " \t\n\r")
			if bytes.IndexByte(value, '(') >= 0 && len(trimmed) > 0 && trimmed[len(trimmed)-1] == ')' {
				return true
			}
		}
	}
	return false
}

// skipJSONSpace returns the index of the first byte at or after i that is
// neither JSON whitespace nor the colon that separates a key from its value.
func skipJSONSpace(body []byte, i int) int {
	for i < len(body) {
		switch body[i] {
		case ' ', '\t', '\n', '\r', ':':
			i++
		default:
			return i
		}
	}
	return i
}

// resolveFormat picks the dialect: the wire the request goes out on, then
// the provider registry, then the model's capability.
//
// The wire wins because the registry describes a provider in general while
// the target describes this request: a connection whose endpoint speaks the
// Responses API has to carry the field that API accepts, whatever the
// provider's default wire is. Upstream pins the same rule for one provider
// by name (`provider === "muse" && targetFormat === "openai-responses"`,
// thinkingUnified.js:137-145); this port applies it to every provider on that
// lane, so the nine aggregators the registry records as "openai" also read
// reasoning:{effort,summary} when the request goes to a /responses endpoint.
func resolveFormat(target, provider, model string, caps providers.Capabilities) string {
	if target == "commandcode" {
		return "commandcode"
	}
	if target == "openai-responses" && !nativeOnlyFormats[caps.ThinkingFormat] {
		return "openai-responses"
	}
	if provider != "" {
		if pf := providers.ThinkingFormatFor(provider); pf != "" {
			return pf
		}
	}
	isOpenAIWire := target == "openai" || target == "openai-responses"
	if caps.ThinkingFormat != "" && (!isOpenAIWire || !nativeOnlyFormats[caps.ThinkingFormat]) {
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
