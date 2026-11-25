package resilience

import (
	"testing"
	"time"
)

func TestStalledToolLoopDetector_ConsecutiveIdenticalCalls(t *testing.T) {
	d := NewStalledToolLoopDetector(StalledToolLoopOptions{Threshold: 3})
	sessionID := "sess-123"

	// Call 1
	res1 := d.RecordToolCall(sessionID, "bash", `{"command":"cat file.txt"}`)
	if res1 != nil {
		t.Fatalf("expected nil intervention on call 1, got %v", res1)
	}
	if cnt := d.GetConsecutiveCount(sessionID); cnt != 1 {
		t.Errorf("expected count 1, got %d", cnt)
	}

	// Call 2
	res2 := d.RecordToolCall(sessionID, "bash", `{"command":"cat file.txt"}`)
	if res2 != nil {
		t.Fatalf("expected nil intervention on call 2, got %v", res2)
	}
	if cnt := d.GetConsecutiveCount(sessionID); cnt != 2 {
		t.Errorf("expected count 2, got %d", cnt)
	}

	// Call 3 - threshold reached!
	res3 := d.RecordToolCall(sessionID, "bash", `{"command":"cat file.txt"}`)
	if res3 == nil {
		t.Fatalf("expected intervention on call 3, got nil")
	}
	if res3.ToolName != "bash" {
		t.Errorf("expected tool_name 'bash', got %s", res3.ToolName)
	}
	if res3.CallCount != 3 {
		t.Errorf("expected call_count 3, got %d", res3.CallCount)
	}
	if res3.Intervention == "" {
		t.Errorf("expected non-empty intervention message")
	}
}

func TestStalledToolLoopDetector_ResetOnDifferentToolOrArgs(t *testing.T) {
	d := NewStalledToolLoopDetector(StalledToolLoopOptions{Threshold: 3})
	sessionID := "sess-456"

	// Call 1 & 2 identical
	d.RecordToolCall(sessionID, "bash", `{"command":"ls"}`)
	d.RecordToolCall(sessionID, "bash", `{"command":"ls"}`)

	if cnt := d.GetConsecutiveCount(sessionID); cnt != 2 {
		t.Errorf("expected count 2, got %d", cnt)
	}

	// Call 3 different arguments -> resets consecutive count to 1
	res3 := d.RecordToolCall(sessionID, "bash", `{"command":"ls -la"}`)
	if res3 != nil {
		t.Fatalf("expected nil intervention on different args, got %v", res3)
	}
	if cnt := d.GetConsecutiveCount(sessionID); cnt != 1 {
		t.Errorf("expected count 1, got %d", cnt)
	}

	// Call 4 different tool -> resets consecutive count to 1
	res4 := d.RecordToolCall(sessionID, "read_file", `{"path":"main.go"}`)
	if res4 != nil {
		t.Fatalf("expected nil intervention on different tool, got %v", res4)
	}
	if cnt := d.GetConsecutiveCount(sessionID); cnt != 1 {
		t.Errorf("expected count 1, got %d", cnt)
	}
}

func TestStalledToolLoopDetector_ResetSession(t *testing.T) {
	d := NewStalledToolLoopDetector(StalledToolLoopOptions{Threshold: 3})
	sessionID := "sess-reset"

	d.RecordToolCall(sessionID, "bash", `{"cmd":"pwd"}`)
	d.RecordToolCall(sessionID, "bash", `{"cmd":"pwd"}`)
	if cnt := d.GetConsecutiveCount(sessionID); cnt != 2 {
		t.Errorf("expected count 2, got %d", cnt)
	}

	d.ResetSession(sessionID)
	if cnt := d.GetConsecutiveCount(sessionID); cnt != 0 {
		t.Errorf("expected count 0 after reset, got %d", cnt)
	}

	// Next call starts fresh at 1
	d.RecordToolCall(sessionID, "bash", `{"cmd":"pwd"}`)
	if cnt := d.GetConsecutiveCount(sessionID); cnt != 1 {
		t.Errorf("expected count 1 after fresh call, got %d", cnt)
	}
}

func TestStalledToolLoopDetector_TTLExpiry(t *testing.T) {
	d := NewStalledToolLoopDetector(StalledToolLoopOptions{
		Threshold: 3,
		TTL:       50 * time.Millisecond,
	})
	sessionID := "sess-ttl"

	d.RecordToolCall(sessionID, "bash", `{"cmd":"pwd"}`)
	d.RecordToolCall(sessionID, "bash", `{"cmd":"pwd"}`)
	if cnt := d.GetConsecutiveCount(sessionID); cnt != 2 {
		t.Errorf("expected count 2, got %d", cnt)
	}

	time.Sleep(70 * time.Millisecond)

	// Call after TTL should reset count to 1
	res := d.RecordToolCall(sessionID, "bash", `{"cmd":"pwd"}`)
	if res != nil {
		t.Errorf("expected nil intervention after TTL expiry, got %v", res)
	}
	if cnt := d.GetConsecutiveCount(sessionID); cnt != 1 {
		t.Errorf("expected count 1 after TTL expiry, got %d", cnt)
	}
}

func TestInspectHistoryTurns_OpenAIToolCalls(t *testing.T) {
	t.Run("detects 3 consecutive identical assistant tool calls", func(t *testing.T) {
		messages := []map[string]any{
			{"role": "user", "content": "run tests"},
			{
				"role": "assistant",
				"tool_calls": []any{
					map[string]any{
						"function": map[string]any{
							"name":      "bash",
							"arguments": `{"command":"go test ./..."}`,
						},
					},
				},
			},
			{"role": "tool", "content": "FAIL"},
			{
				"role": "assistant",
				"tool_calls": []any{
					map[string]any{
						"function": map[string]any{
							"name":      "bash",
							"arguments": `{"command":"go test ./..."}`,
						},
					},
				},
			},
			{"role": "tool", "content": "FAIL"},
			{
				"role": "assistant",
				"tool_calls": []any{
					map[string]any{
						"function": map[string]any{
							"name":      "bash",
							"arguments": `{"command":"go test ./..."}`,
						},
					},
				},
			},
		}

		res := InspectHistoryTurns(messages, 3)
		if res == nil {
			t.Fatalf("expected loop detection, got nil")
		}
		if res.ToolName != "bash" {
			t.Errorf("expected 'bash', got %s", res.ToolName)
		}
		if res.CallCount != 3 {
			t.Errorf("expected 3 calls, got %d", res.CallCount)
		}
	})

	t.Run("user message in between resets count", func(t *testing.T) {
		messages := []map[string]any{
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
			{"role": "tool", "content": "a.txt"},
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
			{"role": "user", "content": "please continue"},
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

		res := InspectHistoryTurns(messages, 3)
		if res != nil {
			t.Errorf("expected nil intervention when user intervening turn, got %v", res)
		}
	})

	t.Run("different tool arguments do not trigger loop", func(t *testing.T) {
		messages := []map[string]any{
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
			{"role": "tool", "content": "a.txt"},
			{
				"role": "assistant",
				"tool_calls": []any{
					map[string]any{
						"function": map[string]any{
							"name":      "bash",
							"arguments": `{"command":"cat a.txt"}`,
						},
					},
				},
			},
			{"role": "tool", "content": "hello"},
			{
				"role": "assistant",
				"tool_calls": []any{
					map[string]any{
						"function": map[string]any{
							"name":      "bash",
							"arguments": `{"command":"rm a.txt"}`,
						},
					},
				},
			},
		}

		res := InspectHistoryTurns(messages, 3)
		if res != nil {
			t.Errorf("expected nil intervention on different args, got %v", res)
		}
	})
}

func TestInspectHistoryTurns_ClaudeToolUse(t *testing.T) {
	messages := []map[string]any{
		{
			"role": "assistant",
			"content": []any{
				map[string]any{
					"type":  "tool_use",
					"id":    "tool_1",
					"name":  "view_file",
					"input": `{"path":"config.json"}`,
				},
			},
		},
		{
			"role": "user",
			"content": []any{
				map[string]any{
					"type":        "tool_result",
					"tool_use_id": "tool_1",
					"content":     "not found",
				},
			},
		},
		{
			"role": "assistant",
			"content": []any{
				map[string]any{
					"type":  "tool_use",
					"id":    "tool_2",
					"name":  "view_file",
					"input": `{"path":"config.json"}`,
				},
			},
		},
		{
			"role": "user",
			"content": []any{
				map[string]any{
					"type":        "tool_result",
					"tool_use_id": "tool_2",
					"content":     "not found",
				},
			},
		},
		{
			"role": "assistant",
			"content": []any{
				map[string]any{
					"type":  "tool_use",
					"id":    "tool_3",
					"name":  "view_file",
					"input": `{"path":"config.json"}`,
				},
			},
		},
	}

	res := InspectHistoryTurns(messages, 3)
	if res == nil {
		t.Fatalf("expected loop detection for Claude tool_use, got nil")
	}
	if res.ToolName != "view_file" {
		t.Errorf("expected 'view_file', got %s", res.ToolName)
	}
	if res.CallCount != 3 {
		t.Errorf("expected call count 3, got %d", res.CallCount)
	}
}

// TestInspectHistoryTurns_NonStringTextBlockNoPanic asserts that a user message
// whose text block carries a nil or non-string `text` value does not panic on
// the naked type assertion and is treated as non-human (does not reset the loop).
func TestInspectHistoryTurns_NonStringTextBlockNoPanic(t *testing.T) {
	messages := []map[string]any{
		{
			"role": "assistant",
			"content": []any{
				map[string]any{
					"type":  "tool_use",
					"id":    "tool_1",
					"name":  "view_file",
					"input": `{"path":"config.json"}`,
				},
			},
		},
		// Human text block is nil -> not a human turn, must not reset; must not panic.
		{
			"role": "user",
			"content": []any{
				map[string]any{
					"type":        "tool_result",
					"tool_use_id": "tool_1",
					"content":     "not found",
				},
				map[string]any{
					"type": "text",
					"text": nil,
				},
			},
		},
		{
			"role": "assistant",
			"content": []any{
				map[string]any{
					"type":  "tool_use",
					"id":    "tool_2",
					"name":  "view_file",
					"input": `{"path":"config.json"}`,
				},
			},
		},
		// Human text block is a number (non-string) -> not a human turn; must not panic.
		{
			"role": "user",
			"content": []any{
				map[string]any{
					"type": "text",
					"text": 42,
				},
			},
		},
		{
			"role": "assistant",
			"content": []any{
				map[string]any{
					"type":  "tool_use",
					"id":    "tool_3",
					"name":  "view_file",
					"input": `{"path":"config.json"}`,
				},
			},
		},
	}

	// Must not panic.
	res := InspectHistoryTurns(messages, 3)
	if res == nil {
		t.Fatalf("expected loop detection, got nil")
	}
	if res.ToolName != "view_file" {
		t.Errorf("expected 'view_file', got %s", res.ToolName)
	}
	if res.CallCount != 3 {
		t.Errorf("expected call count 3, got %d", res.CallCount)
	}
}
