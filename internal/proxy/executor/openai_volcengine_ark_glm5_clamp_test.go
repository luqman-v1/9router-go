package executor

import (
	"encoding/json"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
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
			name: "glm-5 max_output_tokens above ceiling is clamped",
			body: `{"model":"glm-5","max_output_tokens":8192,"messages":[]}`,
			want: map[string]any{"max_output_tokens": float64(4096)},
		},
		{
			name: "glm-5 all three fields above ceiling clamped",
			body: `{"model":"glm-5","max_tokens":16384,"max_completion_tokens":8192,"max_output_tokens":12288,"messages":[]}`,
			want: map[string]any{
				"max_tokens":            float64(4096),
				"max_completion_tokens": float64(4096),
				"max_output_tokens":     float64(4096),
			},
		},
		{
			name: "doubao glm-5 model clamped",
			body: `{"model":"doubao-glm-5-256k","max_tokens":8192,"messages":[]}`,
			want: map[string]any{"max_tokens": float64(4096)},
		},
		{
			name: "zai-org GLM-5.2 clamped",
			body: `{"model":"zai-org/GLM-5.2","max_tokens":8192,"messages":[]}`,
			want: map[string]any{"max_tokens": float64(4096)},
		},
		{
			name: "GLM-5.1 uppercase clamped",
			body: `{"model":"GLM-5.1","max_tokens":8192,"messages":[]}`,
			want: map[string]any{"max_tokens": float64(4096)},
		},
		{
			name: "glm-5-turbo clamped",
			body: `{"model":"glm-5-turbo","max_tokens":8192,"messages":[]}`,
			want: map[string]any{"max_tokens": float64(4096)},
		},
		{
			name: "non-glm5 model untouched above ceiling",
			body: `{"model":"gpt-4o","max_tokens":8192,"messages":[]}`,
			want: map[string]any{"max_tokens": float64(8192)},
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
		{
			name: "glm-5 clamps to lower model maxOutput capability",
			body: `{"model":"glm-5-custom","max_tokens":8192,"messages":[]}`,
			want: map[string]any{"max_tokens": float64(2048)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "glm-5 clamps to lower model maxOutput capability" {
				providers.ClearCustomModelCaps()
				defer providers.ClearCustomModelCaps()
				providers.SetCustomModelCaps("volcengine-ark", "glm-5-custom", providers.Capabilities{MaxOutput: 2048})
			}
			got := clampVolcengineArkGLM5OutputTokens("volcengine-ark", []byte(tt.body))
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
			// Any field not in want must be left untouched (not introduced).
			if len(tt.want) > 0 {
				for _, field := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
					if _, wanted := tt.want[field]; wanted {
						continue
					}
					if strings.Contains(tt.body, field) && parsed[field] == nil {
						t.Errorf("field %s was dropped", field)
					}
				}
			}
		})
	}
}
