package translator

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExtractSystemMessages(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantSys   string
		wantSysOK bool
		wantRoles []string
		sameBytes bool
	}{
		{
			name:      "single system message at index 0 is promoted to system",
			body:      `{"model":"claude-sonnet-4-6","messages":[{"role":"system","content":"You are helpful"},{"role":"user","content":"hi"}]}`,
			wantSys:   "You are helpful",
			wantSysOK: true,
			wantRoles: []string{"user"},
		},
		{
			name:      "mid-conversation system message is extracted and removed",
			body:      `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"},{"role":"system","content":"Be concise"},{"role":"user","content":"again"}]}`,
			wantSys:   "Be concise",
			wantSysOK: true,
			wantRoles: []string{"user", "assistant", "user"},
		},
		{
			name:      "existing top-level system is concatenated with in-body system",
			body:      `{"model":"claude-sonnet-4-6","system":"Base rules","messages":[{"role":"system","content":"Be concise"},{"role":"user","content":"hi"}]}`,
			wantSys:   "Base rules\nBe concise",
			wantSysOK: true,
			wantRoles: []string{"user"},
		},
		{
			name:      "developer role is also promoted to system",
			body:      `{"model":"claude-sonnet-4-6","messages":[{"role":"developer","content":"Prefer Go"},{"role":"user","content":"hi"}]}`,
			wantSys:   "Prefer Go",
			wantSysOK: true,
			wantRoles: []string{"user"},
		},
		{
			name:      "no system messages left untouched",
			body:      `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"}]}`,
			sameBytes: true,
		},
		{
			name:      "empty system message contributes nothing and is removed",
			body:      `{"model":"claude-sonnet-4-6","messages":[{"role":"system","content":"   "},{"role":"user","content":"hi"}]}`,
			wantSysOK: false,
			wantRoles: []string{"user"},
		},
		{
			name:      "content blocks are joined by newlines",
			body:      `{"model":"claude-sonnet-4-6","messages":[{"role":"system","content":[{"type":"text","text":"Part A"},{"type":"text","text":"Part B"}]},{"role":"user","content":"hi"}]}`,
			wantSys:   "Part A\nPart B",
			wantSysOK: true,
			wantRoles: []string{"user"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := ExtractSystemMessages([]byte(tt.body))

			if tt.sameBytes {
				if string(out) != tt.body {
					t.Errorf("expected body unchanged, got %s", out)
				}
				return
			}

			var decoded map[string]any
			if err := json.Unmarshal(out, &decoded); err != nil {
				t.Fatalf("unmarshal output: %v", err)
			}

			sys, hasSys := decoded["system"]
			sysStr, _ := sys.(string)
			if tt.wantSysOK {
				if !hasSys {
					t.Fatalf("system field missing")
				}
				if sysStr != tt.wantSys {
					t.Errorf("system = %q, want %q", sysStr, tt.wantSys)
				}
			} else if hasSys {
				t.Errorf("system field should be absent, got %v", sys)
			}

			msgs, ok := decoded["messages"].([]any)
			if !ok {
				t.Fatalf("messages missing or not an array")
			}
			if len(msgs) != len(tt.wantRoles) {
				t.Fatalf("messages has %d entries, want %d", len(msgs), len(tt.wantRoles))
			}
			var roles []string
			for _, m := range msgs {
				mm, _ := m.(map[string]any)
				r, _ := mm["role"].(string)
				roles = append(roles, r)
			}
			if got, want := strings.Join(roles, ","), strings.Join(tt.wantRoles, ","); got != want {
				t.Errorf("roles = %q, want %q", got, want)
			}
		})
	}
}
