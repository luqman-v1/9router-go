package executor

import (
	"encoding/json"
	"testing"
)

func TestClampVolcengineArkGLM5OutputTokens(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		want      map[string]any
		unchanged bool
	}{
		{
			name: "glm-5 max_tokens above ceiling is clamped",
			body: `{"model":"glm-5","max_tokens":8192,"messages":[{"role":"user","content":"hi"}]}`,
			want: map[string]any{"max_tokens": float64(4096)},
		},
		{
			name: "glm-5 max_tokens at ceiling preserved",
			body: `{"model":"glm-5","max_tokens":4096,"messages":[]}`,
			want: map[string]any{"max_tokens": float64(4096)},
		},
		{
			name: "glm-5 max_tokens below ceiling preserved",
			body: `{"model":"glm-5","max_tokens":2048,"messages":[]}`,
			want: map[string]any{"max_tokens": float64(2048)},
		},
		{
			name: "glm-5 max_completion_tokens above ceiling is clamped",
			body: `{"model":"glm-5","max_completion_tokens":8192,"messages":[]}`,
			want: map[string]any{"max_completion_tokens": float64(4096)},
		},
		{
			name: "doubao glm-5 model clamped",
			body: `{"model":"doubao-glm-5-256k","max_tokens":8192,"messages":[]}`,
			want: map[string]any{"max_tokens": float64(4096)},
		},
		{
			name:      "non-glm5 model untouched above ceiling",
			body:      `{"model":"gpt-4o","max_tokens":8192,"messages":[]}`,
			want:      map[string]any{"max_tokens": float64(8192)},
			unchanged: true,
		},
		{
			name:      "non-prefixed glm model untouched",
			body:      `{"model":"glm-4","max_tokens":8192,"messages":[]}`,
			want:      map[string]any{"max_tokens": float64(8192)},
			unchanged: true,
		},
		{
			name:      "glm-5 with no budget unchanged",
			body:      `{"model":"glm-5","messages":[]}`,
			unchanged: true,
		},
		{
			name:      "unparsable body unchanged",
			body:      `not json`,
			unchanged: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := clampVolcengineArkGLM5OutputTokens([]byte(tt.body))
			if tt.unchanged {
				if string(got) != tt.body {
					t.Errorf("body changed: %s, want unchanged %s", got, tt.body)
				}
				return
			}
			var parsed map[string]any
			if err := json.Unmarshal(got, &parsed); err != nil {
				t.Fatalf("clamped body does not parse: %v", err)
			}
			for k, w := range tt.want {
				if parsed[k] != w {
					t.Errorf("%s = %v, want %v", k, parsed[k], w)
				}
			}
		})
	}
}
