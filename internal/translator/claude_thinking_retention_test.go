package translator

import (
	json "encoding/json/v2"
	"reflect"
	"testing"
)

func TestSanitizeClaudePassthrough_ThinkingRetention(t *testing.T) {
	const validSig = "EpwGCukGAXLI2nxw"

	tests := []struct {
		name        string
		body        string
		wantContent []map[string]any
	}{
		{
			name: "valid blank thinking block is kept with sibling tool_use",
			body: `{
				"messages": [
					{"role": "user", "content": "list"},
					{"role": "assistant", "content": [
						{"type": "thinking", "thinking": "", "signature": "` + validSig + `"},
						{"type": "tool_use", "id": "toolu_01", "name": "Bash", "input": {}}
					]},
					{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "toolu_01", "content": "ok"}]}
				]
			}`,
			wantContent: []map[string]any{
				{"type": "thinking", "thinking": "", "signature": validSig},
				{"type": "tool_use", "id": "toolu_01", "name": "Bash", "input": map[string]any{}},
			},
		},
		{
			name: "invalid signature thinking block is dropped",
			body: `{
				"messages": [
					{"role": "user", "content": "list"},
					{"role": "assistant", "content": [
						{"type": "thinking", "thinking": "", "signature": "invalid-sig"},
						{"type": "tool_use", "id": "toolu_01", "name": "Bash", "input": {}}
					]},
					{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "toolu_01", "content": "ok"}]}
				]
			}`,
			wantContent: []map[string]any{
				{"type": "tool_use", "id": "toolu_01", "name": "Bash", "input": map[string]any{}},
			},
		},
		{
			name: "redacted_thinking block is kept without signature",
			body: `{
				"messages": [
					{"role": "user", "content": "list"},
					{"role": "assistant", "content": [
						{"type": "redacted_thinking", "data": "redacted_bytes"},
						{"type": "tool_use", "id": "toolu_01", "name": "Bash", "input": {}}
					]},
					{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "toolu_01", "content": "ok"}]}
				]
			}`,
			wantContent: []map[string]any{
				{"type": "redacted_thinking", "data": "redacted_bytes"},
				{"type": "tool_use", "id": "toolu_01", "name": "Bash", "input": map[string]any{}},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := SanitizeClaudePassthrough([]byte(tc.body), false)

			var req struct {
				Messages []struct {
					Role    string `json:"role"`
					Content any    `json:"content"`
				} `json:"messages"`
			}
			if err := json.Unmarshal(out, &req); err != nil {
				t.Fatalf("unmarshal error: %v", err)
			}

			if len(req.Messages) < 2 {
				t.Fatalf("expected at least 2 messages, got %d", len(req.Messages))
			}

			asst := req.Messages[1]
			if asst.Role != "assistant" {
				t.Fatalf("expected assistant role, got %s", asst.Role)
			}

			blocks, ok := asst.Content.([]any)
			if !ok {
				t.Fatalf("expected []any content in assistant, got %T: %v", asst.Content, asst.Content)
			}

			if len(blocks) != len(tc.wantContent) {
				t.Fatalf("content len = %d, want %d (content: %v)", len(blocks), len(tc.wantContent), blocks)
			}

			for i, exp := range tc.wantContent {
				got, ok := blocks[i].(map[string]any)
				if !ok {
					t.Fatalf("block[%d] is not map[string]any: %T", i, blocks[i])
				}
				for k, v := range exp {
					if !reflect.DeepEqual(got[k], v) {
						t.Errorf("block[%d][%q] = %v, want %v", i, k, got[k], v)
					}
				}
			}
		})
	}
}
