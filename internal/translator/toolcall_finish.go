package translator

import (
	"bytes"

	"9router/proxy/internal/fastjson"
)

// NormalizeToolCallFinish repairs a non-streaming (unary) OpenAI chat-completion
// body whose assistant turn ended with tool calls but whose finish_reason says
// "stop" or is empty. vLLM reports a plain stop for a named tool_choice, and
// agent loops key off the finish reason, so they treat the tool call as the
// final answer and the turn ends (upstream mydisha/keirouter e340784, whose
// stream-side counterpart — a trailing stop must not override tool_calls — is
// a separate concern of the passthrough lane and intentionally not ported).
//
// The fix is applied to the body the gateway writes, so every client dialect
// benefits: an OpenAI client reads tool_calls, the Responses bridge emits a
// tool-call output, and the OpenAI-to-Claude translation maps it to
// stop_reason tool_use.
//
// The repair is the same map-and-remarshal idiom the other body-repair paths
// use (tool-call id filling, tool-schema normalization): parse, fix the
// finish_reason of the eligible choice, serialize back with fastjson so the
// map order is deterministic. A JSON round-trip is faithful here — in
// particular the tool call's arguments field, which is itself a JSON document
// stored as a JSON string, unescapes to the document and re-escapes to the
// same wire bytes. Bodies that do not need the patch keep their exact bytes,
// so the common case (a plain stop, or no tool call) stays a zero-copy
// pass-through; a body that fails to parse is relayed untouched, and a
// response that is not a Chat Completions shape (Claude Messages, Ollama, an
// error envelope) has no matching choices/message/tool_calls and is left
// alone.
func NormalizeToolCallFinish(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"tool_calls"`)) {
		return body
	}
	var resp map[string]any
	if err := fastjson.Unmarshal(body, &resp); err != nil {
		return body
	}
	choices, _ := resp["choices"].([]any)
	if !hasEligibleChoice(choices) {
		return body
	}
	for _, c := range choices {
		choice, ok := c.(map[string]any)
		if !ok {
			continue
		}
		finish, _ := choice["finish_reason"].(string)
		if finish != "stop" && finish != "" {
			continue
		}
		message, ok := choice["message"].(map[string]any)
		if !ok {
			continue
		}
		calls, _ := message["tool_calls"].([]any)
		for _, raw := range calls {
			tc, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			fn, _ := tc["function"].(map[string]any)
			if name, _ := fn["name"].(string); name != "" {
				choice["finish_reason"] = "tool_calls"
				break
			}
		}
	}
	out, err := fastjson.Marshal(resp)
	if err != nil {
		return body
	}
	return out
}

// hasEligibleChoice reports whether any choice of the body carries a "stop" or
// empty finish_reason on a message with at least one named tool call — the
// exact vLLM shape that needs the patch.
func hasEligibleChoice(choices []any) bool {
	for _, c := range choices {
		choice, ok := c.(map[string]any)
		if !ok {
			continue
		}
		finish, _ := choice["finish_reason"].(string)
		if finish != "stop" && finish != "" {
			continue
		}
		message, ok := choice["message"].(map[string]any)
		if !ok {
			continue
		}
		calls, _ := message["tool_calls"].([]any)
		for _, raw := range calls {
			tc, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			fn, _ := tc["function"].(map[string]any)
			if name, _ := fn["name"].(string); name != "" {
				return true
			}
		}
	}
	return false
}
