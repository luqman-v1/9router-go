package translator

import "testing"

// TestParseResponsesUsage covers the two places a Responses-native upstream
// reports usage, and the cached-token detail the dashboard bills from. Without
// this a relayed codex turn logs zero tokens.
func TestParseResponsesUsage(t *testing.T) {
	tests := []struct {
		name           string
		body           string
		wantPrompt     int
		wantCompletion int
		wantCached     int
	}{
		{
			name:           "non-streaming body",
			body:           `{"id":"resp_1","usage":{"input_tokens":120,"output_tokens":34,"total_tokens":154}}`,
			wantPrompt:     120,
			wantCompletion: 34,
		},
		{
			name:           "terminal stream event",
			body:           `{"type":"response.completed","response":{"usage":{"input_tokens":9,"output_tokens":2}}}`,
			wantPrompt:     9,
			wantCompletion: 2,
		},
		{
			name:           "cached prompt detail",
			body:           `{"usage":{"input_tokens":80,"output_tokens":5,"input_tokens_details":{"cached_tokens":64}}}`,
			wantPrompt:     80,
			wantCompletion: 5,
			wantCached:     64,
		},
		{
			name:           "terminal stream event with cached tokens",
			body:           `{"type":"response.completed","response":{"id":"resp_123","usage":{"input_tokens":5000,"output_tokens":150,"total_tokens":5150,"input_tokens_details":{"cached_tokens":4000}}}}`,
			wantPrompt:     5000,
			wantCompletion: 150,
			wantCached:     4000,
		},
		{
			name:           "terminal stream event response.done with prompt_tokens_details",
			body:           `{"type":"response.done","response":{"usage":{"prompt_tokens":1000,"completion_tokens":200,"prompt_tokens_details":{"cached_tokens":800}}}}`,
			wantPrompt:     1000,
			wantCompletion: 200,
			wantCached:     800,
		},
		{
			name: "no usage at all",
			body: `{"id":"resp_1","output":[]}`,
		},
		{
			name: "explicit null usage",
			body: `{"usage":null}`,
		},
		{
			name: "not json",
			body: `upstream exploded`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseResponsesUsage([]byte(tt.body))
			if tt.wantPrompt == 0 && tt.wantCompletion == 0 {
				if got != nil {
					t.Fatalf("expected no usage, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected usage, got nil")
			}
			if got.PromptTokens != tt.wantPrompt {
				t.Errorf("PromptTokens = %d, want %d", got.PromptTokens, tt.wantPrompt)
			}
			if got.CompletionTokens != tt.wantCompletion {
				t.Errorf("CompletionTokens = %d, want %d", got.CompletionTokens, tt.wantCompletion)
			}
			if got.CachedTokens != tt.wantCached {
				t.Errorf("CachedTokens = %d, want %d", got.CachedTokens, tt.wantCached)
			}
		})
	}
}
