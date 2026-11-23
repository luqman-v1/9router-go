package executor

import (
	"encoding/json"
	"strings"
	"testing"
)

// Client billing headers vary per Claude Code turn (cron/subagent/version).
// Keeping them in the translated system text splits the cached system prompt;
// Anthropic only ignores the header as a separate system[0] block, which the
// OAuth cloaking layer already supplies (upstream decolua/9router@971ae638,
// fixes #5739).
func TestEnsureClaudeMessages_StripsClientBillingHeaderFromSystem(t *testing.T) {
	const header = "x-anthropic-billing-header: cc_version=2.1.322; cc_entrypoint=cli;"
	const instructions = "Follow the project instructions.\nKeep answers concise."
	const userText = header + "\nExplain this header."

	tests := []struct {
		name   string
		system any
		want   string
	}{
		{
			name:   "string system text",
			system: header + "\n" + instructions,
			want:   instructions,
		},
		{
			name:   "CRLF mixed-case header",
			system: "X-ANTHROPIC-BILLING-HEADER: cc_version=2.1.322;\r\n" + instructions,
			want:   instructions,
		},
		{
			name: "separate text blocks, billing-only block omitted",
			system: []any{
				map[string]any{"type": "text", "text": header},
				map[string]any{"type": "text", "text": instructions},
			},
			want: instructions,
		},
		{
			name: "header inside an otherwise stable block",
			system: []any{
				map[string]any{"type": "text", "text": "Stable prefix"},
				map[string]any{"type": "text", "text": header + "\n" + instructions},
			},
			want: "Stable prefix\n\n" + instructions,
		},
		{
			name:   "billing header mentioned after real instructions",
			system: "Document " + header + "\n" + header + "\n" + instructions,
			want:   "Document " + header + "\n" + header + "\n" + instructions,
		},
		{
			name:   "billing-only system message",
			system: header,
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{
				"messages": []any{
					map[string]any{"role": "system", "content": tt.system},
					map[string]any{"role": "user", "content": userText},
				},
			})
			if err != nil {
				t.Fatalf("marshal input: %v", err)
			}

			var got struct {
				System   string `json:"system"`
				Messages []struct {
					Content json.RawMessage `json:"content"`
				} `json:"messages"`
			}
			if err := json.Unmarshal(EnsureClaudeMessages(body, "claude-sonnet-4"), &got); err != nil {
				t.Fatalf("unmarshal output: %v", err)
			}
			if got.System != tt.want {
				t.Errorf("system = %q, want %q", got.System, tt.want)
			}
			if len(got.Messages) != 1 {
				t.Fatalf("messages = %d, want 1", len(got.Messages))
			}
			var userContent string
			if err := json.Unmarshal(got.Messages[0].Content, &userContent); err != nil {
				t.Fatalf("unmarshal user content: %v", err)
			}
			if userContent != userText {
				t.Errorf("user content = %q, want %q (must not be stripped)", userContent, userText)
			}
			if !strings.Contains(got.System, "Follow the project instructions.") && tt.want != "" && !strings.Contains(tt.want, "Document ") {
				t.Errorf("system lost its instructions: %q", got.System)
			}
		})
	}
}
