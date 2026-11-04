package detection

import "testing"

func TestDetectLoops_ConsecutiveToolSameArgs(t *testing.T) {
	// Identical arguments -> true stuck loop
	tools := []ToolCallItem{
		{Name: "Bash", Arguments: `{"command":"pytest"}`},
		{Name: "Bash", Arguments: `{"command":"pytest"}`},
		{Name: "Bash", Arguments: `{"command":"pytest"}`},
	}
	findings := DetectLoops(tools, "")
	if len(findings) == 0 {
		t.Fatalf("expected finding for 3 consecutive Bash calls with same args, got none")
	}
	if findings[0].Rule != "consecutive_tool" || findings[0].Severity != SeverityCritical {
		t.Errorf("expected critical consecutive_tool finding, got %+v", findings[0])
	}
}

func TestDetectLoops_ConsecutiveToolDifferentArgsNotLoop(t *testing.T) {
	// Different files read in large codebase (e.g. 5 different files) -> NOT a stuck loop
	tools := []ToolCallItem{
		{Name: "Read", Arguments: `{"file_path":"a.go"}`},
		{Name: "Read", Arguments: `{"file_path":"b.go"}`},
		{Name: "Read", Arguments: `{"file_path":"c.go"}`},
		{Name: "Read", Arguments: `{"file_path":"d.go"}`},
		{Name: "Read", Arguments: `{"file_path":"e.go"}`},
	}
	findings := DetectLoops(tools, "")
	if len(findings) != 0 {
		t.Fatalf("expected 0 findings for reading 5 different files, got %+v", findings)
	}
}

// Regression for the false positive seen in production: an agentic coding
// session ran the same tool ("terminal") 15 times in a row, each with a
// different command (grep socketFile, grep capabilityLen, git diff ...).
// The removed "!allSameArgs && length >= 12" rule flagged that as a loop.
// Same-tool exploration with varying arguments is normal workflow, not a loop.
func TestDetectLoops_SameToolDifferentArgsNeverFlagsEvenAt15(t *testing.T) {
	commands := []string{"grep socketFile", "grep ResolveDataDir", "grep capabilityLen", "grep mcode-auth-lease-v1.sock", "grep capabilityFile", "grep isBase64URL", "grep claudeIdentityRegex", "git diff media.go", "git diff live_catalog.go", "grep fallback", "grep ResolveEndpoint", "grep socketFile runDir", "grep capabilityLen cap", "grep truncateRuneSafe", "git diff usage.go"}
	tools := make([]ToolCallItem, 0, len(commands))
	for _, cmd := range commands {
		tools = append(tools, ToolCallItem{Name: "terminal", Arguments: `{"command":"` + cmd + `"}`})
	}
	findings := DetectLoops(tools, "")
	if len(findings) != 0 {
		t.Fatalf("expected 0 findings for 15 terminal calls with different arguments, got %+v", findings)
	}
}

// Sanity floor: the stuck loop itself (identical args) must still fire after
// removing the frequency rule, at any length, including when a single call
// in the run differs (falls back to frequency territory, must not fire).
func TestDetectLoops_SameArgsStillFlagsAtAnyLength(t *testing.T) {
	for _, n := range []int{3, 5, 15} {
		tools := make([]ToolCallItem, 0, n)
		for range n {
			tools = append(tools, ToolCallItem{Name: "terminal", Arguments: `{"command":"pytest"}`})
		}
		findings := DetectLoops(tools, "")
		if len(findings) == 0 {
			t.Fatalf("expected stuck-loop finding for %d identical calls, got none", n)
		}
		if findings[0].Rule != "consecutive_tool" || findings[0].Severity != SeverityCritical {
			t.Errorf("expected critical consecutive_tool for %d identical calls, got %+v", n, findings[0])
		}
	}
}

func TestDetectLoops_CyclePattern(t *testing.T) {
	// 2-tool cycle: [Read, Edit] repeated 3 times
	tools := []ToolCallItem{
		{Name: "Read", Arguments: "f.go"}, {Name: "Edit", Arguments: "f.go"},
		{Name: "Read", Arguments: "f.go"}, {Name: "Edit", Arguments: "f.go"},
		{Name: "Read", Arguments: "f.go"}, {Name: "Edit", Arguments: "f.go"},
	}
	findings := DetectLoops(tools, "")
	if len(findings) == 0 {
		t.Fatalf("expected finding for [Read, Edit] cycle, got none")
	}
	if findings[0].Rule != "cycle_pattern" {
		t.Errorf("expected cycle_pattern rule, got %s", findings[0].Rule)
	}
}

func TestDetectLoops_RepeatedError(t *testing.T) {
	tools := []ToolCallItem{
		{Name: "ToolA", Error: "command not found: pytest"},
		{Name: "ToolB", Error: "command not found: pytest"},
		{Name: "ToolC", Error: "command not found: pytest"},
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

func TestDetectLoops_IgnoresAncientLoopsInPastTurns(t *testing.T) {
	// Ancient loop at beginning (turn 1-3)
	tools := []ToolCallItem{
		{Name: "Read", Arguments: "same.go"},
		{Name: "Read", Arguments: "same.go"},
		{Name: "Read", Arguments: "same.go"},
	}
	// Followed by 20 healthy varied tool calls across different tools
	toolNames := []string{"Bash", "Grep", "Read", "Edit"}
	for i := 0; i < 20; i++ {
		tools = append(tools, ToolCallItem{
			Name:      toolNames[i%len(toolNames)],
			Arguments: string(rune('a' + i)),
		})
	}
	findings := DetectLoops(tools, "")
	// Ancient loop is outside recent trailing window -> should not trigger warning now
	if len(findings) != 0 {
		t.Fatalf("expected ancient loop from turn 1 to be ignored at turn 23, got: %+v", findings)
	}
}

func TestExtractToolCallsFromAnthropicMessages(t *testing.T) {
	msgs := []OpenAIMessage{
		{
			Role: "assistant",
			Content: []any{
				map[string]any{
					"type":  "tool_use",
					"id":    "toolu_01",
					"name":  "Bash",
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
	if tools[0].Arguments == "" {
		t.Errorf("expected Arguments populated from Anthropic tool_use input, got empty")
	}
}
