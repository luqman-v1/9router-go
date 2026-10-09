package executor

import (
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"strings"

	"9router/proxy/internal/providers"
)

// ForwardZai forwards to Z.ai (z.ai / bigmodel.cn) after rewriting the client's
// thinking request into the shape Z.ai accepts.
//
// Z.ai is OpenAI-compatible for the transport but not for the thinking control:
// `reasoning_effort: "none"` is ignored, and sending `thinking.type: disabled`
// is answered with 400 code 1210 "Invalid API parameter". Turning thinking off
// therefore requires `enable_thinking: false` — and on GLM-5.3 it is not
// possible at all, because z.ai's docs state the line "no longer support
// disabling thinking". A request that asks for no reasoning must then be
// clamped to the lowest effort rather than translated to a disabling one.
//
// Port of the `case "zai"` arm of applyFormat in upstream
// open-sse/translator/concerns/thinkingUnified.js:321.
func ForwardZai(w http.ResponseWriter, req *Request) error {
	body, err := applyZaiThinking(req.Body, req.ModelName)
	if err != nil {
		return fmt.Errorf("applyZaiThinking: %w", err)
	}
	forwarded := *req
	forwarded.Body = body
	return ForwardOpenAI(w, &forwarded)
}

// applyZaiThinking rewrites body into Z.ai's thinking dialect and returns the
// result. A body it cannot parse is returned unchanged: the upstream is the
// authority on rejecting malformed requests, and guessing here would turn a
// parse error into a silently mis-routed request.
//
// The rewrite is a no-op unless the body actually asks for something, so a
// request that never mentioned thinking is byte-identical to what it sent.
func applyZaiThinking(body []byte, model string) ([]byte, error) {
	raw, err := parseZaiBody(body)
	if err != nil {
		return body, nil //nolint:nilerr // see doc: an unparsable body is the upstream's problem, not ours
	}

	intent, asked := zaiThinkingIntent(raw)
	if !asked {
		return body, nil
	}

	// Upstream strips every thinking field before re-emitting one, so a
	// Responses-shaped `reasoning` object cannot survive next to the
	// reasoning_effort z.ai is about to receive — it rejects a body carrying
	// both. Mirrors stripAll in thinkingUnified.js.
	stripThinkingFields(raw)

	// Capabilities resolve provider-agnostically here: no provider-qualified
	// override exists for the glm / glm-cn lanes, so the pattern table is the
	// whole answer for them.
	caps := providers.GetCapabilitiesForModel("", model)
	if !caps.Reasoning {
		return json.Marshal(raw)
	}

	level, none := zaiEffortLevel(intent, caps)
	if none {
		// z.ai ignores thinking.type:disabled and answers 400 code 1210 to
		// it; enable_thinking is the only way to switch thinking off.
		//
		// none is true only for a model that CAN disable — zaiEffortLevel
		// clamps the request to the lowest effort instead for one that
		// cannot — so this branch is never the #4656 400.
		raw["enable_thinking"] = false
		return json.Marshal(raw)
	}

	// Reached both when the client wants reasoning, and when it asked for
	// none on a model that cannot disable it: thinking stays on at the
	// lowest effort the model publishes, the closest legal request.
	raw["thinking"] = map[string]any{"type": "enabled"}
	if caps.ThinkingEffortSupported && level != "" {
		// reasoning_effort is only read from GLM-5.2 onward. Older models
		// do not recognise the field, and it must not be sent to them.
		raw["reasoning_effort"] = level
	}
	return json.Marshal(raw)
}

// stripThinkingFields removes every shape the client's thinking request may
// have arrived in, so exactly one dialect goes back out.
func stripThinkingFields(raw map[string]any) {
	for _, key := range []string{
		"thinking", "reasoning", "reasoning_effort",
		"thinkingConfig", "enable_thinking", "thinking_budget", "output_config",
	} {
		delete(raw, key)
	}
	for _, key := range []string{"generationConfig", "request"} {
		if nested, ok := raw[key].(map[string]any); ok {
			delete(nested, "thinkingConfig")
		}
	}
	if params, ok := raw["params"].(map[string]any); ok {
		delete(params, "reasoning_effort")
		delete(params, "thinking")
	}
}

// zaiThinkingIntent reports the effort the client asked for, in the order
// upstream extractThinking reads it: an explicit output_config.effort wins
// over the OpenAI-shaped reasoning_effort / reasoning.effort, which wins over
// the Claude-shaped thinking object. The order matters because z.ai bodies
// carry both a thinking object and a reasoning.effort, and the explicit
// Claude-shaped one is the more deliberate of the two.
//
// asked=false means the body carries no thinking request at all, which is the
// common case and must leave it untouched.
func zaiThinkingIntent(raw map[string]any) (level string, asked bool) {
	if oc, ok := raw["output_config"].(map[string]any); ok {
		if v, ok := oc["effort"].(string); ok && v != "" {
			return strings.ToLower(v), true
		}
	}
	if v, ok := raw["reasoning_effort"].(string); ok && v != "" {
		return strings.ToLower(v), true
	}
	if r, ok := raw["reasoning"].(map[string]any); ok {
		if v, ok := r["effort"].(string); ok && v != "" {
			return strings.ToLower(v), true
		}
	}
	if t, ok := raw["thinking"].(map[string]any); ok {
		if typ, _ := t["type"].(string); typ != "" {
			return "none", strings.EqualFold(typ, "disabled")
		}
	}
	return "", false
}

// zaiEffortLevel maps the client's intent onto the three values Z.ai accepts.
// z.ai accepts only low|high|max: GLM-5.3 errors on anything else, and GLM-5.2
// remaps low/medium to high and xhigh to max server-side anyway, so one
// three-way mapping serves both.
func zaiEffortLevel(intent string, caps providers.Capabilities) (level string, none bool) {
	none = intent == "none" || intent == "off"
	if none && !zaiCanDisable(caps) {
		// Clamp rather than disable. Upstream does the same clamp for every
		// format; for z.ai it is load-bearing because the alternative is the
		// 1210 that #4656 fixed.
		return "low", false
	}
	if none {
		return "", true
	}
	switch intent {
	case "low", "minimal":
		return "low", false
	case "high", "medium":
		return "high", false
	default:
		return "max", false
	}
}

// zaiCanDisable reports whether thinking may be switched off. An unset flag
// means "not specified", which is the default: it can.
func zaiCanDisable(caps providers.Capabilities) bool {
	return caps.ThinkingCanDisable == nil || *caps.ThinkingCanDisable
}

// parseZaiBody unmarshals body into a mutable map. jsontext is used so an
// unknown field type never fails the parse of the fields this cares about.
func parseZaiBody(body []byte) (map[string]any, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse body: %w", err)
	}
	if raw == nil {
		return nil, fmt.Errorf("body is not a JSON object")
	}
	return raw, nil
}
