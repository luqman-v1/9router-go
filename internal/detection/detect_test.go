package detection

import "testing"

func TestDetectLoops_ConsecutiveTool(t *testing.T) {
	tools := []ToolCallItem{
		{Name: "Bash"},
		{Name: "Bash"},
		{Name: "Bash"},
	}
	findings := DetectLoops(tools, "")
	if len(findings) == 0 {
		t.Fatalf("expected finding for 3 consecutive Bash calls, got none")
	}
	if findings[0].Rule != "consecutive_tool" || findings[0].Tool != "Bash" {
		t.Errorf("unexpected finding: %+v", findings[0])
	}
	if findings[0].Severity != SeverityWarning {
		t.Errorf("expected warning severity for 3 repeats, got %s", findings[0].Severity)
	}

	// 5 or more repeats should be critical
	tools5 := []ToolCallItem{
		{Name: "Bash"}, {Name: "Bash"}, {Name: "Bash"}, {Name: "Bash"}, {Name: "Bash"},
	}
	findings5 := DetectLoops(tools5, "")
	if len(findings5) == 0 || findings5[0].Severity != SeverityCritical {
		t.Errorf("expected critical severity for 5 repeats, got %+v", findings5)
	}
}

func TestDetectLoops_CyclePattern(t *testing.T) {
	// 2-tool cycle: [Read, Edit] repeated
	tools := []ToolCallItem{
		{Name: "Read"}, {Name: "Edit"},
		{Name: "Read"}, {Name: "Edit"},
	}
	findings := DetectLoops(tools, "")
	if len(findings) == 0 {
		t.Fatalf("expected finding for [Read, Edit] cycle, got none")
	}
	if findings[0].Rule != "cycle_pattern" {
		t.Errorf("expected cycle_pattern rule, got %s", findings[0].Rule)
	}

	// 3-tool cycle: [Read, Grep, Edit] repeated
	tools3 := []ToolCallItem{
		{Name: "Read"}, {Name: "Grep"}, {Name: "Edit"},
		{Name: "Read"}, {Name: "Grep"}, {Name: "Edit"},
	}
	findings3 := DetectLoops(tools3, "")
	if len(findings3) == 0 || findings3[0].Rule != "cycle_pattern" {
		t.Errorf("expected cycle_pattern rule for 3-tool cycle, got %+v", findings3)
	}
}

func TestDetectLoops_RepeatedError(t *testing.T) {
	tools := []ToolCallItem{
		{Name: "ToolA", Error: "command not found"},
		{Name: "ToolB", Error: "command not found"},
		{Name: "ToolC", Error: "command not found"},
	}
	findings := DetectLoops(tools, "")
	found := false
	for _, f := range findings {
		if f.Rule == "repeated_error" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected finding for repeated error, got %+v", findings)
	}
}

func TestDetectLoops_NormalNoLoop(t *testing.T) {
	tools := []ToolCallItem{
		{Name: "Read"}, {Name: "Grep"}, {Name: "Edit"}, {Name: "Bash"},
	}
	findings := DetectLoops(tools, "")
	if len(findings) != 0 {
		t.Fatalf("expected 0 findings for normal flow, got %d", len(findings))
	}
}

func TestExtractToolCallsFromOpenAIMessages(t *testing.T) {
	// Test extraction from standard tool_calls
	msgs := []OpenAIMessage{
		{
			Role: "assistant",
			ToolCalls: []OpenAIToolCall{
				{Function: OpenAIFunctionCall{Name: "Bash", Arguments: `{"command":"ls"}`}},
			},
		},
		{
			Role:    "tool",
			Content: "file1 file2",
		},
		{
			Role: "assistant",
			ToolCalls: []OpenAIToolCall{
				{Function: OpenAIFunctionCall{Name: "Bash", Arguments: `{"command":"pwd"}`}},
			},
		},
	}
	tools := ExtractToolCallsFromOpenAIMessages(msgs)
	if len(tools) != 2 {
		t.Fatalf("expected 2 extracted tools, got %d", len(tools))
	}
	if tools[0].Name != "Bash" || tools[1].Name != "Bash" {
		t.Errorf("unexpected tool names: %+v", tools)
	}
}

func TestExtractToolCallsFromAnthropicMessages(t *testing.T) {
	msgs := []OpenAIMessage{
		{
			Role: "assistant",
			Content: []any{
				map[string]any{
					"type": "tool_use",
					"id":   "toolu_01",
					"name": "Bash",
					"input": map[string]any{"command": "cat /nonexistent"},
				},
			},
		},
		{
			Role: "user",
			Content: []any{
				map[string]any{
					"type":        "tool_result",
					"tool_use_id": "toolu_01",
					"content":     "cat: /nonexistent: No such file or directory",
					"is_error":    true,
				},
			},
		},
	}
	tools := ExtractToolCallsFromOpenAIMessages(msgs)
	if len(tools) != 1 {
		t.Fatalf("expected 1 extracted tool, got %d", len(tools))
	}
	if tools[0].Name != "Bash" {
		t.Errorf("expected tool Bash, got %s", tools[0].Name)
	}
	if tools[0].Error == "" {
		t.Errorf("expected Error populated from Anthropic tool_result, got empty")
	}
}

func TestDetectLoops_PrefersTrailingLoops(t *testing.T) {
	// Ancient loop at beginning
	tools := []ToolCallItem{
		{Name: "Read"}, {Name: "Read"}, {Name: "Read"},
		{Name: "Grep"}, {Name: "Edit"}, {Name: "Grep"}, {Name: "Edit"},
		// Active trailing loop at the end
		{Name: "Bash"}, {Name: "Bash"}, {Name: "Bash"}, {Name: "Bash"},
	}
	findings := DetectLoops(tools, "")
	if len(findings) == 0 {
		t.Fatalf("expected findings, got 0")
	}
	// The first finding should be the active trailing loop (Bash)
	if findings[0].Tool != "Bash" {
		t.Errorf("expected findings[0] to be active trailing tool Bash, got %s", findings[0].Tool)
	}
}

func TestDetectLoops_RuneSafeTruncation(t *testing.T) {
	// Vietnamese string with multi-byte characters
	vnErr := "Lỗi thực thi lệnh hệ thống rất dài vượt quá một trăm ký tự để kiểm tra việc cắt chuỗi an toàn không bị lỗi rune"
	tools := []ToolCallItem{
		{Name: "Tool1", Error: vnErr},
		{Name: "Tool2", Error: vnErr},
		{Name: "Tool3", Error: vnErr},
	}
	findings := DetectLoops(tools, "")
	if len(findings) == 0 {
		t.Fatalf("expected repeated_error finding, got 0")
	}
}
