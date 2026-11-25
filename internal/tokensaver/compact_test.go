package tokensaver

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"strings"
	"testing"
)

func TestCompactContextByBudget_TableDriven(t *testing.T) {
	largeOutput := strings.Repeat("line of tool output data 1234567890\n", 50)
	errorOutput := "Error: execution failed with exit code 1\n" + strings.Repeat("error trace line\n", 30)

	tests := []struct {
		name                string
		messages            []map[string]any
		opts                CompactContextOptions
		wantCompacted       bool
		verifySysPrompt     bool
		verifyInitObjective bool
		verifyLastTurn      bool
		verifyCompactedStub bool
	}{
		{
			name: "request below threshold remains completely untouched",
			messages: []map[string]any{
				{"role": "system", "content": "You are a helpful assistant."},
				{"role": "user", "content": "Hello world"},
				{"role": "assistant", "content": "Hi there!"},
			},
			opts: CompactContextOptions{
				ModelContextWindow: 200000,
				BudgetRatio:        0.85,
			},
			wantCompacted: false,
		},
		{
			name: "explicit budget token threshold below token count triggers compaction",
			messages: []map[string]any{
				{"role": "system", "content": "System prompt instructions."},
				{"role": "user", "content": "Initial user task objective."},
				{"role": "assistant", "content": "Running task..."},
				{"role": "tool", "tool_call_id": "call_1", "content": largeOutput},
				{"role": "assistant", "content": "I finished the task."},
				{"role": "user", "content": "Recent question preserved."},
			},
			opts: CompactContextOptions{
				BudgetTokens:        200,
				PreserveRecentTurns: 1,
			},
			wantCompacted:       true,
			verifySysPrompt:     true,
			verifyInitObjective: true,
			verifyLastTurn:      true,
			verifyCompactedStub: true,
		},
		{
			name: "model context window 85% ratio triggers compaction when exceeding threshold",
			messages: []map[string]any{
				{"role": "system", "content": "System instructions preserved."},
				{"role": "user", "content": "Initial goal preserved."},
				{"role": "tool", "tool_call_id": "call_2", "content": largeOutput},
				{"role": "assistant", "content": "Working on it..."},
				{"role": "user", "content": "Final question preserved."},
			},
			opts: CompactContextOptions{
				ModelContextWindow:  300, // 85% is 255 tokens, largeOutput is ~450 tokens
				BudgetRatio:         0.85,
				PreserveRecentTurns: 1,
			},
			wantCompacted:       true,
			verifySysPrompt:     true,
			verifyInitObjective: true,
			verifyLastTurn:      true,
			verifyCompactedStub: true,
		},
		{
			name: "preserves error tool results even when budget exceeded",
			messages: []map[string]any{
				{"role": "system", "content": "System prompt"},
				{"role": "user", "content": "Run command"},
				{"role": "tool", "tool_call_id": "call_err", "content": errorOutput},
				{"role": "assistant", "content": "Command failed, fixing."},
				{"role": "user", "content": "Next step"},
			},
			opts: CompactContextOptions{
				BudgetTokens:        50,
				PreserveRecentTurns: 1,
			},
			wantCompacted: false,
		},
		{
			name: "protects recent N turns from compaction",
			messages: []map[string]any{
				{"role": "user", "content": "First prompt"},
				{"role": "tool", "tool_call_id": "call_old", "content": largeOutput},
				{"role": "assistant", "content": "Turn 1 reply"},
				{"role": "user", "content": "Turn 2 user"},
				{"role": "tool", "tool_call_id": "call_recent", "content": largeOutput},
				{"role": "assistant", "content": "Turn 2 reply"},
			},
			opts: CompactContextOptions{
				BudgetTokens:        200,
				PreserveRecentTurns: 2, // protects Turn 1 reply, Turn 2 user, call_recent, Turn 2 reply
			},
			wantCompacted: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bodyObj := map[string]any{
				"model":    "test-model",
				"messages": tc.messages,
			}
			bodyBytes, err := json.Marshal(bodyObj)
			if err != nil {
				t.Fatalf("json.Marshal failed: %v", err)
			}

			outBytes, res := CompactContextByBudget(bodyBytes, tc.opts)
			if res.Compacted != tc.wantCompacted {
				t.Fatalf("Compacted = %v, want %v", res.Compacted, tc.wantCompacted)
			}

			if !tc.wantCompacted {
				if string(outBytes) != string(bodyBytes) {
					t.Fatalf("expected body to be untouched byte-for-byte")
				}
				return
			}

			var parsed map[string]any
			if err := json.Unmarshal(outBytes, &parsed); err != nil {
				t.Fatalf("failed to parse output JSON: %v", err)
			}
			msgs := parsed["messages"].([]any)

			if tc.verifySysPrompt {
				first := msgs[0].(map[string]any)
				if !strings.Contains(first["content"].(string), "preserved") && !strings.Contains(first["content"].(string), "instructions") {
					t.Errorf("system prompt was altered: %v", first["content"])
				}
			}

			if tc.verifyInitObjective {
				initUser := msgs[1].(map[string]any)
				if !strings.Contains(initUser["content"].(string), "Initial") {
					t.Errorf("initial user objective was altered: %v", initUser["content"])
				}
			}

			if tc.verifyLastTurn {
				last := msgs[len(msgs)-1].(map[string]any)
				if !strings.Contains(last["content"].(string), "preserved") {
					t.Errorf("last turn was altered: %v", last["content"])
				}
			}

			if tc.verifyCompactedStub {
				foundStub := false
				for _, m := range msgs {
					mMap := m.(map[string]any)
					if c, ok := mMap["content"].(string); ok && strings.Contains(c, "[output compacted:") {
						foundStub = true
						break
					}
				}
				if !foundStub {
					t.Errorf("expected at least one compacted output stub [output compacted: ...]")
				}
			}
		})
	}
}

func TestCompactContextByBudget_ClaudeFormat(t *testing.T) {
	largeToolResult := strings.Repeat("file content dump from tool\n", 60)

	bodyObj := map[string]any{
		"model": "claude-3-5-sonnet-20241022",
		"messages": []any{
			map[string]any{"role": "user", "content": "Initial prompt"},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "id": "tu_1", "name": "read_file", "input": map[string]any{"path": "foo.txt"}},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "tu_1", "content": largeToolResult},
			}},
			map[string]any{"role": "assistant", "content": "Let me examine this further."},
			map[string]any{"role": "user", "content": "Latest user message"},
		},
	}

	body, _ := json.Marshal(bodyObj)

	opts := CompactContextOptions{
		BudgetTokens:        150,
		PreserveRecentTurns: 1,
	}

	out, res := CompactContextByBudget(body, opts)
	if !res.Compacted {
		t.Fatalf("expected compaction for Claude format")
	}

	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("failed to unmarshal output: %v", err)
	}

	messages := parsed["messages"].([]any)
	toolUserMsg := messages[2].(map[string]any)
	contentArr := toolUserMsg["content"].([]any)
	toolResultBlock := contentArr[0].(map[string]any)
	trContent := toolResultBlock["content"].(string)

	if !strings.Contains(trContent, "[output compacted:") {
		t.Errorf("expected Claude tool_result to be compacted, got: %s", trContent)
	}
}

func TestCompactContextByBudget_ResponsesInputFormat(t *testing.T) {
	largeOutput := strings.Repeat("responses tool output data\n", 50)

	bodyObj := map[string]any{
		"model": "gpt-4o",
		"input": []any{
			map[string]any{"role": "user", "content": "Initial input prompt"},
			map[string]any{"type": "function_call", "call_id": "c1", "name": "exec"},
			map[string]any{"type": "function_call_output", "call_id": "c1", "output": largeOutput},
			map[string]any{"role": "assistant", "content": "Done"},
			map[string]any{"role": "user", "content": "Recent input"},
		},
	}

	body, _ := json.Marshal(bodyObj)

	opts := CompactContextOptions{
		BudgetTokens:        100,
		PreserveRecentTurns: 1,
	}

	out, res := CompactContextByBudget(body, opts)
	if !res.Compacted {
		t.Fatalf("expected Responses input format to be compacted")
	}

	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("failed to unmarshal output: %v", err)
	}

	inputList := parsed["input"].([]any)
	fcOutput := inputList[2].(map[string]any)
	outputStr := fcOutput["output"].(string)

	if !strings.Contains(outputStr, "[output compacted:") {
		t.Errorf("expected function_call_output to be compacted, got: %s", outputStr)
	}
}

// TestFormatCompactedSummary_MultibyteUTF8 verifies that formatCompactedSummary
// slices head and tail at UTF-8 rune boundaries, so CJK and emoji text is never
// split mid-sequence, and json.Marshal with json.Deterministic(true) succeeds.
func TestFormatCompactedSummary_MultibyteUTF8(t *testing.T) {
	// Multibyte text with CJK characters and emoji, each rune is 3-4 bytes.
	text := strings.Repeat("こんにちは🌍世界", 50)

	summary := formatCompactedSummary(text)

	// The summary itself must round-trip through deterministic JSON marshaling.
	raw := jsontext.Value([]byte(fmt.Sprintf(`{"summary": %q}`, summary)))
	if _, err := json.Marshal(raw, json.Deterministic(true)); err != nil {
		t.Fatalf("json.Marshal with deterministic failed on multibyte summary: %v", err)
	}

	// Head and tail previews must each be intact runes (no replacement chars from split bytes).
	if strings.Contains(summary, "�") {
		t.Fatalf("summary contains replacement character U+FFFD, indicating a split multibyte rune: %q", summary)
	}

	// The head preview should start with the first rune of the text.
	if !strings.HasPrefix(summary, "こんにちは🌍世界") {
		t.Fatalf("head preview does not start with intact multibyte runes: %q", summary)
	}

	// The tail preview should end with the last rune of the text.
	if !strings.HasSuffix(summary, "こんにちは🌍世界") {
		t.Fatalf("tail preview does not end with intact multibyte runes: %q", summary)
	}
}
