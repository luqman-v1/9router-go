package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestApplyComboStrategy_capacity(t *testing.T) {
	h := NewChatHandler(nil)
	models := []string{"gpt-4", "claude-3", "gemini-pro"}
	got := h.applyComboStrategy("capacity", models)
	if !reflect.DeepEqual(got, models) {
		t.Errorf("capacity: got %v, want %v", got, models)
	}
	if len(got) > 0 && &got[0] == &models[0] {
		t.Error("capacity: returned same backing array, not a copy")
	}
}

func TestApplyComboStrategy_roundRobin(t *testing.T) {
	h := NewChatHandler(nil)
	models := []string{"a", "b", "c"}
	first := h.applyComboStrategy("round-robin", models)
	if !reflect.DeepEqual(first, models) {
		t.Errorf("first call: got %v, want %v", first, models)
	}
	second := h.applyComboStrategy("round-robin", models)
	want := []string{"b", "c", "a"}
	if !reflect.DeepEqual(second, want) {
		t.Errorf("second call: got %v, want %v", second, want)
	}
}

func TestApplyComboStrategy_fallback(t *testing.T) {
	h := NewChatHandler(nil)
	models := []string{"gpt-4", "claude-3", "gemini-pro"}
	got := h.applyComboStrategy("fallback", models)
	if !reflect.DeepEqual(got, models) {
		t.Errorf("fallback: got %v, want %v", got, models)
	}
	if len(got) > 0 && &got[0] == &models[0] {
		t.Error("fallback: returned same backing array, not a copy")
	}
}

func TestApplyComboStrategy_singleModel(t *testing.T) {
	h := NewChatHandler(nil)
	models := []string{"gpt-4"}
	t.Run("capacity", func(t *testing.T) {
		got := h.applyComboStrategy("capacity", models)
		if !reflect.DeepEqual(got, models) {
			t.Errorf("got %v, want %v", got, models)
		}
	})
	t.Run("round-robin", func(t *testing.T) {
		got := h.applyComboStrategy("round-robin", models)
		if !reflect.DeepEqual(got, models) {
			t.Errorf("got %v, want %v", got, models)
		}
	})
	t.Run("fallback", func(t *testing.T) {
		got := h.applyComboStrategy("fallback", models)
		if !reflect.DeepEqual(got, models) {
			t.Errorf("got %v, want %v", got, models)
		}
	})
}

func TestApplyComboStrategy_empty(t *testing.T) {
	h := NewChatHandler(nil)
	t.Run("capacity", func(t *testing.T) {
		got := h.applyComboStrategy("capacity", nil)
		if got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})
	t.Run("round-robin", func(t *testing.T) {
		got := h.applyComboStrategy("round-robin", nil)
		if got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})
}

// ---- Fusion tests ----

func TestFlattenToolHistory_noTools(t *testing.T) {
	input := []any{
		map[string]any{"role": "user", "content": "hello"},
		map[string]any{"role": "assistant", "content": "hi there"},
	}
	got := flattenToolHistory(input)
	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got))
	}
	assertMsgContent(t, got[0], "user", "hello")
	assertMsgContent(t, got[1], "assistant", "hi there")
}

func TestFlattenToolHistory_toolResult(t *testing.T) {
	input := []any{
		map[string]any{"role": "user", "content": "what's the weather?"},
		map[string]any{"role": "assistant", "content": "", "tool_calls": []any{
			map[string]any{
				"id":   "call_1",
				"type": "function",
				"function": map[string]any{
					"name":      "get_weather",
					"arguments": `{"city":"Jakarta"}`,
				},
			},
		}},
		map[string]any{"role": "tool", "content": `{"temp":32}`, "tool_call_id": "call_1"},
	}
	got := flattenToolHistory(input)
	if len(got) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(got))
	}
	// Assistant with tool_calls → prose
	assertMsgContent(t, got[1], "assistant", "[Call tool get_weather({\"city\":\"Jakarta\"})]")
	// Tool result → user prose
	assertMsgContent(t, got[2], "user", "[Tool result]\n{\"temp\":32}")
}

func TestFlattenToolHistory_mixedContent(t *testing.T) {
	input := []any{
		map[string]any{"role": "user", "content": "hello"},
		map[string]any{"role": "assistant", "content": "Let me check", "tool_calls": []any{
			map[string]any{
				"function": map[string]any{"name": "search", "arguments": `{"q":"test"}`},
			},
		}},
		map[string]any{"role": "tool", "content": "results", "tool_call_id": "c1"},
		map[string]any{"role": "assistant", "content": "Here are the results"},
	}
	got := flattenToolHistory(input)
	if len(got) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(got))
	}
	// Second message: content preserved + tool call prose appended
	second := got[1].(map[string]any)
	c, _ := second["content"].(string)
	if !strings.Contains(c, "Let me check") || !strings.Contains(c, "[Call tool search(") {
		t.Errorf("assistant content should include original text + tool prose, got: %s", c)
	}
	// Tool result → user with [Tool result] prefix
	assertMsgContent(t, got[2], "user", "[Tool result]\nresults")
	// Last assistant message unchanged
	assertMsgContent(t, got[3], "assistant", "Here are the results")
}

func TestBuildJudgePrompt(t *testing.T) {
	answers := []fusionAnswer{
		{model: "openai/gpt-4", text: "Answer one"},
		{model: "anthropic/claude-3", text: "Answer two"},
	}
	prompt := buildJudgePrompt(answers)
	if !strings.Contains(prompt, "[Source 1]") {
		t.Error("expected [Source 1] in prompt")
	}
	if !strings.Contains(prompt, "[Source 2]") {
		t.Error("expected [Source 2] in prompt")
	}
	if !strings.Contains(prompt, "Answer one") {
		t.Error("expected first answer text in prompt")
	}
	if !strings.Contains(prompt, "Answer two") {
		t.Error("expected second answer text in prompt")
	}
	if !strings.Contains(prompt, "2 expert models") {
		t.Error("expected count of answers in prompt")
	}
	if strings.Contains(prompt, "openai/gpt-4") {
		t.Error("judge prompt should NOT contain model names - they should be anonymized, but source text leaked")
	}
}

func TestExtractPanelText(t *testing.T) {
	body := `{"choices":[{"message":{"content":"Hello world"}}]}`
	got := extractPanelText([]byte(body))
	if got != "Hello world" {
		t.Errorf("got %q, want %q", got, "Hello world")
	}
}

func TestExtractPanelText_empty(t *testing.T) {
	if got := extractPanelText([]byte(`{"choices":[]}`)); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
	if got := extractPanelText([]byte(`{}`)); got != "" {
		t.Errorf("expected empty for empty object, got %q", got)
	}
	if got := extractPanelText([]byte(`invalid`)); got != "" {
		t.Errorf("expected empty for invalid JSON, got %q", got)
	}
}

func TestAppendUserTurn_messages(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hi"}],"model":"test"}`)
	result := appendUserTurn(body, "judge prompt")
	var parsed map[string]any
	json.Unmarshal(result, &parsed)
	msgs, _ := parsed["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	last := msgs[1].(map[string]any)
	if last["role"] != "user" {
		t.Errorf("expected role user, got %v", last["role"])
	}
	if last["content"] != "judge prompt" {
		t.Errorf("expected 'judge prompt', got %v", last["content"])
	}
}

func TestAppendUserTurn_input(t *testing.T) {
	body := []byte(`{"input":[{"role":"user","content":"hi"}],"model":"test"}`)
	result := appendUserTurn(body, "judge prompt")
	var parsed map[string]any
	json.Unmarshal(result, &parsed)
	input, _ := parsed["input"].([]any)
	if len(input) != 2 {
		t.Fatalf("expected 2 input items, got %d", len(input))
	}
}

func TestAppendUserTurn_noMessages(t *testing.T) {
	body := []byte(`{"model":"test"}`)
	result := appendUserTurn(body, "judge prompt")
	var parsed map[string]any
	json.Unmarshal(result, &parsed)
	msgs, _ := parsed["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message (created), got %d", len(msgs))
	}
}

func TestCollectPanel_allFast(t *testing.T) {
	calls := []func() *fusionResult{
		func() *fusionResult { return &fusionResult{ok: true, body: []byte("a")} },
		func() *fusionResult { return &fusionResult{ok: true, body: []byte("b")} },
		func() *fusionResult { return &fusionResult{ok: true, body: []byte("c")} },
	}
	ft := FusionTuning{MinPanel: 2, StragglerGraceMs: 5000, PanelHardTimeoutMs: 30000}
	results := collectPanel(calls, ft)
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	for i, r := range results {
		if !r.ok {
			t.Errorf("result[%d] should be ok", i)
		}
	}
}

func TestCollectPanel_oneSlow(t *testing.T) {
	calls := []func() *fusionResult{
		func() *fusionResult { return &fusionResult{ok: true, body: []byte("fast")} },
		func() *fusionResult {
			// Simulate slow call — slept in goroutine will still complete before grace
			return &fusionResult{ok: true, body: []byte("slow")}
		},
	}
	ft := FusionTuning{MinPanel: 1, StragglerGraceMs: 100, PanelHardTimeoutMs: 5000}
	results := collectPanel(calls, ft)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}

func TestCollectPanel_empty(t *testing.T) {
	ft := FusionTuning{MinPanel: 2, StragglerGraceMs: 100, PanelHardTimeoutMs: 100}
	if got := collectPanel(nil, ft); got != nil {
		t.Errorf("expected nil for empty, got %v", got)
	}
}

func TestCollectPanel_allFail(t *testing.T) {
	calls := []func() *fusionResult{
		func() *fusionResult { return &fusionResult{ok: false, err: fmt.Errorf("fail")} },
		func() *fusionResult { return &fusionResult{ok: false, err: fmt.Errorf("fail")} },
	}
	ft := FusionTuning{MinPanel: 2, StragglerGraceMs: 100, PanelHardTimeoutMs: 5000}
	results := collectPanel(calls, ft)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	for i, r := range results {
		if r.ok {
			t.Errorf("result[%d] should not be ok", i)
		}
	}
}

func TestResponseBuffer(t *testing.T) {
	buf := &responseBuffer{header: http.Header{}}
	buf.Header().Set("Content-Type", "application/json")
	buf.WriteHeader(200)
	n, err := buf.Write([]byte(`{"hello":"world"}`))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if n != 17 {
		t.Errorf("wrote %d bytes, want 17", n)
	}
	if buf.code != 200 {
		t.Errorf("status %d, want 200", buf.code)
	}
	if buf.body.String() != `{"hello":"world"}` {
		t.Errorf("body %q, want %q", buf.body.String(), `{"hello":"world"}`)
	}
}

// TestResponseBufferImplementsResponseWriter ensures responseBuffer satisfies http.ResponseWriter.
func TestResponseBufferImplementsResponseWriter(t *testing.T) {
	var buf any = &responseBuffer{header: http.Header{}}
	if _, ok := buf.(http.ResponseWriter); !ok {
		t.Error("responseBuffer does not implement http.ResponseWriter")
	}
}

// ---- helpers ----

func assertMsgContent(t *testing.T, msg any, expectedRole, expectedContent string) {
	t.Helper()
	m, ok := msg.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", msg)
	}
	role, _ := m["role"].(string)
	if role != expectedRole {
		t.Errorf("expected role %q, got %q", expectedRole, role)
	}
	content, _ := m["content"].(string)
	if content != expectedContent {
		t.Errorf("expected content %q, got %q", expectedContent, content)
	}
}
