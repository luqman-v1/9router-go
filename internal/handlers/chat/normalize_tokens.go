package chat

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
)

// maxCompletionNeedle and jsonEscapeNeedle gate the normalizer decode below.
// The normalizer only ever changes a body that declares BOTH max_tokens and
// max_completion_tokens: a body that cannot declare max_completion_tokens
// cannot declare both, so the decode is skipped when that key cannot appear.
var (
	maxCompletionNeedle = []byte(`"max_completion_tokens"`)
	jsonEscapeNeedle    = []byte(`\u`)
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
//
// The scan before the parse is a fast path, not a parser: a body it lets
// through is parsed exactly as before, so a body it skips must be one the parse
// could not have changed. It skips only when max_completion_tokens cannot
// appear in any legal spelling — see mayCarryCompletionTokens for why an
// escaped body always falls through.
func normalizeChatTokenFields(body []byte) []byte {
	if !mayCarryCompletionTokens(body) {
		return body
	}
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

// mayCarryCompletionTokens reports whether body may declare
// max_completion_tokens in any legal JSON spelling.
//
// The normalizer only ever changes a body that declares BOTH token keys. A
// request that cannot declare max_completion_tokens therefore cannot be
// changed by the parse, and that is the vast majority of requests: legacy
// clients send max_tokens only, and clients that send no budget at all keep the
// provider default.
//
// A JSON member name may be written with \u escapes, so the decoded key can
// differ from its bytes on the wire: {"\u006dax_completion_tokens":1} declares
// the key without carrying the literal byte sequence. Every body carrying any
// escape therefore falls through to the parse. That is deliberate: matching
// only the literal spelling would skip the normalizer on an escaped body and
// put both fields back on the wire, which is the bug this function exists to
// prevent.
func mayCarryCompletionTokens(body []byte) bool {
	if bytes.Contains(body, jsonEscapeNeedle) {
		return true
	}
	return bytes.Contains(body, maxCompletionNeedle)
}
