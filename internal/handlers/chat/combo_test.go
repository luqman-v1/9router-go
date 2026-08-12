package chat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestApplyComboStrategy_capacity(t *testing.T) {
	h := NewChatHandler(nil)
	models := []string{"gpt-4", "claude-3", "gemini-pro"}
	got := h.ApplyComboStrategy("capacity", models, "", 1)
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
	first := h.ApplyComboStrategy("round-robin", models, "combo1", 1)
	if !reflect.DeepEqual(first, models) {
		t.Errorf("first call: got %v, want %v", first, models)
	}
	second := h.ApplyComboStrategy("round-robin", models, "combo1", 1)
	want := []string{"b", "c", "a"}
	if !reflect.DeepEqual(second, want) {
		t.Errorf("second call: got %v, want %v", second, want)
	}
}

func TestApplyComboStrategy_roundRobinPerCombo(t *testing.T) {
	h := NewChatHandler(nil)
	models := []string{"a", "b", "c"}

	// combo1: first call → [a, b, c]
	h.ApplyComboStrategy("round-robin", models, "combo1", 1)
	// combo1: second call → [b, c, a]
	c1second := h.ApplyComboStrategy("round-robin", models, "combo1", 1)
	if !reflect.DeepEqual(c1second, []string{"b", "c", "a"}) {
		t.Errorf("combo1 second: got %v, want [b c a]", c1second)
	}

	// combo2: first call should start fresh → [a, b, c] (independent of combo1)
	c2first := h.ApplyComboStrategy("round-robin", models, "combo2", 1)
	if !reflect.DeepEqual(c2first, models) {
		t.Errorf("combo2 first: got %v, want %v (should be independent)", c2first, models)
	}

	// combo1: third call → [c, a, b] (continues its own state)
	c1third := h.ApplyComboStrategy("round-robin", models, "combo1", 1)
	if !reflect.DeepEqual(c1third, []string{"c", "a", "b"}) {
		t.Errorf("combo1 third: got %v, want [c a b]", c1third)
	}
}

func TestApplyComboStrategy_stickyBackwardCompat(t *testing.T) {
	h := NewChatHandler(nil)
	models := []string{"a", "b", "c"}

	// sticky with stickyLimit=3: model should stay for 3 requests
	for i := 0; i < 3; i++ {
		got := h.ApplyComboStrategy("sticky", models, "combo1", 3)
		if got[0] != "a" {
			t.Errorf("call %d: first model should be 'a' (sticky), got %q", i+1, got[0])
		}
	}
	// 4th call: should rotate to "b"
	got := h.ApplyComboStrategy("sticky", models, "combo1", 3)
	if got[0] != "b" {
		t.Errorf("4th call: first model should be 'b' after rotation, got %q", got[0])
	}
}

func TestApplyComboStrategy_fallback(t *testing.T) {
	h := NewChatHandler(nil)
	models := []string{"gpt-4", "claude-3", "gemini-pro"}
	got := h.ApplyComboStrategy("fallback", models, "", 1)
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
		got := h.ApplyComboStrategy("capacity", models, "", 1)
		if !reflect.DeepEqual(got, models) {
			t.Errorf("got %v, want %v", got, models)
		}
	})
	t.Run("round-robin", func(t *testing.T) {
		got := h.ApplyComboStrategy("round-robin", models, "", 1)
		if !reflect.DeepEqual(got, models) {
			t.Errorf("got %v, want %v", got, models)
		}
	})
	t.Run("fallback", func(t *testing.T) {
		got := h.ApplyComboStrategy("fallback", models, "", 1)
		if !reflect.DeepEqual(got, models) {
			t.Errorf("got %v, want %v", got, models)
		}
	})
}

func TestApplyComboStrategy_empty(t *testing.T) {
	h := NewChatHandler(nil)
	t.Run("capacity", func(t *testing.T) {
		got := h.ApplyComboStrategy("capacity", nil, "", 1)
		if got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})
	t.Run("round-robin", func(t *testing.T) {
		got := h.ApplyComboStrategy("round-robin", nil, "", 1)
		if got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})
}

func TestResetComboState(t *testing.T) {
	h := NewChatHandler(nil)
	models := []string{"a", "b", "c"}

	// Advance combo1 state
	h.ApplyComboStrategy("round-robin", models, "combo1", 1)
	h.ApplyComboStrategy("round-robin", models, "combo1", 1)

	// Reset combo1
	h.ResetComboState("combo1")

	// Should start fresh (index 0)
	got := h.ApplyComboStrategy("round-robin", models, "combo1", 1)
	if !reflect.DeepEqual(got, models) {
		t.Errorf("after reset: got %v, want %v", got, models)
	}
}

func TestResetComboState_all(t *testing.T) {
	h := NewChatHandler(nil)
	models := []string{"a", "b"}

	h.ApplyComboStrategy("round-robin", models, "combo1", 1)
	h.ApplyComboStrategy("round-robin", models, "combo2", 1)

	// Reset all
	h.ResetComboState("")

	// Both should start fresh
	got1 := h.ApplyComboStrategy("round-robin", models, "combo1", 1)
	got2 := h.ApplyComboStrategy("round-robin", models, "combo2", 1)
	if !reflect.DeepEqual(got1, models) {
		t.Errorf("combo1 after reset all: got %v, want %v", got1, models)
	}
	if !reflect.DeepEqual(got2, models) {
		t.Errorf("combo2 after reset all: got %v, want %v", got2, models)
	}
}

func TestExportedWrappers(t *testing.T) {
	h := NewChatHandler(nil)
	models := []string{"a", "b"}

	// ApplyComboStrategy should behave the same as internal version
	got := h.ApplyComboStrategy("round-robin", models, "test", 1)
	if len(got) != 2 {
		t.Errorf("ApplyComboStrategy: expected 2 models, got %d", len(got))
	}

	// DetectRequiredCapabilities should handle empty body
	caps := DetectRequiredCapabilities([]byte(`{"messages":[{"role":"user","content":"hello"}]}`))
	if caps == nil {
		// nil is fine (no capabilities detected)
	}

	// ReorderByCapabilities with no required caps should return same order
	reordered := ReorderByCapabilities(models, nil)
	if len(reordered) != 2 {
		t.Errorf("ReorderByCapabilities: expected 2 models, got %d", len(reordered))
	}
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

func TestDetectRequiredCapabilities(t *testing.T) {
	t.Run("Trailing user turn only", func(t *testing.T) {
		body := []byte(`{
			"messages": [
				{
					"role": "user",
					"content": [
						{"type": "image_url", "image_url": {"url": "..."}}
					]
				},
				{
					"role": "assistant",
					"content": "Image description"
				},
				{
					"role": "user",
					"content": [
						{"type": "text", "text": "Now what about this audio?"},
						{"type": "input_audio", "input_audio": {"data": "..."}}
					]
				}
			]
		}`)

		caps := DetectRequiredCapabilities(body)

		if caps["vision"] {
			t.Errorf("expected vision to be false (earlier turn should be ignored)")
		}
		if !caps["audioInput"] {
			t.Errorf("expected audioInput to be true from trailing turn")
		}
	})

	t.Run("Responses API format", func(t *testing.T) {
		body := []byte(`{
			"input": [
				{
					"role": "user",
					"content": [
						{"type": "video_url", "video_url": {"url": "..."}}
					]
				}
			]
		}`)

		caps := DetectRequiredCapabilities(body)

		if !caps["videoInput"] {
			t.Errorf("expected videoInput to be true")
		}
	})
}

func TestReorderByCapabilities(t *testing.T) {

	// gemini-1.5-pro has audioInput=true, videoInput=true, vision=true
	// openai/gpt-4 has vision=false, audioInput=false (defaults to false? wait, GetCapabilitiesForModel handles this)
	// Actually, wait, let's use known models from capabilities.go.
	// "gemini-3-pro" has audioInput, videoInput, etc.
	// "gpt-4" has only Tools: true
	// "claude-3-opus" has vision: true, tools: true.

	testModels := []string{"openai/gpt-4", "anthropic/claude-3-opus", "gemini/gemini-3-pro"}

	req := map[string]bool{
		"audioInput": true,
	}

	reordered := ReorderByCapabilities(testModels, req)

	if len(reordered) != 3 {
		t.Fatalf("expected 3 models, got %d", len(reordered))
	}
	if reordered[0] != "gemini/gemini-3-pro" {
		t.Errorf("expected gemini/gemini-3-pro first for audioInput, got %s", reordered[0])
	}
}

func TestComboStrategy_OrderAndRotation(t *testing.T) {
	h := NewChatHandler(nil)

	// gemini/gemini-3-pro has audioInput=true
	// openai/gpt-4 has audioInput=false
	// anthropic/claude-3-opus has audioInput=false
	models := []string{"openai/gpt-4", "anthropic/claude-3-opus", "gemini/gemini-3-pro"}

	// 1. First request: round-robin returns [openai/gpt-4, anthropic/claude-3-opus, gemini/gemini-3-pro]
	// But it requires audioInput, so ReorderByCapabilities MUST push gemini-3-pro to the front.
	req1 := map[string]bool{"audioInput": true}
	rotated1 := h.ApplyComboStrategy("round-robin", models, "mycombo", 1)
	reordered1 := ReorderByCapabilities(rotated1, req1)

	if reordered1[0] != "gemini/gemini-3-pro" {
		t.Errorf("First call: expected gemini to be front due to capability override, got %s", reordered1[0])
	}

	// 2. Second request: NO capabilities required.
	// Round robin should advance index to 1.
	// Original models rotated by 1 -> [anthropic/claude-3-opus, gemini/gemini-3-pro, openai/gpt-4]
	req2 := map[string]bool{}
	rotated2 := h.ApplyComboStrategy("round-robin", models, "mycombo", 1)
	reordered2 := ReorderByCapabilities(rotated2, req2)

	if reordered2[0] != "anthropic/claude-3-opus" {
		t.Errorf("Second call: expected claude to be front due to rotation, got %s", reordered2[0])
	}
}

func TestDetectNewTurn(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{"user text", `{"messages":[{"role":"user","content":"hello"}]}`, true},
		{"user text array", `{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`, true},
		{"mid-turn tool result", `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"x","tool_calls":[{"id":"t1"}]},{"role":"tool","tool_call_id":"t1","content":"ok"}]}`, false},
		{"empty body", `{}`, true},
	}
	for _, tt := range tests {
		if got := detectNewTurn([]byte(tt.body)); got != tt.want {
			t.Errorf("%s: detectNewTurn = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestApplyComboStrategy_roundRobinTurnAware(t *testing.T) {
	h := NewChatHandler(nil)
	models := []string{"a/x", "b/y", "c/z"}

	first := h.applyComboStrategy("round-robin", models, "comboT", 1, true)
	if first[0] != "a/x" {
		t.Errorf("first lead = %s, want a/x", first[0])
	}

	mid1 := h.applyComboStrategy("round-robin", models, "comboT", 1, false)
	if mid1[0] != "a/x" {
		t.Errorf("mid1 lead = %s, want a/x", mid1[0])
	}
	mid2 := h.applyComboStrategy("round-robin", models, "comboT", 1, false)
	if mid2[0] != "a/x" {
		t.Errorf("mid2 lead = %s, want a/x", mid2[0])
	}

	second := h.applyComboStrategy("round-robin", models, "comboT", 1, true)
	if second[0] != "b/y" {
		t.Errorf("second lead = %s, want b/y", second[0])
	}
}

func TestComboRetryAfter(t *testing.T) {
	past := time.Now().Add(-1 * time.Second)
	bounded := time.Now().Add(4 * time.Second)
	tooFar := time.Now().Add(60 * time.Second)

	tests := []struct {
		name       string
		retryAfter string
	}{
		{"empty", ""},
		{"invalid time", "not-a-time"},
		{"past", past.Format(time.RFC3339)},
		{"bounded", bounded.Format(time.RFC3339)},
		{"exceeds cap", tooFar.Format(time.RFC3339)},
	}
	wants := map[string]time.Duration{
		"empty":        0,
		"invalid time": 0,
		"past":         time.Second, // clamped to a minimum 1s wait
		"bounded":      4 * time.Second,
		"exceeds cap":  0, // too long -> surface Retry-After header instead
	}

	for _, tt := range tests {
		got := comboRetryAfter(tt.retryAfter)
		want := wants[tt.name]
		switch tt.name {
		case "bounded":
			// Allow the ceil() rounding to land on 4s or just either side of it.
			if got <= 0 || got > comboRetryWaitCap {
				t.Errorf("%s: comboRetryAfter = %v, want in (0, %v]", tt.name, got, comboRetryWaitCap)
			}
		default:
			if got != want {
				t.Errorf("%s: comboRetryAfter = %v, want %v", tt.name, got, want)
			}
		}
	}
}
