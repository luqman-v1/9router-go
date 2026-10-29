package executor

import (
	json "encoding/json/v2"
)

// stripCodexUnsupportedTokenParams removes the parameters the ChatGPT Codex
// OAuth backend rejects with 400 "Unsupported parameter: ...": the
// generation-limit fields (max_output_tokens and friends) and the sampling
// knobs (temperature, top_p). Editors such as Zed send temperature on every
// chat-completions turn, and the reasoning models behind Codex answer it with
// 400 "Unsupported parameter: temperature" — a request-scoped error that does
// not lock the connection, so each turn burned a failed upstream call before
// the combo moved on.
//
// The strip is deliberately NOT done in buildResponsesBody. That function is
// shared with the other Responses-API providers (grok-cli and friends), whose
// backends do accept the field, and the command-code body builder reads
// max_output_tokens off the incoming map to pick its own default. So the
// removal lives in the Codex forward path only, where the rejecting backend is
// a known destination.
//
// Port of open-sse/executors/codex.js transformRequest:
//
//	delete body.max_tokens;
//	delete body.max_completion_tokens;
//	delete body.max_output_tokens;
//
// plus temperature/top_p, which the Codex backend rejects the same way.
//
// A body that does not parse is returned untouched: rewriting a payload we
// could not read risks turning an upstream-parseable error into a silently
// different request.
func stripCodexUnsupportedTokenParams(body []byte) []byte {
	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		return body
	}

	var changed bool
	for _, key := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens", "temperature", "top_p"} {
		if _, ok := envelope[key]; ok {
			delete(envelope, key)
			changed = true
		}
	}
	changed = normalizeCodexServiceTier(envelope) || changed
	if !changed {
		return body
	}

	out, err := marshalStable(envelope)
	if err != nil {
		return body
	}
	return out
}

// normalizeCodexServiceTier applies the Codex backend's service_tier rule and
// drops the client-only "speed" hint. Port of open-sse/executors/codex.js
// transformRequest:
//
//	if (body.service_tier === "fast") body.service_tier = "priority";
//	if (body.service_tier && body.service_tier !== "priority") delete body.service_tier;
//
// "speed" is not deleted by that block. It disappears with every other field
// outside RESPONSES_API_ALLOWLIST when the final filter runs, which this port
// does not carry — so it is dropped explicitly here for the same effect.
//
// The rule lives here rather than in buildResponsesBody because that builder is
// shared with every other Responses provider (grok-cli and friends); those
// backends are not behind this allowlist and accept the field as sent.
//
// The guard is truthiness, not "not priority": upstream leaves null, false, 0
// and "" in the body, so this does too. Reading it as strip-everything-else
// would be stricter than the reference and would change what a client sending
// an explicit null gets.
//
// Absence stays absence. Nothing here invents a tier: neither an account-level
// codexFastMode preference nor a "speed" field promotes a request the caller
// did not ask to prioritise.
func normalizeCodexServiceTier(envelope map[string]any) bool {
	var changed bool
	if raw, ok := envelope["service_tier"]; ok {
		s, isString := raw.(string)
		switch {
		case isString && s == "fast":
			envelope["service_tier"] = "priority"
			changed = true
		case isString && s == "priority":
			// Already the one value the backend accepts; leave it.
		case truthy(raw):
			delete(envelope, "service_tier")
			changed = true
		}
	}
	if _, ok := envelope["speed"]; ok {
		delete(envelope, "speed")
		changed = true
	}
	return changed
}

// truthy mirrors JavaScript's truthiness for the values JSON can hold, which
// is what upstream's guard evaluates. Numbers matter: 0 and 0.0 are falsy on
// the wire's terms, and encoding/json decodes both to float64.
func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case float64:
		return t != 0
	case []any:
		return true // an array is an object in JS; only null/undefined/0/""/false are falsy
	case map[string]any:
		return true
	default:
		return true
	}
}
