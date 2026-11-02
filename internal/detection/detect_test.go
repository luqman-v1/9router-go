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
