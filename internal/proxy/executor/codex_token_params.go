package executor

import (
	json "encoding/json/v2"
	"regexp"
	"strings"
)

var codexServerIDPattern = regexp.MustCompile(`^(rs|fc|resp|msg)_`)

// codexResponsesAllowlist defines the parameters accepted by the ChatGPT Codex
// Responses API. Port of RESPONSES_API_ALLOWLIST from open-sse/executors/codex.js.
var codexResponsesAllowlist = map[string]bool{
	"model":               true,
	"input":               true,
	"instructions":        true,
	"tools":               true,
	"tool_choice":         true,
	"stream":              true,
	"store":               true,
	"reasoning":           true,
	"service_tier":        true,
	"include":             true,
	"prompt_cache_key":    true,
	"client_metadata":     true,
	"text":                true,
	"parallel_tool_calls": true,
}

// stripCodexStoredItemReferences strips server-generated item references and IDs
// from input. When store=false, the backend cannot look up stored server item IDs
// (rs_, fc_, resp_, msg_) and fails or invalidates session state.
// Port of stripStoredItemReferences from open-sse/executors/codex.js.
func stripCodexStoredItemReferences(envelope map[string]any, preserveLitePrefix bool) bool {
	rawInput, ok := envelope["input"]
	if !ok {
		return false
	}
	slice, ok := responseInputItems(rawInput)
	if !ok || len(slice) == 0 {
		return false
	}
	filtered := make([]any, 0, len(slice))
	changed := false
	for _, item := range slice {
		if s, isStr := item.(string); isStr {
			if codexServerIDPattern.MatchString(s) {
				changed = true
				continue
			}
		}
		if m, isMap := item.(map[string]any); isMap {
			if m["type"] == "item_reference" {
				changed = true
				continue
			}
			if id, ok := m["id"].(string); ok && codexServerIDPattern.MatchString(id) {
				isDevMsg := preserveLitePrefix && m["role"] == "developer" && strings.HasPrefix(id, "msg_")
				if !isDevMsg {
					delete(m, "id")
					changed = true
				}
			}
		}
		filtered = append(filtered, item)
	}
	if changed {
		envelope["input"] = filtered
	}
	return changed
}

// injectCodexReasoningInclude ensures reasoning encrypted content is included
// for multi-turn reasoning continuity when store=false.
// Port of open-sse/executors/codex.js:
//
//	if (body.reasoning && body.reasoning.effort && body.reasoning.effort !== 'none') {
//	  body.include = ["reasoning.encrypted_content"];
//	}
//
// We deliberately guard on envelope["include"] != nil so a client-supplied
// include list (e.g. logprobs) is preserved rather than overwritten unconditionally.
func injectCodexReasoningInclude(envelope map[string]any) bool {
	if envelope["include"] != nil {
		return false
	}
	rMap, ok := envelope["reasoning"].(map[string]any)
	if !ok {
		return false
	}
	eff, _ := rMap["effort"].(string)
	if eff != "" && eff != "none" {
		envelope["include"] = []any{"reasoning.encrypted_content"}
		return true
	}
	return false
}

// stripCodexUnsupportedTokenParams removes the parameters the ChatGPT Codex
// OAuth backend rejects with 400 "Unsupported parameter: ...", strips stored
// item references, injects reasoning include, and enforces the Responses API allowlist.
func stripCodexUnsupportedTokenParams(body []byte, responsesLite ...bool) []byte {
	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		return body
	}

	preserveLitePrefix := len(responsesLite) > 0 && responsesLite[0]
	var changed bool
	for _, key := range []string{
		"temperature", "top_p", "frequency_penalty", "presence_penalty",
		"logprobs", "top_logprobs", "n", "seed", "max_tokens",
		"max_completion_tokens", "max_output_tokens", "user",
		"prompt_cache_retention", "metadata", "stream_options",
		"safety_identifier", "previous_response_id",
	} {
		if _, ok := envelope[key]; ok {
			delete(envelope, key)
			changed = true
		}
	}
	changed = normalizeCodexServiceTier(envelope) || changed
	changed = stripCodexStoredItemReferences(envelope, preserveLitePrefix) || changed
	changed = injectCodexReasoningInclude(envelope) || changed

	// Final allowlist filter (preserves internal _ flags like _compact)
	for k := range envelope {
		if !codexResponsesAllowlist[k] && !strings.HasPrefix(k, "_") {
			delete(envelope, k)
			changed = true
		}
	}

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
