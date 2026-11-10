package chat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/resilience"
)

func TestWriteStalledToolLoopOpenAI_NonStreaming(t *testing.T) {
	rec := httptest.NewRecorder()
	intervention := &resilience.StalledLoopIntervention{
		ToolName:  "bash",
		CallCount: 3,
		Message:   "stalled tool loop detected",
	}

	writeStalledToolLoopOpenAI(rec, intervention, "gpt-4o", false)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if rec.Header().Get("X-9Router-Resilience") != "stalled-tool-loop-intervention" {
		t.Errorf("expected X-9Router-Resilience header")
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
		t.Errorf("expected application/json Content-Type, got %s", rec.Header().Get("Content-Type"))
	}

	var resp struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Model   string `json:"model"`
		Choices []struct {
			Index   int `json:"index"`
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse JSON response: %v", err)
	}

	if resp.Object != "chat.completion" {
		t.Errorf("expected object 'chat.completion', got %s", resp.Object)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(resp.Choices))
	}
	if resp.Choices[0].Message.Role != "assistant" {
		t.Errorf("expected role 'assistant', got %s", resp.Choices[0].Message.Role)
	}
	if resp.Choices[0].FinishReason != "stop" {
		t.Errorf("expected finish_reason 'stop', got %s", resp.Choices[0].FinishReason)
	}
	if !strings.Contains(resp.Choices[0].Message.Content, "System Intervention [StalledToolLoopDetected]") {
		t.Errorf("expected intervention text in content, got %s", resp.Choices[0].Message.Content)
	}
	if !strings.Contains(resp.Choices[0].Message.Content, `tool "bash" 3 consecutive times`) {
		t.Errorf("expected tool name and count in content, got %s", resp.Choices[0].Message.Content)
	}
}

func TestWriteStalledToolLoopOpenAI_Streaming(t *testing.T) {
	rec := httptest.NewRecorder()
	intervention := &resilience.StalledLoopIntervention{
		ToolName:  "fetch_page",
		CallCount: 4,
		Message:   "stalled tool loop detected",
	}

	writeStalledToolLoopOpenAI(rec, intervention, "gpt-4o", true)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Errorf("expected text/event-stream Content-Type, got %s", rec.Header().Get("Content-Type"))
	}
	if rec.Header().Get("X-9Router-Resilience") != "stalled-tool-loop-intervention" {
		t.Errorf("expected X-9Router-Resilience header")
	}

	bodyStr := rec.Body.String()
	if !strings.Contains(bodyStr, "data: [DONE]") {
		t.Errorf("expected stream to end with [DONE]")
	}
	if !strings.Contains(bodyStr, "chat.completion.chunk") {
		t.Errorf("expected chat.completion.chunk in SSE stream")
	}
	if !strings.Contains(bodyStr, "System Intervention [StalledToolLoopDetected]") {
		t.Errorf("expected intervention message in SSE stream")
	}
}

func TestWriteStalledToolLoopClaude_NonStreaming(t *testing.T) {
	rec := httptest.NewRecorder()
	intervention := &resilience.StalledLoopIntervention{
		ToolName:  "view_file",
		CallCount: 3,
		Message:   "stalled tool loop detected",
	}

	writeStalledToolLoopClaude(rec, intervention, "claude-3-7-sonnet", false)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if rec.Header().Get("X-9Router-Resilience") != "stalled-tool-loop-intervention" {
		t.Errorf("expected X-9Router-Resilience header")
	}

	var resp struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Role    string `json:"role"`
		Model   string `json:"model"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse Claude message response: %v", err)
	}

	if resp.Type != "message" || resp.Role != "assistant" {
		t.Errorf("expected message/assistant, got %s/%s", resp.Type, resp.Role)
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("expected stop_reason 'end_turn', got %s", resp.StopReason)
	}
	if len(resp.Content) != 1 || resp.Content[0].Type != "text" {
		t.Fatalf("expected 1 text content block, got %+v", resp.Content)
	}
	if !strings.Contains(resp.Content[0].Text, "System Intervention [StalledToolLoopDetected]") {
		t.Errorf("expected intervention text in content block, got %s", resp.Content[0].Text)
	}
}

func TestWriteStalledToolLoopClaude_Streaming(t *testing.T) {
	rec := httptest.NewRecorder()
	intervention := &resilience.StalledLoopIntervention{
		ToolName:  "view_file",
		CallCount: 3,
		Message:   "stalled tool loop detected",
	}

	writeStalledToolLoopClaude(rec, intervention, "claude-3-7-sonnet", true)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Errorf("expected text/event-stream Content-Type, got %s", rec.Header().Get("Content-Type"))
	}
	if rec.Header().Get("X-9Router-Resilience") != "stalled-tool-loop-intervention" {
		t.Errorf("expected X-9Router-Resilience header")
	}

	bodyStr := rec.Body.String()
	for _, expectedEvent := range []string{
		"event: message_start",
		"event: content_block_start",
		"event: content_block_delta",
		"event: content_block_stop",
		"event: message_delta",
		"event: message_stop",
	} {
		if !strings.Contains(bodyStr, expectedEvent) {
			t.Errorf("expected stream to contain %q", expectedEvent)
		}
	}
	if !strings.Contains(bodyStr, "System Intervention [StalledToolLoopDetected]") {
		t.Errorf("expected intervention text in streaming events")
	}
}

func TestCheckStalledToolLoop(t *testing.T) {
	h := &ChatHandler{}

	t.Run("OpenAI loop triggers intervention", func(t *testing.T) {
		rec := httptest.NewRecorder()
		messages := []map[string]any{
			{
				"role": "assistant",
				"tool_calls": []any{
					map[string]any{
						"function": map[string]any{
							"name":      "bash",
							"arguments": `{"command":"pwd"}`,
						},
					},
				},
			},
			{"role": "tool", "content": "/app"},
			{
				"role": "assistant",
				"tool_calls": []any{
					map[string]any{
						"function": map[string]any{
							"name":      "bash",
							"arguments": `{"command":"pwd"}`,
						},
					},
				},
			},
			{"role": "tool", "content": "/app"},
			{
				"role": "assistant",
				"tool_calls": []any{
					map[string]any{
						"function": map[string]any{
							"name":      "bash",
							"arguments": `{"command":"pwd"}`,
						},
					},
				},
			},
		}

		handled := h.CheckStalledToolLoopOpenAI(rec, messages, "gpt-4o", false)
		if !handled {
			t.Fatalf("expected CheckStalledToolLoopOpenAI to return true")
		}
		if rec.Code != http.StatusOK {
			t.Errorf("expected 200 OK, got %d", rec.Code)
		}
	})

	t.Run("OpenAI non-loop passes through", func(t *testing.T) {
		rec := httptest.NewRecorder()
		messages := []map[string]any{
			{
				"role": "assistant",
				"tool_calls": []any{
					map[string]any{
						"function": map[string]any{
							"name":      "bash",
							"arguments": `{"command":"pwd"}`,
						},
					},
				},
			},
			{"role": "tool", "content": "/app"},
			{
				"role": "assistant",
				"tool_calls": []any{
					map[string]any{
						"function": map[string]any{
							"name":      "bash",
							"arguments": `{"command":"ls"}`,
						},
					},
				},
			},
		}

		handled := h.CheckStalledToolLoopOpenAI(rec, messages, "gpt-4o", false)
		if handled {
			t.Fatalf("expected CheckStalledToolLoopOpenAI to return false on non-loop")
		}
	})

	t.Run("Claude loop triggers intervention", func(t *testing.T) {
		rec := httptest.NewRecorder()
		messages := []map[string]any{
			{
				"role": "assistant",
				"content": []any{
					map[string]any{
						"type":  "tool_use",
						"name":  "read_file",
						"input": `{"path":"a.go"}`,
					},
				},
			},
			{
				"role": "user",
				"content": []any{
					map[string]any{
						"type":    "tool_result",
						"content": "not found",
					},
				},
			},
			{
				"role": "assistant",
				"content": []any{
					map[string]any{
						"type":  "tool_use",
						"name":  "read_file",
						"input": `{"path":"a.go"}`,
					},
				},
			},
			{
				"role": "user",
				"content": []any{
					map[string]any{
						"type":    "tool_result",
						"content": "not found",
					},
				},
			},
			{
				"role": "assistant",
				"content": []any{
					map[string]any{
						"type":  "tool_use",
						"name":  "read_file",
						"input": `{"path":"a.go"}`,
					},
				},
			},
		}

		handled := h.CheckStalledToolLoopClaude(rec, messages, "claude-3-7-sonnet", false)
		if !handled {
			t.Fatalf("expected CheckStalledToolLoopClaude to return true")
		}
		if rec.Code != http.StatusOK {
			t.Errorf("expected 200 OK, got %d", rec.Code)
		}
	})
}
