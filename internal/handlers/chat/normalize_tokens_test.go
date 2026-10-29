package chat

import (
	json "encoding/json/v2"
	"testing"
)

// TestNormalizeChatTokenFields pins what the normalizer does to the body,
// including the parts the full dispatch path cannot demonstrate on its own.
func TestNormalizeChatTokenFields(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantBody string // "" means "expect the input unchanged"
	}{
		{
			name: "both fields keep max_completion_tokens",
			body: `{"model":"m","max_tokens":1024,"max_completion_tokens":2048}`,
			// Deterministic marshal orders keys; the point is max_tokens is gone.
			wantBody: `{"max_completion_tokens":2048,"model":"m"}`,
		},
		{
			name:     "only max_tokens is left alone",
			body:     `{"model":"m","max_tokens":1024}`,
			wantBody: "",
		},
		{
			name:     "only max_completion_tokens is left alone",
			body:     `{"model":"m","max_completion_tokens":2048}`,
			wantBody: "",
		},
		{
			name:     "neither field is left alone",
			body:     `{"model":"m","messages":[]}`,
			wantBody: "",
		},
		{
			name:     "a non-object body is returned unchanged",
			body:     `[1,2,3]`,
			wantBody: "",
		},
		{
			// A value above 2^53. Decoding into map[string]any would turn it
			// into a float64 and emit 9007199254740992, silently changing a
			// field the client set.
			name:     "values outside float64 precision keep their bytes",
			body:     `{"model":"m","seed":9007199254740993,"max_tokens":1,"max_completion_tokens":2}`,
			wantBody: `{"max_completion_tokens":2,"model":"m","seed":9007199254740993}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(normalizeChatTokenFields([]byte(tt.body)))
			want := tt.wantBody
			if want == "" {
				want = tt.body
			}
			if got != want {
				t.Errorf("normalizeChatTokenFields() = %s, want %s", got, want)
			}
			if tt.wantBody == "" {
				return
			}
			// The rewritten body must still be valid JSON.
			var probe map[string]any
			if err := json.Unmarshal([]byte(got), &probe); err != nil {
				t.Errorf("rewritten body is not valid JSON: %v", err)
			}
		})
	}
}
