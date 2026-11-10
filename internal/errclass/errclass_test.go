package errclass

import "testing"

func TestLooksLikeContextWindow(t *testing.T) {
	positiveBodies := []string{
		`{"error":{"message":"This model's maximum context length is 128000 tokens. However, your messages resulted in 130000 tokens.","code":"context_length_exceeded"}}`,
		`{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 210000 tokens > 200000 maximum"}}`,
		`{"error":{"code":400,"message":"The input token count (1200000) exceeds the maximum number of tokens allowed (1048576)."}}`,
		`Input is too long for requested model.`,
		// Cerebras positional fallback: "current length is N while limit is M".
		`{"error":{"message":"current length is 300000 while limit is 200000"}}`,
		// HuggingFace fallback: "maximum input length is N tokens".
		`{"error":{"message":"maximum input length is 5120 tokens, but you requested 6000"}}`,
	}
	for _, body := range positiveBodies {
		if !LooksLikeContextWindow(body) {
			t.Errorf("LooksLikeContextWindow(%q) = false, want true", body)
		}
	}

	negativeBodies := []string{
		`Rate limit reached for gpt-5-codex: too many tokens per min. Limit 30000, Requested 31000.`,
		`{"error":{"message":"Invalid 'user': string too long. Expected a string with maximum length 256"}}`,
		`{"error":{"message":"string_above_max_length"}}`,
		`{"error":{"message":"Incorrect API key provided"}}`,
		``,
	}
	for _, body := range negativeBodies {
		if LooksLikeContextWindow(body) {
			t.Errorf("LooksLikeContextWindow(%q) = true, want false", body)
		}
	}
}

func TestLooksLikeAuthError(t *testing.T) {
	positiveBodies := []string{
		`{"errors":[{"code":9106,"message":"Authentication failed"}]}`,
		`{"error":"invalid api key"}`,
		`Incorrect API key provided`,
		`{"error":{"message":"Invalid x-api-key"}}`,
		`{"error":{"message":"Authorization: invalid token"}}`,
	}
	for _, body := range positiveBodies {
		if !LooksLikeAuthError(body) {
			t.Errorf("LooksLikeAuthError(%q) = false, want true", body)
		}
	}

	negativeBodies := []string{
		`{"error":{"message":"internal server error"}}`,
		``,
	}
	for _, body := range negativeBodies {
		if LooksLikeAuthError(body) {
			t.Errorf("LooksLikeAuthError(%q) = true, want false", body)
		}
	}
}

func TestLooksLikeContentFilter(t *testing.T) {
	positiveBodies := []string{
		`{"error":{"code":"content_policy_violation","message":"Your request was rejected"}}`,
		`The response was filtered due to the prompt triggering Azure OpenAI's content management policy.`,
		`{"error":{"message":"The response was blocked"}}`,
	}
	for _, body := range positiveBodies {
		if !LooksLikeContentFilter(body) {
			t.Errorf("LooksLikeContentFilter(%q) = false, want true", body)
		}
	}

	negativeBodies := []string{
		`{"error":{"message":"model not found"}}`,
		``,
	}
	for _, body := range negativeBodies {
		if LooksLikeContentFilter(body) {
			t.Errorf("LooksLikeContentFilter(%q) = true, want false", body)
		}
	}
}
