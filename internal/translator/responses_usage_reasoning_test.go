package translator

import "testing"

// TestParseResponsesUsageReasoning covers the reasoning half of the Responses
// usage shape. output_tokens_details.reasoning_tokens is billed apart from the
// visible output, so a Responses-native upstream that reports it must not have
// it dropped on the way into the Chat-shaped usage every log and cost estimate
// reads.
func TestParseResponsesUsageReasoning(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{
			name: "non-streaming body with output details",
			body: `{"id":"resp_1","usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120,"output_tokens_details":{"reasoning_tokens":12}}}`,
			want: 12,
		},
		{
			name: "terminal stream event with output details",
			body: `{"type":"response.completed","response":{"usage":{"input_tokens":9,"output_tokens":30,"output_tokens_details":{"reasoning_tokens":25}}}}`,
			want: 25,
		},
		{
			name: "cache and reasoning together",
			body: `{"usage":{"input_tokens":25421,"output_tokens":120,"input_tokens_details":{"cached_tokens":24320},"output_tokens_details":{"reasoning_tokens":65}}}`,
			want: 65,
		},
		{
			name: "zero reasoning tokens leaves details nil",
			body: `{"usage":{"input_tokens":10,"output_tokens":4,"output_tokens_details":{"reasoning_tokens":0}}}`,
			want: 0,
		},
		{
			name: "missing output details",
			body: `{"usage":{"input_tokens":10,"output_tokens":4}}`,
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseResponsesUsage([]byte(tt.body))
			if got == nil {
				t.Fatal("expected usage, got nil")
			}
			if tt.want == 0 {
				if got.CompletionTokensDetails != nil {
					t.Fatalf("expected nil CompletionTokensDetails, got %+v", got.CompletionTokensDetails)
				}
				return
			}
			if got.ReasoningTokens() != tt.want {
				t.Errorf("ReasoningTokens() = %d, want %d", got.ReasoningTokens(), tt.want)
			}
		})
	}
}

// TestParseResponsesUsageCachedAndReasoning is the both-details regression: the
// cache fix predates the reasoning one, and adding the reasoning read must not
// disturb the cached read that billing already depends on.
func TestParseResponsesUsageCachedAndReasoning(t *testing.T) {
	body := []byte(`{"usage":{"input_tokens":25421,"output_tokens":120,"total_tokens":25541,` +
		`"input_tokens_details":{"cached_tokens":24320},"output_tokens_details":{"reasoning_tokens":65}}}`)

	got := ParseResponsesUsage(body)
	if got == nil {
		t.Fatal("expected usage, got nil")
	}
	if got.PromptTokens != 25421 || got.CompletionTokens != 120 {
		t.Errorf("prompt/completion = %d/%d, want 25421/120", got.PromptTokens, got.CompletionTokens)
	}
	if got.GetCachedTokens() != 24320 {
		t.Errorf("GetCachedTokens() = %d, want 24320", got.GetCachedTokens())
	}
	if got.ReasoningTokens() != 65 {
		t.Errorf("ReasoningTokens() = %d, want 65", got.ReasoningTokens())
	}
	if !got.PromptCacheIncluded {
		t.Error("PromptCacheIncluded = false, want true: the Responses input count already includes the cache")
	}
}