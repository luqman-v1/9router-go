package executor

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

// codexCompletedUsage extracts the usage block a response.completed event
// translates into.
func codexCompletedUsage(t *testing.T, event string) map[string]any {
	t.Helper()
	out := ProcessCodexEvent(event, &CodexStreamState{}, "chatcmpl-x", 1)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d: %v", len(out), out)
	}
	data := strings.TrimSpace(strings.TrimPrefix(out[0], "data: "))
	var chunk struct {
		Usage map[string]any `json:"usage"`
	}
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		t.Fatalf("unmarshal completion chunk: %v (got %q)", err, data)
	}
	return chunk.Usage
}

// TestCodexUsageMap pins the Responses→Chat usage conversion that the
// response.completed translation runs on. reasoning_tokens is the regression:
// it sits under output_tokens_details upstream and is billed separately from
// the visible output, so leaving it out under-counts the turn — twice, since
// sseToOpenAIJSON copies this same block into non-streaming responses.
func TestCodexUsageMap(t *testing.T) {
	tests := []struct {
		name  string
		usage string
		want  map[string]any
	}{
		{
			name:  "flat counts only",
			usage: `{"input_tokens":120,"output_tokens":34,"total_tokens":154}`,
			want:  map[string]any{"prompt_tokens": 120, "completion_tokens": 34, "total_tokens": 154},
		},
		{
			name:  "reasoning tokens survive",
			usage: `{"input_tokens":100,"output_tokens":40,"total_tokens":140,"output_tokens_details":{"reasoning_tokens":32}}`,
			want: map[string]any{
				"prompt_tokens":      100,
				"completion_tokens":  40,
				"total_tokens":       140,
				"completion_tokens_details": map[string]any{
					"reasoning_tokens": 32,
				},
			},
		},
		{
			name:  "cache and reasoning together",
			usage: `{"input_tokens":25421,"output_tokens":120,"total_tokens":25541,"input_tokens_details":{"cached_tokens":24320},"output_tokens_details":{"reasoning_tokens":65}}`,
			want: map[string]any{
				"prompt_tokens":     25421,
				"completion_tokens": 120,
				"total_tokens":      25541,
				"prompt_tokens_details": map[string]any{
					"cached_tokens": 24320,
				},
				"completion_tokens_details": map[string]any{
					"reasoning_tokens": 65,
				},
			},
		},
		{
			name:  "empty usage object yields no usage block",
			usage: `{}`,
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := codexCompletedUsage(t, `{"type":"response.completed","response":{"usage":`+tt.usage+`}}`)
			if tt.want == nil {
				if len(got) != 0 {
					t.Fatalf("expected no usage block, got %v", got)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("usage keys = %v, want %v", got, tt.want)
			}
			for k, wantVal := range tt.want {
				gotVal, ok := got[k]
				if !ok {
					t.Errorf("usage missing key %q (got %v)", k, got)
					continue
				}
				if wantNested, isNested := wantVal.(map[string]any); isNested {
					gotNested, _ := gotVal.(map[string]any)
					for nk, nv := range wantNested {
						if wf, ok := nv.(int); ok {
							if gf, ok := gotNested[nk].(float64); !ok || gf != float64(wf) {
								t.Errorf("usage[%q][%q] = %v, want %v", k, nk, gotNested[nk], nv)
							}
							continue
						}
						if gotNested[nk] != nv {
							t.Errorf("usage[%q][%q] = %v, want %v", k, nk, gotNested[nk], nv)
						}
					}
					continue
				}
				// JSON decodes every number to float64; the expectations are
				// written as untyped ints, so compare numerically.
				if wf, ok := wantVal.(int); ok {
					gf, ok := gotVal.(float64)
					if !ok || gf != float64(wf) {
						t.Errorf("usage[%q] = %v, want %v", k, gotVal, wantVal)
					}
					continue
				}
				if gotVal != wantVal {
					t.Errorf("usage[%q] = %v, want %v", k, gotVal, wantVal)
				}
			}
		})
	}
}

// TestSSEToOpenAIJSONPreservesCodexUsage covers the non-streaming half of the
// same loss: sseToOpenAIJSON copies the translated chunk's usage verbatim, so
// a reasoning turn aggregated into a chat.completion kept its reasoning count
// only if the chunk carried one.
func TestSSEToOpenAIJSONPreservesCodexUsage(t *testing.T) {
	usage := `{"input_tokens":25421,"output_tokens":120,"total_tokens":25541,` +
		`"input_tokens_details":{"cached_tokens":24320},"output_tokens_details":{"reasoning_tokens":65}}`
	chunk := codexCompletedUsage(t, `{"type":"response.completed","response":{"usage":`+usage+`}}`)
	body, err := json.Marshal(map[string]any{
		"id":      "chatcmpl-x",
		"created": 1,
		"model":   "gpt-5",
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
		"usage":   chunk,
	})
	if err != nil {
		t.Fatalf("marshal chunk: %v", err)
	}

	raw, ok := sseToOpenAIJSON([]byte("data: " + string(body) + "\n\ndata: [DONE]\n\n"))
	if !ok {
		t.Fatal("expected sseToOpenAIJSON to accept the chunk")
	}
	var out struct {
		Usage struct {
			PromptTokens      int `json:"prompt_tokens"`
			CompletionTokens  int `json:"completion_tokens"`
			PromptDetails     struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
			CompletionDetails struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal aggregated response: %v", err)
	}
	if out.Usage.PromptTokens != 25421 || out.Usage.CompletionTokens != 120 {
		t.Errorf("prompt/completion = %d/%d, want 25421/120", out.Usage.PromptTokens, out.Usage.CompletionTokens)
	}
	if out.Usage.PromptDetails.CachedTokens != 24320 {
		t.Errorf("cached_tokens = %d, want 24320", out.Usage.PromptDetails.CachedTokens)
	}
	if out.Usage.CompletionDetails.ReasoningTokens != 65 {
		t.Errorf("reasoning_tokens = %d, want 65", out.Usage.CompletionDetails.ReasoningTokens)
	}
}