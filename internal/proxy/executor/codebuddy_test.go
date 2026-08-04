package executor

import (
	"encoding/json"
	"testing"
)

func TestTransformCodebuddyBody_ForceStream(t *testing.T) {
	// non-stream request should be forced to stream=true
	input := `{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}],"stream":false}`
	out, err := transformCodebuddyBody([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if m["stream"] != true {
		t.Errorf("expected stream=true, got %v", m["stream"])
	}
}

func TestTransformCodebuddyBody_StreamAlreadyTrue(t *testing.T) {
	input := `{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}],"stream":true}`
	out, err := transformCodebuddyBody([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if m["stream"] != true {
		t.Errorf("expected stream=true, got %v", m["stream"])
	}
}

func TestTransformCodebuddyBody_ReasoningEffortNone(t *testing.T) {
	// "none" and "off" should be stripped
	for _, eff := range []string{"none", "off"} {
		input := `{"model":"glm-5.2","messages":[],"reasoning_effort":"` + eff + `"}`
		out, err := transformCodebuddyBody([]byte(input))
		if err != nil {
			t.Fatalf("unexpected error for %q: %v", eff, err)
		}
		var m map[string]interface{}
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("unmarshal output: %v", err)
		}
		if _, exists := m["reasoning_effort"]; exists {
			t.Errorf("reasoning_effort=%q should be removed, but still present", eff)
		}
		if _, exists := m["reasoning_summary"]; exists {
			t.Errorf("reasoning_summary should NOT be set when effort=%q", eff)
		}
	}
}

func TestTransformCodebuddyBody_ReasoningEffortMedium(t *testing.T) {
	// Active reasoning_effort should inject reasoning_summary="auto"
	input := `{"model":"glm-5.2","messages":[],"reasoning_effort":"medium"}`
	out, err := transformCodebuddyBody([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if m["reasoning_effort"] != "medium" {
		t.Errorf("reasoning_effort should stay %q, got %v", "medium", m["reasoning_effort"])
	}
	if m["reasoning_summary"] != "auto" {
		t.Errorf("expected reasoning_summary=%q, got %v", "auto", m["reasoning_summary"])
	}
}

func TestTransformCodebuddyBody_NoReasoningEffort(t *testing.T) {
	// When reasoning_effort is not set, neither should reasoning_summary
	input := `{"model":"glm-5.2","messages":[{"role":"user","content":"hello"}]}`
	out, err := transformCodebuddyBody([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if _, exists := m["reasoning_effort"]; exists {
		t.Error("reasoning_effort should not be present")
	}
	if _, exists := m["reasoning_summary"]; exists {
		t.Error("reasoning_summary should not be present when no effort set")
	}
}

func TestSSEToOpenAIJSON_MergesChunks(t *testing.T) {
	sse := "data: {\"id\":\"chatcmpl-1\",\"created\":123,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\" world\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"thinking\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":3}}\n\n" +
		"data: [DONE]\n\n"
	out, ok := sseToOpenAIJSON([]byte(sse))
	if !ok {
		t.Fatal("expected SSE detected")
	}
	var resp struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("output not valid JSON: %v\n%s", err, out)
	}
	if resp.ID != "chatcmpl-1" || resp.Model != "glm-5.2" {
		t.Errorf("id/model mismatch: %+v", resp)
	}
	if resp.Choices[0].Message.Content != "Hello world" {
		t.Errorf("content not merged, got %q", resp.Choices[0].Message.Content)
	}
	if resp.Choices[0].Message.ReasoningContent != "thinking" {
		t.Errorf("reasoning_content missing, got %q", resp.Choices[0].Message.ReasoningContent)
	}
	if resp.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason missing, got %q", resp.Choices[0].FinishReason)
	}
	if resp.Usage.PromptTokens != 5 {
		t.Errorf("usage not carried, got %+v", resp.Usage)
	}
}

func TestSSEToOpenAIJSON_MergesToolCalls(t *testing.T) {
	sse := "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"get_\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"weather\",\"arguments\":\"{\\\"city\\\":\\\"KL\\\"}\"}}]}}]}\n\n"
	out, ok := sseToOpenAIJSON([]byte(sse))
	if !ok {
		t.Fatal("expected SSE detected")
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content   any `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("output not valid JSON: %v\n%s", err, out)
	}
	if resp.Choices[0].Message.Content != nil {
		t.Errorf("content should be null when tool_calls present, got %v", resp.Choices[0].Message.Content)
	}
	tcs := resp.Choices[0].Message.ToolCalls
	if len(tcs) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(tcs))
	}
	if tcs[0].ID != "call_1" {
		t.Errorf("tool call id not carried, got %q", tcs[0].ID)
	}
	if tcs[0].Function.Name != "get_weather" {
		t.Errorf("tool name not merged, got %q", tcs[0].Function.Name)
	}
	if tcs[0].Function.Arguments != `{"city":"KL"}` {
		t.Errorf("tool arguments not merged, got %q", tcs[0].Function.Arguments)
	}
}

func TestSSEToOpenAIJSON_NotSSE(t *testing.T) {
	// A plain JSON error body must pass through untouched.
	if _, ok := sseToOpenAIJSON([]byte(`{"error":{"message":"boom","code":400}}`)); ok {
		t.Error("plain JSON should not be treated as SSE")
	}
}
