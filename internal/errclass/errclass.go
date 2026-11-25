package errclass

import "strings"

// containsAny reports whether lower contains any of phrases. lower is
// expected to already be lowercased; the phrase tables are stored lowercase
// so no per-call allocation is spent normalising either side.
func containsAny(lower string, phrases []string) bool {
	for _, p := range phrases {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// contextWindowPhrases are lowercase substrings that reliably indicate the
// prompt exceeds the model's context window: OpenAI (context_length_exceeded),
// Anthropic, Gemini, Bedrock, llama.cpp, Cerebras, vLLM and HuggingFace each
// word the condition differently, and the list has to cover all of them
// because a misread here decides whether the chain advances to a larger model
// or flips the request to a retryable failure.
var contextWindowPhrases = []string{
	"context_length_exceeded",
	"context length exceeded",
	"exceed context limit",
	"exceeds the context window",
	"exceeds context window",
	"this model's maximum context length is",
	"model's maximum context limit",
	"maximum context length",
	"is longer than the model's context length",
	"input tokens exceed the configured limit",
	"`inputs` tokens + `max_new_tokens` must be",
	"exceeds the available context size",
	"exceeds the maximum number of tokens allowed",
	"prompt is too long",
	"prompt: length",
	"input is too long",
	"too many input tokens",
	"too many tokens",
	"request payload size exceeds",
	"string too long. expected a string with maximum length",
	"input length and `max_tokens` exceed context limit",
}

// contextWindowExclusions prevent unrelated "too long" validation errors (for
// example an oversized `user` field) from being classified as context
// overflow. The generic positive phrases above would otherwise catch them.
var contextWindowExclusions = []string{
	"string_above_max_length",
}

// rateLimitNotContextPhrases are throttling messages that mention token counts
// to name the limit ("Rate limit reached ... too many tokens per min"). A rate
// limit is never a context overflow: the account is fine and a wait clears it,
// while a real overflow needs a bigger model. Ordering the exclusion before
// the positive match keeps the shared vocabulary from landing the wrong verdict.
var rateLimitNotContextPhrases = []string{
	"rate limit",
	"rate_limit",
	"per min",
	"per minute",
	"per second",
	"per hour",
}

// LooksLikeContextWindow reports whether an error body describes a prompt that
// does not fit the model's context window. Open/closed both on "exceeds model
// max" phrasing and on the positional fallbacks (Cerebras "current length is N
// while limit is M", HuggingFace "maximum input length is N tokens").
func LooksLikeContextWindow(body string) bool {
	if body == "" {
		return false
	}
	s := strings.ToLower(body)
	if containsAny(s, contextWindowExclusions) {
		return false
	}
	if strings.Contains(s, "invalid 'user'") && strings.Contains(s, "string too long") {
		return false
	}
	if containsAny(s, rateLimitNotContextPhrases) {
		return false
	}
	if containsAny(s, contextWindowPhrases) {
		return true
	}
	// Cerebras: "current length is N while limit is M"; HF: "maximum input
	// length is N tokens". These only read as overflow when both halves are
	// present, so the compound check avoids a bare mention of either clause.
	if strings.Contains(s, "current length is") && strings.Contains(s, "while limit is") {
		return true
	}
	if strings.Contains(s, "maximum input length is") && strings.Contains(s, "tokens") {
		return true
	}
	return false
}

// authPhrases are lowercase substrings providers use for credential failures
// reported under a status other than 401/403. Cloudflare answers an invalid
// token with HTTP 400 {"errors":[{"code":9106,"message":"Authentication
// failed"}]}, and gateways frequently hide a 401 behind a 4xx wrapper, so the
// status alone is not enough to name the credential dead.
var authPhrases = []string{
	"authentication failed",
	"authentication error",
	"invalid api key",
	"invalid api token",
	"invalid token",
	"incorrect api key",
	"unauthorized",
	"invalid x-api-key",
	"api key not valid",
	"invalid authentication",
}

// LooksLikeAuthError reports whether an error body describes rejected
// credentials regardless of the HTTP status it came with. Only a body verdict
// can: a provider misfiring a 429 or a 502 can still reject the key in prose.
func LooksLikeAuthError(body string) bool {
	if body == "" {
		return false
	}
	return containsAny(strings.ToLower(body), authPhrases)
}

// contentFilterPhrases are lowercase substrings used by OpenAI, Azure,
// Anthropic, Gemini and Bedrock when a safety system rejects a request. This
// classification exists so the chain can advance to a provider with a
// different policy instead of burning the same policy's refusal again.
var contentFilterPhrases = []string{
	"content_policy_violation",
	"content_filter_policy",
	"responsibleaipolicyviolation",
	"content management policy",
	"your task failed as a result of our safety system",
	"your request was rejected as a result of our safety system",
	"request was rejected as a result of the safety system",
	"violating our usage policy",
	"content filtering policy",
	"the response was blocked",
	"output blocked by content filtering policy",
	"blocked by content filter",
	"prohibited_content",
}

// LooksLikeContentFilter reports whether an error body describes a
// content-policy rejection rather than a transport or model condition.
func LooksLikeContentFilter(body string) bool {
	if body == "" {
		return false
	}
	return containsAny(strings.ToLower(body), contentFilterPhrases)
}
