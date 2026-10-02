package translator

import (
	json "encoding/json/v2"
	"testing"
)

// Newer Claude models answer 400 "… does not support assistant message
// prefill" when the conversation ends on an assistant turn. The cleanup in
// SanitizeClaudePassthrough deletes messages left empty, so a trailing user
// turn that was blank (or held only dropped blocks) silently promotes the
// previous assistant turn to last. Upstream restores a placeholder user turn
// for that case and leaves a prefill the client wrote itself alone
// (open-sse/translator/formats/claude.js:345-349).

func claudeTurn(role string, blocks ...any) map[string]any {
	return map[string]any{"role": role, "content": blocks}
}

func textBlock(text string) map[string]any {
	return map[string]any{"type": "text", "text": text}
}

func messageRoles(t *testing.T, body []byte) []string {
	t.Helper()
	var decoded struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal sanitized body: %v (%s)", err, body)
	}
	roles := make([]string, 0, len(decoded.Messages))
	for _, m := range decoded.Messages {
		roles = append(roles, m.Role)
	}
	return roles
}

// TestSanitizeClaudePassthrough_CleanupInducedAssistantTail: the empty trailing
// user turn is dropped, so the repair must put a user turn back — otherwise the
// upstream 400s on a body the client never wrote that way.
func TestSanitizeClaudePassthrough_CleanupInducedAssistantTail(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5","messages":[
		{"role":"user","content":[{"type":"text","text":"hi"}]},
		{"role":"assistant","content":[{"type":"text","text":"working on it"}]},
		{"role":"user","content":[{"type":"text","text":"   "}]}
	]}`)

	got := SanitizeClaudePassthrough(body, false)

	roles := messageRoles(t, got)
	want := []string{"user", "assistant", "user"}
	if len(roles) != len(want) {
		t.Fatalf("roles = %v, want %v (body: %s)", roles, want, got)
	}
	for i, role := range want {
		if roles[i] != role {
			t.Fatalf("roles = %v, want %v (body: %s)", roles, want, got)
		}
	}
	if !containsPlaceholder(got) {
		t.Errorf("body must carry the trailing user placeholder, got %s", got)
	}
}

// TestSanitizeClaudePassthrough_ClientPrefillPreserved is the other direction: a
// body the client deliberately ended on an assistant turn keeps it, because the
// prefill is the client's own choice and only older models accept it.
func TestSanitizeClaudePassthrough_ClientPrefillPreserved(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5","messages":[
		{"role":"user","content":[{"type":"text","text":"Write a haiku"}]},
		{"role":"assistant","content":[{"type":"text","text":"Once upon a"}]}
	]}`)

	if !ClaudeIntentionalPrefill(body) {
		t.Fatal("a trailing assistant turn in messages[] is a prefill")
	}
	got := SanitizeClaudePassthrough(body, ClaudeIntentionalPrefill(body))

	if containsPlaceholder(got) {
		t.Errorf("a client prefill must not get a placeholder appended: %s", got)
	}
	roles := messageRoles(t, got)
	if len(roles) != 2 || roles[1] != "assistant" {
		t.Fatalf("roles = %v, want the client's assistant tail kept", roles)
	}
}

// TestClaudeIntentionalPrefill reads the tail in the source format's own shape,
// because messages[] is not the only one a request arrives in.
func TestClaudeIntentionalPrefill(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "claude client tail",
			body: `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"prefill"}]}`,
			want: true,
		},
		{
			name: "claude client ending on a user turn",
			body: `{"messages":[{"role":"assistant","content":"x"},{"role":"user","content":"hi"}]}`,
			want: false,
		},
		{
			name: "gemini/Antigravity model tail",
			body: `{"contents":[{"role":"user","parts":[]},{"role":"model","parts":[]}]}`,
			want: true,
		},
		{
			name: "responses/codex assistant tail",
			body: `{"input":[{"role":"user","content":"hi"},{"role":"assistant","content":"prefill"}]}`,
			want: true,
		},
		{
			name: "function output tail carries no role",
			body: `{"input":[{"role":"assistant","content":"x"},{"type":"function_call_output","call_id":"c1"}]}`,
			want: false,
		},
		{
			name: "no conversation at all",
			body: `{"model":"claude-opus-5"}`,
			want: false,
		},
		{
			name: "not JSON",
			body: `{`,
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClaudeIntentionalPrefill([]byte(tt.body)); got != tt.want {
				t.Errorf("ClaudeIntentionalPrefill(%s) = %v, want %v", tt.body, got, tt.want)
			}
		})
	}
}

// TestEnsureTrailingUserTurnBody covers the path that runs after the OpenAI→
// Claude conversion, where the merged turns can end on an assistant message even
// though the client body ended on a tool result.
func TestEnsureTrailingUserTurnBody(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		prefill    bool
		wantRepair bool
	}{
		{
			name:       "assistant tail gets the placeholder",
			body:       `{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]},{"role":"assistant","content":[{"type":"text","text":"partial"}]}]}`,
			prefill:    false,
			wantRepair: true,
		},
		{
			name:       "client prefill is left alone",
			body:       `{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]},{"role":"assistant","content":[{"type":"text","text":"partial"}]}]}`,
			prefill:    true,
			wantRepair: false,
		},
		{
			name:       "user tail needs no repair",
			body:       `{"messages":[{"role":"assistant","content":[{"type":"text","text":"hi"}]},{"role":"user","content":[{"type":"text","text":"go on"}]}]}`,
			prefill:    false,
			wantRepair: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EnsureTrailingUserTurnBody([]byte(tt.body), tt.prefill)
			repaired := containsPlaceholder(got)
			if repaired != tt.wantRepair {
				t.Fatalf("placeholder appended = %v, want %v (body: %s)", repaired, tt.wantRepair, got)
			}
			if !tt.wantRepair && string(got) != tt.body {
				t.Errorf("body must be returned unchanged, got %s", got)
			}
		})
	}
}

func containsPlaceholder(body []byte) bool {
	var decoded struct {
		Messages []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return false
	}
	for _, m := range decoded.Messages {
		for _, b := range m.Content {
			if b.Type == "text" && b.Text == TrailingUserPlaceholder {
				return true
			}
		}
	}
	return false
}
