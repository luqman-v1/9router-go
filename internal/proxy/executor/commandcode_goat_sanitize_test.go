package executor

import (
	json "encoding/json/v2"
	"testing"
)

func TestSanitizeCommandcodeGoatChatBody_NullAndArrayContent(t *testing.T) {
	input := []byte(`{
		"model": "deepseek/deepseek-v4.1-flash",
		"messages": [
			{"role": "system", "content": null},
			{"role": "user", "content": null},
			{"role": "user", "content": []},
			{"role": "assistant", "content": null, "tool_calls": [{"id": "c1", "type": "function", "function": {"name": "test", "arguments": "{}"}}]},
			{"role": "tool", "tool_call_id": "c1", "content": null},
			{"role": "tool", "tool_call_id": "c1", "content": []},
			{"role": "user", "content": [{"type": "tool_result", "content": "stdout_res"}]},
			{"role": "user", "content": [{"type": "text", "text": "normal text"}]}
		]
	}`)

	cleaned := sanitizeCommandcodeGoatChatBody(input)

	var req struct {
		Messages []struct {
			Role       string `json:"role"`
			Content    any    `json:"content"`
			ToolCallID string `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(cleaned, &req); err != nil {
		t.Fatalf("unmarshal cleaned body: %v", err)
	}

	if len(req.Messages) != 8 {
		t.Fatalf("expected 8 messages, got %d", len(req.Messages))
	}

	// 0: system null -> ""
	if req.Messages[0].Content != "" {
		t.Errorf("msg[0] content = %v, want empty string", req.Messages[0].Content)
	}
	// 1: user null -> ""
	if req.Messages[1].Content != "" {
		t.Errorf("msg[1] content = %v, want empty string", req.Messages[1].Content)
	}
	// 2: user [] -> ""
	if req.Messages[2].Content != "" {
		t.Errorf("msg[2] content = %v, want empty string", req.Messages[2].Content)
	}
	// 3: assistant null with tool_calls -> keeps nil
	if req.Messages[3].Content != nil {
		t.Errorf("msg[3] content = %v, want nil", req.Messages[3].Content)
	}
	// 4: tool null -> ""
	if req.Messages[4].Content != "" {
		t.Errorf("msg[4] content = %v, want empty string", req.Messages[4].Content)
	}
	// 5: tool [] -> ""
	if req.Messages[5].Content != "" {
		t.Errorf("msg[5] content = %v, want empty string", req.Messages[5].Content)
	}
	// 6: user with tool_result converted to text
	parts, ok := req.Messages[6].Content.([]any)
	if !ok || len(parts) == 0 {
		t.Fatalf("msg[6] content not non-empty array: %v", req.Messages[6].Content)
	}
	partMap := parts[0].(map[string]any)
	if partMap["type"] != "text" || partMap["text"] != "stdout_res" {
		t.Errorf("msg[6] part[0] = %+v, want text: stdout_res", partMap)
	}
}
