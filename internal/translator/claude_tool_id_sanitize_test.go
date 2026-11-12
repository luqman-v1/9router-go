package translator

import (
	"encoding/json"
	"regexp"
	"testing"
)

var anthropicToolIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func TestSanitizeClaudeToolID(t *testing.T) {
	for _, id := range []string{"toolu_01abc", "call_1", "abc-DEF_123", ""} {
		if got := SanitizeClaudeToolID(id); got != id {
			t.Errorf("SanitizeClaudeToolID(%q) = %q, want unchanged", id, got)
		}
	}
	for _, id := range []string{"call.a:b", "toolu/1", "id with space", "訊息1"} {
		got := SanitizeClaudeToolID(id)
		if !anthropicToolIDPattern.MatchString(got) {
			t.Errorf("SanitizeClaudeToolID(%q) = %q, does not match the Anthropic pattern", id, got)
		}
		if again := SanitizeClaudeToolID(id); again != got {
			t.Errorf("SanitizeClaudeToolID(%q) is not deterministic: %q then %q", id, got, again)
		}
	}
}

func TestSanitizeClaudePassthrough_RewritesForeignToolIDs(t *testing.T) {
	body := []byte(`{"messages":[` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"call.a:b","name":"Read","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call.a:b","content":"ok"}]}]}`)

	out := SanitizeClaudePassthrough(body, false)

	var req struct {
		Messages []struct {
			Content []struct {
				Type      string `json:"type"`
				ID        string `json:"id"`
				ToolUseID string `json:"tool_use_id"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if len(req.Messages) != 2 {
		t.Fatalf("messages = %d, want 2: %s", len(req.Messages), out)
	}
	useID := req.Messages[0].Content[0].ID
	resultID := req.Messages[1].Content[0].ToolUseID
	if !anthropicToolIDPattern.MatchString(useID) {
		t.Errorf("tool_use.id = %q, does not match the Anthropic pattern", useID)
	}
	if useID != resultID {
		t.Errorf("tool_use.id = %q but tool_result.tool_use_id = %q; pairing broke", useID, resultID)
	}
}
