package middleware

import (
	json "encoding/json/v2"
	"strings"
)

// estimatePromptTokens counts the input tokens a request is about to send.
//
// TPM cannot be enforced after the fact: the budget has to be decided before the
// request leaves. This is an estimate, not a tokenizer — it walks the decoded
// payload and counts characters the way the gateway's existing Anthropic
// estimator does (four characters per token), because a real tokenizer per vendor
// would mean shipping model vocabularies and guessing which one applies.
//
// It is deliberately generous: over-counting rejects a request that would have
// fit, while under-counting lets a caller blow through the budget they were
// given. Biasing high is the safe direction for a limit.
func estimatePromptTokens(body []byte) int {
	if len(body) == 0 {
		return 0
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		// Undecodable body: the downstream handler owns it, but the limiter
		// still has to charge something. Counting the raw bytes is the only
		// honest fallback available here.
		return (len(body) + 3) / 4
	}

	var chars int
	// The messages/system/tools triple covers OpenAI chat, Anthropic messages,
	// and the Responses shape, which all carry prompt text in one of them.
	for _, key := range [...]string{"messages", "system", "tools", "input", "contents"} {
		if v, ok := payload[key]; ok {
			chars += valueChars(v)
		}
	}
	// Responses-style requests put the whole turn under a single key.
	if chars == 0 {
		if v, ok := payload["prompt"]; ok {
			chars += valueChars(v)
		}
	}
	if chars == 0 {
		chars = len(body)
	}

	tokens := (chars + 3) / 4
	// Every request carries protocol overhead — role markers, framing, the model
	// name — that the character count misses. A floor keeps a trivially small
	// request from consuming effectively zero budget.
	const minimumPromptTokens = 1
	if tokens < minimumPromptTokens {
		tokens = minimumPromptTokens
	}
	return tokens
}

// valueChars counts the character length of a decoded JSON value, recursing into
// objects and arrays so nested tool schemas are not free.
func valueChars(v any) int {
	switch t := v.(type) {
	case string:
		return len(t)
	case float64, bool, nil:
		return 4
	case []any:
		total := 0
		for _, item := range t {
			total += valueChars(item)
		}
		return total
	case map[string]any:
		total := 0
		for key, item := range t {
			// Include the key: field names are part of what the model reads.
			total += len(key) + valueChars(item)
		}
		return total
	default:
		return len(strings.Trim(stringify(v), `"`))
	}
}

// stringify is a minimal fallback for the rare decoded type the switch above
// does not name. It is intentionally not the general case: every JSON number,
// string, bool and null is already handled above.
func stringify(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}