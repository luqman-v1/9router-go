package chat

import (
	json "encoding/json/v2"
	"testing"
)

func TestExtractLeadingJSONObject(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantMatched bool
		wantJSON    string
	}{
		{
			name:        "clean JSON without trailing content",
			input:       `{"id":"chatcmpl-1","choices":[{"message":{"content":"hello"}}]}`,
			wantMatched: false,
		},
		{
			name:        "JSON with trailing data: [DONE]",
			input:       `{"id":"chatcmpl-1","choices":[{"message":{"content":"hello"}}]}data: [DONE]` + "\n\n",
			wantMatched: true,
			wantJSON:    `{"id":"chatcmpl-1","choices":[{"message":{"content":"hello"}}]}`,
		},
		{
			name:        "JSON with nested braces and trailing text",
			input:       `{"id":"1","obj":{"nested":"value","braces":"{inside}"}}` + "\n\ndata: [DONE]\n\n",
			wantMatched: true,
			wantJSON:    `{"id":"1","obj":{"nested":"value","braces":"{inside}"}}`,
		},
		{
			name:        "no opening brace",
			input:       `data: [DONE]`,
			wantMatched: false,
		},
		{
			name:        "unbalanced braces",
			input:       `{"id":"1","unbalanced": true`,
			wantMatched: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := extractLeadingJSONObject([]byte(tt.input))
			if ok != tt.wantMatched {
				t.Fatalf("extractLeadingJSONObject() ok = %v, want %v", ok, tt.wantMatched)
			}
			if tt.wantMatched {
				if string(got) != tt.wantJSON {
					t.Errorf("got %s, want %s", string(got), tt.wantJSON)
				}
				var check any
				if err := json.Unmarshal(got, &check); err != nil {
					t.Errorf("result is not valid JSON: %s (err: %v)", string(got), err)
				}
			}
		})
	}
}
