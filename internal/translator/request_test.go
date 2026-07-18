package translator

import (
	"encoding/json"
	"strings"
	"testing"
)

// --- stripAnthropicBillingHeader ---

func TestStripAnthropicBillingHeader(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"x-anthropic-billing-header: some-value\nhello", "hello"},
		{"X-ANTHROPIC-BILLING-HEADER: test\r\nworld", "world"},
		{"hello world", "hello world"},
		{"x-anthropic-billing-header: abc", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := stripAnthropicBillingHeader(tt.input)
		if got != tt.expected {
			t.Errorf("stripAnthropicBillingHeader(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

// --- parseSystemPrompt ---

func TestParseSystemPrompt(t *testing.T) {
	tests := []struct {
		name     string
		input    json.RawMessage
		expected string
	}{
		{"empty", nil, ""},
		{"empty string", json.RawMessage(`""`), ""},
		{"string prompt", json.RawMessage(`"You are helpful"`), "You are helpful"},
		{"string with billing header", json.RawMessage(`"x-anthropic-billing-header: k\nBe concise"`), "Be concise"},
		{"array blocks", json.RawMessage(`[{"type":"text","text":"Part A"},{"type":"text","text":"Part B"}]`), "Part A\nPart B"},
		{"array with empty", json.RawMessage(`[{"type":"text","text":""},{"type":"text","text":"Only"}]`), "Only"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseSystemPrompt(tt.input)
			if got != tt.expected {
				t.Errorf("got %q, want %q", got, tt.expected)
			}
		})
	}
}

// --- systemReminderText ---

func TestSystemReminderText(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", ""},
		{"  ", ""},
		{"Be concise", "<instructions>\nBe concise\n</instructions>"},
		{"multi\nline", "<instructions>\nmulti\nline\n</instructions>"},
	}
	for _, tt := range tests {
		got := systemReminderText(tt.input)
		if got != tt.expected {
			t.Errorf("systemReminderText(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

// --- collapseTextParts ---

func TestCollapseTextParts(t *testing.T) {
	// Single text parts → string
	single := []OpenAIContentBlock{{Type: "text", Text: "hello"}}
	got := collapseTextParts(single)
	if s, ok := got.(string); !ok || s != "hello" {
		t.Errorf("expected string 'hello', got %#v", got)
	}

	// Multiple parts → array
	multi := []OpenAIContentBlock{
		{Type: "text", Text: "hello"},
		{Type: "image_url", ImageUrl: &OpenAIImageUrl{URL: "data:test"}},
	}
	got = collapseTextParts(multi)
	if arr, ok := got.([]OpenAIContentBlock); !ok || len(arr) != 2 {
		t.Errorf("expected array of 2, got %#v", got)
	}

	// Single non-text → array
	onlyImg := []OpenAIContentBlock{{Type: "image_url", ImageUrl: &OpenAIImageUrl{URL: "data:test"}}}
	got = collapseTextParts(onlyImg)
	if _, ok := got.([]OpenAIContentBlock); !ok {
		t.Errorf("expected array for non-text single block, got %#v", got)
	}
}

// --- convertToolChoice ---

func TestConvertToolChoice(t *testing.T) {
	auto := json.RawMessage(`"auto"`)

	tests := []struct {
		name  string
		input *json.RawMessage
		want  string
		check func(t *testing.T, got any)
	}{
		{"nil", nil, "auto", nil},
		{"auto string", &auto, "auto", nil},
		{"any type", rawPtr(`{"type":"any"}`), "required", nil},
		{"specific tool", rawPtr(`{"type":"tool","name":"get_weather"}`), "",
			func(t *testing.T, got any) {
				m, ok := got.(map[string]any)
				if !ok {
					t.Fatalf("expected map, got %T", got)
				}
				fn := m["function"].(map[string]any)
				if fn["name"] != "get_weather" {
					t.Errorf("expected name 'get_weather', got %v", fn["name"])
				}
			}},
		{"unknown type", rawPtr(`{"type":"unknown"}`), "auto", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := convertToolChoice(tt.input)
			if tt.check != nil {
				tt.check(t, got)
			} else if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func rawPtr(s string) *json.RawMessage {
	r := json.RawMessage(s)
	return &r
}

// --- fixMissingToolResponsesOpenAI edge cases ---

func TestFixMissingToolResponsesOpenAI(t *testing.T) {
	t.Run("no tool calls — unchanged", func(t *testing.T) {
		msgs := []OpenAIMessage{{Role: "user", Content: "hello"}}
		got := fixMissingToolResponsesOpenAI(msgs)
		if len(got) != 1 {
			t.Errorf("expected 1 message, got %d", len(got))
		}
	})

	t.Run("all tool calls responded", func(t *testing.T) {
		msgs := []OpenAIMessage{
			{Role: "assistant", ToolCalls: []OpenAIToolCall{{ID: "call_1"}}},
			{Role: "tool", ToolCallID: "call_1", Content: "result"},
		}
		got := fixMissingToolResponsesOpenAI(msgs)
		if len(got) != 2 {
			t.Errorf("expected 2 messages, got %d", len(got))
		}
	})

	t.Run("multiple missing tool calls get fixed", func(t *testing.T) {
		msgs := []OpenAIMessage{
			{Role: "user", Content: "hi"},
			{Role: "assistant", ToolCalls: []OpenAIToolCall{
				{ID: "call_1"},
				{ID: "call_2"},
			}},
		}
		got := fixMissingToolResponsesOpenAI(msgs)
		if len(got) != 4 {
			t.Fatalf("expected 4 messages (user + assistant + 2 tool), got %d", len(got))
		}
		// Both missing tool calls should be inserted
		if got[2].Role != "tool" || got[2].Content != "[No response received]" {
			t.Errorf("expected inserted tool response, got %#v", got[2])
		}
	})
}

// --- convertClaudeMessage tool_result edge ---

func TestConvertClaudeMessage_ToolResultArrayContent(t *testing.T) {
	msg := ClaudeMessage{
		Role: "user",
		Content: json.RawMessage(`[{"type":"tool_result","tool_use_id":"call_1","content":[{"type":"text","text":"result text"}]}]`),
	}
	results, err := convertClaudeMessage(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 message, got %d", len(results))
	}
	if results[0].Content != "result text" {
		t.Errorf("expected content 'result text', got %v", results[0].Content)
	}
}

func TestConvertClaudeMessage_ToolResultRawContent(t *testing.T) {
	msg := ClaudeMessage{
		Role: "user",
		Content: json.RawMessage(`[{"type":"tool_result","tool_use_id":"call_1","content":{"raw":true}}]`),
	}
	results, err := convertClaudeMessage(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 message, got %d", len(results))
	}
	if !strings.Contains(results[0].Content.(string), `{"raw":true}`) {
		t.Errorf("expected raw JSON content, got %v", results[0].Content)
	}
}

func TestConvertClaudeMessage_EmptyBlocks(t *testing.T) {
	msg := ClaudeMessage{
		Role:    "user",
		Content: json.RawMessage(`[]`),
	}
	results, err := convertClaudeMessage(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 message, got %d", len(results))
	}
}
