package chat

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
)

// normalizeChatTokenFields keeps at most one of max_tokens /
// max_completion_tokens on an outbound OpenAI Chat Completions body.
//
// Both spellings are legal on the wire and some clients send both at once. The
// gateway used to forward such a body verbatim to a custom openai-compatible
// node, and an upstream that rejects the pair answered 400 "max_tokens and
// max_completion_tokens cannot both be set" on a streaming call.
//
// When both are present the explicit max_completion_tokens value is kept and
// max_tokens is dropped (the same direction upstream's provider-level rename
// takes). When the client sent neither, nothing is added — a provider default
// is never overridden. A body that does not parse as an object is returned
// unchanged.
func normalizeChatTokenFields(body []byte) []byte {
	// jsontext.Value, not any: re-marshalling through map[string]any would
	// decode every number into a float64, and a field such as a 64-bit seed
	// would come back with its low bits rounded away. The untouched fields keep
	// their original bytes this way.
	var m map[string]jsontext.Value
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		return body
	}
	_, hasMaxTokens := m["max_tokens"]
	_, hasMaxCompletion := m["max_completion_tokens"]
	if !hasMaxTokens || !hasMaxCompletion {
		return body
	}
	delete(m, "max_tokens")
	out, err := json.Marshal(m, json.Deterministic(true))
	if err != nil {
		return body
	}
	return out
}
