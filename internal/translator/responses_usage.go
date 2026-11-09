package translator

import json "encoding/json/v2"

// ParseResponsesUsage reads the usage a Responses-native upstream reports,
// which counts input_tokens/output_tokens instead of the Chat Completions
// prompt_tokens/completion_tokens. Usage logging and cost estimation read the
// Chat shape everywhere, so a Responses body relayed untouched would otherwise
// log zero tokens for every codex, grok-cli or perplexity-agent turn.
func ParseResponsesUsage(body []byte) *OpenAIUsage {
	var envelope struct {
		Usage *ResponsesUsage `json:"usage"`
		// The streaming form carries usage on the terminal event under the
		// response object rather than at the top level.
		Response *struct {
			Usage *ResponsesUsage `json:"usage"`
		} `json:"response"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return nil
	}

	u := envelope.Usage
	if u == nil && envelope.Response != nil {
		u = envelope.Response.Usage
	}
	if u == nil {
		return nil
	}

	prompt := u.InputTokens
	if prompt == 0 && u.PromptTokens > 0 {
		prompt = u.PromptTokens
	}
	completion := u.OutputTokens
	if completion == 0 && u.CompletionTokens > 0 {
		completion = u.CompletionTokens
	}

	out := &OpenAIUsage{
		PromptTokens:     prompt,
		CompletionTokens: completion,
		// The figure was counted against the Responses envelope, so the
		// Chat-shaped prompt/completion split already includes the cache.
		PromptCacheIncluded: true,
	}

	cached := 0
	if u.InputTokensDetails != nil && u.InputTokensDetails.CachedTokens > 0 {
		cached = u.InputTokensDetails.CachedTokens
	} else if u.PromptTokensDetails != nil && u.PromptTokensDetails.CachedTokens > 0 {
		cached = u.PromptTokensDetails.CachedTokens
	} else if u.CachedTokens > 0 {
		cached = u.CachedTokens
	} else if u.CacheReadInputTokens > 0 {
		cached = u.CacheReadInputTokens
	}
	if cached > 0 {
		out.CachedTokens = cached
		out.PromptTokensDetails = &PromptTokensDetails{CachedTokens: cached}
	}

	// Reasoning tokens are nested under output_tokens on the Responses wire but
	// billed apart from the visible output, so dropping them under-counts every
	// reasoning turn relayed from a Responses-native upstream.
	reasoning := 0
	if u.OutputTokensDetails != nil && u.OutputTokensDetails.ReasoningTokens > 0 {
		reasoning = u.OutputTokensDetails.ReasoningTokens
	} else if u.CompletionTokensDetails != nil && u.CompletionTokensDetails.ReasoningTokens > 0 {
		reasoning = u.CompletionTokensDetails.ReasoningTokens
	}
	if reasoning > 0 {
		out.CompletionTokensDetails = &CompletionTokensDetails{ReasoningTokens: reasoning}
	}
	return out
}
