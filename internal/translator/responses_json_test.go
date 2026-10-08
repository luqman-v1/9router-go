package translator

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

// TestChatResponseToResponses pins the non-streaming contract: a Responses
// client that did not ask to stream must still receive one Response object
// carrying the answer, the output items and the usage.
func TestChatResponseToResponses(t *testing.T) {
	body := []byte(`{
		"id": "chatcmpl-9",
		"model": "gpt-x",
		"choices": [{"index": 0, "message": {"role": "assistant", "content": "Hello"},
			"finish_reason": "stop"}],
		"usage": {"prompt_tokens": 12, "completion_tokens": 5}
	}`)

	out, err := ChatResponseToResponses(body)
	if err != nil {
		t.Fatalf("ChatResponseToResponses: %v", err)
	}

	var response struct {
		ID     string `json:"id"`
		Object string `json:"object"`
		Status string `json:"status"`
		Output []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Usage *ResponsesUsage `json:"usage"`
	}
	if err := json.Unmarshal(out, &response); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if response.Object != "response" {
		t.Errorf("object = %q, want %q", response.Object, "response")
	}
	if response.Status != "completed" {
		t.Errorf("status = %q, want %q", response.Status, "completed")
	}
	if len(response.Output) != 1 {
		t.Fatalf("expected 1 output item, got %d: %s", len(response.Output), out)
	}
	if len(response.Output[0].Content) != 1 || response.Output[0].Content[0].Text != "Hello" {
		t.Errorf("answer text not carried into the output item: %s", out)
	}
	if response.Usage == nil || response.Usage.InputTokens != 12 || response.Usage.OutputTokens != 5 {
		t.Errorf("usage not carried over: %s", out)
	}
}

// TestChatResponseToResponses_RejectsUnusableBody keeps the converter honest:
// a body it cannot read must be an error, never a silently empty success.
func TestChatResponseToResponses_RejectsUnusableBody(t *testing.T) {
	if _, err := ChatResponseToResponses([]byte("not json")); err == nil {
		t.Error("expected an error for a non-JSON body")
	}
}

// TestChatResponseToResponses_ToolCallBecomesFunctionCallItem checks a tool turn
// is not silently dropped: the client's next request depends on the function
// call item being present in the response.
func TestChatResponseToResponses_ToolCallBecomesFunctionCallItem(t *testing.T) {
	body := []byte(`{
		"id": "chatcmpl-10",
		"model": "gpt-x",
		"choices": [{"index": 0, "finish_reason": "tool_calls", "message": {"role": "assistant",
			"tool_calls": [{"index": 0, "id": "call_1", "type": "function",
				"function": {"name": "get_weather", "arguments": "{\"city\":\"jakarta\"}"}}]}}]
	}`)

	out, err := ChatResponseToResponses(body)
	if err != nil {
		t.Fatalf("ChatResponseToResponses: %v", err)
	}
	for _, want := range []string{"function_call", "get_weather", "call_1"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("tool call field %q missing from the response: %s", want, out)
		}
	}
}
