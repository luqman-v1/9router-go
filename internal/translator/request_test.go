package translator

import (
	"9router/proxy/internal/thinking"
	"encoding/json/jsontext"
	json "encoding/json/v2"
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
		input    jsontext.Value
		expected string
	}{
		{"empty", nil, ""},
		{"empty string", jsontext.Value(`""`), ""},
		{"string prompt", jsontext.Value(`"You are helpful"`), "You are helpful"},
		{"string with billing header", jsontext.Value(`"x-anthropic-billing-header: k\nBe concise"`), "Be concise"},
		{"array blocks", jsontext.Value(`[{"type":"text","text":"Part A"},{"type":"text","text":"Part B"}]`), "Part A\nPart B"},
		{"array with empty", jsontext.Value(`[{"type":"text","text":""},{"type":"text","text":"Only"}]`), "Only"},
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
	auto := jsontext.Value(`"auto"`)

	tests := []struct {
		name  string
		input *jsontext.Value
		want  string
		check func(t *testing.T, got any)
	}{
		{"nil", nil, "auto", nil},
		{"auto string", &auto, "auto", nil},
		{"any type", rawPtr(`{"type":"any"}`), "required", nil},
		{
			"specific tool", rawPtr(`{"type":"tool","name":"get_weather"}`), "",
			func(t *testing.T, got any) {
				m, ok := got.(map[string]any)
				if !ok {
					t.Fatalf("expected map, got %T", got)
				}
				fn := m["function"].(map[string]any)
				if fn["name"] != "get_weather" {
					t.Errorf("expected name 'get_weather', got %v", fn["name"])
				}
			},
		},
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

func rawPtr(s string) *jsontext.Value {
	r := jsontext.Value(s)
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

func TestEnsureToolCallIDs_MissingIDRepaired(t *testing.T) {
	t.Run("pairs id-less tool message with pending assistant call (PR #4090)", func(t *testing.T) {
		msgs := []OpenAIMessage{
			{
				Role: "assistant",
				ToolCalls: []OpenAIToolCall{
					{ID: "call_abc", Type: "function", Function: OpenAIFunctionCall{Name: "shell", Arguments: "{}"}},
				},
			},
			{
				Role:       "tool",
				ToolCallID: "", // client omitted tool_call_id!
				Content:    "ok",
			},
		}

		got := EnsureToolCallIDs(msgs)
		if len(got) != 2 {
			t.Fatalf("expected 2 messages, got %d", len(got))
		}
		if got[1].ToolCallID != "call_abc" {
			t.Errorf("expected tool_call_id to be repaired to 'call_abc', got %q", got[1].ToolCallID)
		}
	})

	t.Run("mints deterministic id when both assistant and tool lack id", func(t *testing.T) {
		msgs := []OpenAIMessage{
			{
				Role: "assistant",
				ToolCalls: []OpenAIToolCall{
					{ID: "", Type: "function", Function: OpenAIFunctionCall{Name: "shell", Arguments: "{}"}},
				},
			},
			{
				Role:       "tool",
				ToolCallID: "",
				Content:    "ok",
			},
		}

		got := EnsureToolCallIDs(msgs)
		if got[0].ToolCalls[0].ID == "" {
			t.Errorf("expected assistant tool call to have minted id, got empty")
		}
		if got[1].ToolCallID != got[0].ToolCalls[0].ID {
			t.Errorf("expected tool message id %q to match assistant call id %q", got[1].ToolCallID, got[0].ToolCalls[0].ID)
		}
	})
}

// --- convertClaudeMessage tool_result edge ---

func TestConvertClaudeMessage_ToolResultArrayContent(t *testing.T) {
	msg := ClaudeMessage{
		Role:    "user",
		Content: jsontext.Value(`[{"type":"tool_result","tool_use_id":"call_1","content":[{"type":"text","text":"result text"}]}]`),
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
		Role:    "user",
		Content: jsontext.Value(`[{"type":"tool_result","tool_use_id":"call_1","content":{"raw":true}}]`),
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

func TestConvertClaudeMessage_ToolResultImageHoisting(t *testing.T) {
	// Image inside tool_result should emit tool message (text) + follow-up user message with image_url (PR #4083)
	msg := ClaudeMessage{
		Role: "user",
		Content: jsontext.Value(`[
			{
				"type": "tool_result",
				"tool_use_id": "call_screenshot_1",
				"content": [
					{"type": "text", "text": "Screenshot captured"},
					{"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "iVBORw0KGgoAAAANSUhEUgAAAAEAAAAB"}}
				]
			}
		]`),
	}

	results, err := convertClaudeMessage(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 messages (tool + follow-up user image), got %d", len(results))
	}
	// 1. Tool message
	if results[0].Role != "tool" || results[0].ToolCallID != "call_screenshot_1" || results[0].Content != "Screenshot captured" {
		t.Errorf("unexpected tool message: %#v", results[0])
	}
	// 2. Follow-up user message with image
	if results[1].Role != "user" {
		t.Errorf("expected follow-up message to have role 'user', got %s", results[1].Role)
	}
	blocks, ok := results[1].Content.([]OpenAIContentBlock)
	if !ok || len(blocks) != 2 {
		t.Fatalf("expected 2 content blocks in follow-up, got %#v", results[1].Content)
	}
	if blocks[0].Text != "[Image from tool result call_screenshot_1]" {
		t.Errorf("expected tag block, got %s", blocks[0].Text)
	}
	if blocks[1].Type != "image_url" || blocks[1].ImageUrl == nil || !strings.Contains(blocks[1].ImageUrl.URL, "base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAAB") {
		t.Errorf("expected image_url block with base64 data, got %#v", blocks[1])
	}
}

func TestConvertClaudeMessage_EmptyBlocks(t *testing.T) {
	msg := ClaudeMessage{
		Role:    "user",
		Content: jsontext.Value(`[]`),
	}
	results, err := convertClaudeMessage(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 message, got %d", len(results))
	}
}

// --- BudgetToLevel (unified map; port of upstream budgetToLevel) ---

func TestBudgetToLevel(t *testing.T) {
	tests := []struct {
		budget int
		want   string
	}{
		{24576, "high"},
		{30000, "xhigh"},
		{8192, "medium"},
		{10000, "medium"},
		{1024, "low"},
		{100, "minimal"},
		{0, ""},
	}
	for _, tt := range tests {
		if got := thinking.BudgetToLevel(tt.budget); got != tt.want {
			t.Errorf("BudgetToLevel(%d) = %q, want %q", tt.budget, got, tt.want)
		}
	}
}

// --- Thinking block in ConvertClaudeMessage ---

func TestConvertClaudeMessage_ThinkingBlock(t *testing.T) {
	t.Run("thinking block with text and tool_use", func(t *testing.T) {
		msg := ClaudeMessage{
			Role: "assistant",
			Content: jsontext.Value(`[
				{"type":"thinking","thinking":"Let me reason step by step..."},
				{"type":"text","text":"Final answer"},
				{"type":"tool_use","id":"call_1","name":"Bash","input":{"cmd":"ls"}}
			]`),
		}
		results, err := convertClaudeMessage(msg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("expected 1 message, got %d", len(results))
		}
		if results[0].Role != "assistant" {
			t.Errorf("expected role assistant, got %q", results[0].Role)
		}
		if results[0].ReasoningContent != "Let me reason step by step..." {
			t.Errorf("expected reasoning_content %q, got %q", "Let me reason step by step...", results[0].ReasoningContent)
		}
		if results[0].Content != "Final answer" {
			t.Errorf("expected content %q, got %v", "Final answer", results[0].Content)
		}
		if len(results[0].ToolCalls) != 1 {
			t.Fatalf("expected 1 tool call, got %d", len(results[0].ToolCalls))
		}
	})

	t.Run("thinking block without tool_use", func(t *testing.T) {
		msg := ClaudeMessage{
			Role: "assistant",
			Content: jsontext.Value(`[
				{"type":"thinking","thinking":"Thinking..."},
				{"type":"text","text":"Hello"}
			]`),
		}
		results, err := convertClaudeMessage(msg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("expected 1 message, got %d", len(results))
		}
		if results[0].Role != "assistant" {
			t.Errorf("expected role assistant, got %q", results[0].Role)
		}
		if results[0].Content != "Hello" {
			t.Errorf("expected content %q, got %v", "Hello", results[0].Content)
		}
	})

	t.Run("thinking block alone (no text, no tool_use)", func(t *testing.T) {
		msg := ClaudeMessage{
			Role: "assistant",
			Content: jsontext.Value(`[
				{"type":"thinking","thinking":"Just thinking..."}
			]`),
		}
		results, err := convertClaudeMessage(msg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(results) != 0 {
			t.Fatalf("expected 0 messages, got %d", len(results))
		}
	})
}

// --- TranslateClaudeToOpenAI with thinking config ---

func TestTranslateClaudeToOpenAI_ThinkingConfig(t *testing.T) {
	t.Run("thinking config → high reasoning_effort", func(t *testing.T) {
		claudeJSON := []byte(`{
			"model": "claude-3-5-sonnet",
			"messages": [{"role":"user","content":"hello"}],
			"thinking": {"type": "enabled", "budget_tokens": 20000}
		}`)
		openaiJSON, err := TranslateClaudeToOpenAI(claudeJSON)
		if err != nil {
			t.Fatalf("failed to translate: %v", err)
		}
		var oreq map[string]any
		if err := json.Unmarshal(openaiJSON, &oreq); err != nil {
			t.Fatalf("failed to parse: %v", err)
		}
		if oreq["reasoning_effort"] != "high" {
			t.Errorf("expected reasoning_effort 'high', got %v", oreq["reasoning_effort"])
		}
	})

	t.Run("thinking config → medium reasoning_effort", func(t *testing.T) {
		claudeJSON := []byte(`{
			"model": "claude-3-5-sonnet",
			"messages": [{"role":"user","content":"hello"}],
			"thinking": {"type": "enabled", "budget_tokens": 5000}
		}`)
		openaiJSON, err := TranslateClaudeToOpenAI(claudeJSON)
		if err != nil {
			t.Fatalf("failed to translate: %v", err)
		}
		var oreq map[string]any
		if err := json.Unmarshal(openaiJSON, &oreq); err != nil {
			t.Fatalf("failed to parse: %v", err)
		}
		if oreq["reasoning_effort"] != "medium" {
			t.Errorf("expected reasoning_effort 'medium', got %v", oreq["reasoning_effort"])
		}
	})

	t.Run("thinking config → minimal reasoning_effort", func(t *testing.T) {
		claudeJSON := []byte(`{
			"model": "claude-3-5-sonnet",
			"messages": [{"role":"user","content":"hello"}],
			"thinking": {"type": "enabled", "budget_tokens": 100}
		}`)
		openaiJSON, err := TranslateClaudeToOpenAI(claudeJSON)
		if err != nil {
			t.Fatalf("failed to translate: %v", err)
		}
		var oreq map[string]any
		if err := json.Unmarshal(openaiJSON, &oreq); err != nil {
			t.Fatalf("failed to parse: %v", err)
		}
		// Unified BudgetToLevel: 100 → minimal (old 3-bucket map said low).
		if oreq["reasoning_effort"] != "minimal" {
			t.Errorf("expected reasoning_effort 'minimal', got %v", oreq["reasoning_effort"])
		}
	})

	t.Run("no thinking config → no reasoning_effort", func(t *testing.T) {
		claudeJSON := []byte(`{
			"model": "claude-3-5-sonnet",
			"messages": [{"role":"user","content":"hello"}]
		}`)
		openaiJSON, err := TranslateClaudeToOpenAI(claudeJSON)
		if err != nil {
			t.Fatalf("failed to translate: %v", err)
		}
		var oreq map[string]any
		if err := json.Unmarshal(openaiJSON, &oreq); err != nil {
			t.Fatalf("failed to parse: %v", err)
		}
		if _, exists := oreq["reasoning_effort"]; exists {
			t.Errorf("expected no reasoning_effort, got %v", oreq["reasoning_effort"])
		}
	})

	t.Run("thinking disabled → none reasoning_effort", func(t *testing.T) {
		claudeJSON := []byte(`{
			"model": "claude-3-5-sonnet",
			"messages": [{"role":"user","content":"hello"}],
			"thinking": {"type": "disabled"}
		}`)
		openaiJSON, err := TranslateClaudeToOpenAI(claudeJSON)
		if err != nil {
			t.Fatalf("failed to translate: %v", err)
		}
		var oreq map[string]any
		if err := json.Unmarshal(openaiJSON, &oreq); err != nil {
			t.Fatalf("failed to parse: %v", err)
		}
		// "none" is the OpenAI dialect the thinking pipeline reads: the
		// disable must survive the conversion, and a provider that forbids
		// disabling clamps it to minimal afterwards.
		if oreq["reasoning_effort"] != "none" {
			t.Errorf("expected reasoning_effort 'none', got %v", oreq["reasoning_effort"])
		}
	})
}

func TestDefaultClaudeToolType(t *testing.T) {
	t.Run("defaults missing type to custom", func(t *testing.T) {
		input := []byte(`{"tools":[{"name":"get_weather","description":"fetch weather","input_schema":{"type":"object"}}]}`)
		output := DefaultClaudeToolType(input)
		var parsed struct {
			Tools []struct {
				Name string `json:"name"`
				Type string `json:"type"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(output, &parsed); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if len(parsed.Tools) != 1 || parsed.Tools[0].Type != "custom" {
			t.Errorf("expected type='custom', got %+v", parsed.Tools)
		}
	})

	t.Run("preserves existing tool type", func(t *testing.T) {
		input := []byte(`{"tools":[{"name":"computer","type":"computer_use","input_schema":{"type":"object"}}]}`)
		output := DefaultClaudeToolType(input)
		var parsed struct {
			Tools []struct {
				Name string `json:"name"`
				Type string `json:"type"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(output, &parsed); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if len(parsed.Tools) != 1 || parsed.Tools[0].Type != "computer_use" {
			t.Errorf("expected type='computer_use', got %+v", parsed.Tools)
		}
	})
}

func TestTranslateClaudeToOpenAI_AdaptiveEffortNormalization(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantEffort string
	}{
		{
			name:       "adaptive with auto effort normalizes to high",
			input:      `{"model":"claude-3-7-sonnet","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"adaptive"},"output_config":{"effort":"auto"}}`,
			wantEffort: "high",
		},
		{
			name:       "adaptive with xhigh effort normalizes to high",
			input:      `{"model":"claude-3-7-sonnet","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"adaptive"},"output_config":{"effort":"xhigh"}}`,
			wantEffort: "high",
		},
		{
			name:       "adaptive with low effort preserves low",
			input:      `{"model":"claude-3-7-sonnet","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"adaptive"},"output_config":{"effort":"low"}}`,
			wantEffort: "low",
		},
		{
			name:       "output_config alone with auto normalizes to high",
			input:      `{"model":"claude-3-7-sonnet","messages":[{"role":"user","content":"hi"}],"output_config":{"effort":"auto"}}`,
			wantEffort: "high",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := TranslateClaudeToOpenAI([]byte(tt.input))
			if err != nil {
				t.Fatalf("TranslateClaudeToOpenAI failed: %v", err)
			}
			var oreq OpenAIRequest
			if err := json.Unmarshal(out, &oreq); err != nil {
				t.Fatalf("unmarshal failed: %v", err)
			}
			if oreq.ReasoningEffort != tt.wantEffort {
				t.Errorf("got ReasoningEffort %q, want %q", oreq.ReasoningEffort, tt.wantEffort)
			}
		})
	}
}

func TestTranslateClaudeToOpenAI_DocumentBlock(t *testing.T) {
	claudeJSON := []byte(`{
		"model": "claude-opus-4-6-thinking",
		"messages": [{
			"role": "user",
			"content": [
				{"type": "text", "text": "explain this document"},
				{"type": "document", "source": {"type": "base64", "media_type": "application/pdf", "data": "JVBERi0xLjQK..."}}
			]
		}]
	}`)

	openaiJSON, err := TranslateClaudeToOpenAI(claudeJSON)
	if err != nil {
		t.Fatalf("TranslateClaudeToOpenAI failed: %v", err)
	}

	var oreq OpenAIRequest
	if err := json.Unmarshal(openaiJSON, &oreq); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if len(oreq.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(oreq.Messages))
	}

	var blocks []OpenAIContentBlock
	rawContent, err := json.Marshal(oreq.Messages[0].Content)
	if err != nil {
		t.Fatalf("marshal message content: %v", err)
	}
	if err := json.Unmarshal(rawContent, &blocks); err != nil {
		t.Fatalf("unmarshal content blocks: %v", err)
	}

	if len(blocks) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(blocks))
	}
	if blocks[0].Type != "text" || blocks[0].Text != "explain this document" {
		t.Errorf("block 0 mismatch: %#v", blocks[0])
	}
	if blocks[1].Type != "file" || blocks[1].File == nil {
		t.Fatalf("block 1 must be file block, got: %#v", blocks[1])
	}
	expectedURL := "data:application/pdf;base64,JVBERi0xLjQK..."
	if blocks[1].File.FileData != expectedURL {
		t.Errorf("got FileData %q, want %q", blocks[1].File.FileData, expectedURL)
	}

	// Also verify that passing this translated OpenAI format to TranslateOpenAIToGemini creates an InlineData part
	geminiBytes, err := TranslateOpenAIToGemini(openaiJSON)
	if err != nil {
		t.Fatalf("TranslateOpenAIToGemini failed: %v", err)
	}
	var geminiReq GeminiRequest
	if err := json.Unmarshal(geminiBytes, &geminiReq); err != nil {
		t.Fatalf("unmarshal geminiReq: %v", err)
	}
	if len(geminiReq.Contents) != 1 || len(geminiReq.Contents[0].Parts) != 2 {
		t.Fatalf("expected 2 parts in Gemini contents, got: %#v", geminiReq.Contents)
	}
	part2 := geminiReq.Contents[0].Parts[1]
	if part2.InlineData == nil || part2.InlineData.MimeType != "application/pdf" {
		t.Errorf("expected InlineData application/pdf, got: %#v", part2.InlineData)
	}
}
