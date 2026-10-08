package executor

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

func TestDeriveOpencodeSession(t *testing.T) {
	// Native session preserved (canonical 30 chars)
	native := "ses_f52b0d414ffeObbCKcHUQYZR2I"
	if got := deriveOpencodeSession(native, "claude", "conn1"); got != native {
		t.Errorf("expected native session %q, got %q", native, got)
	}

	// Translation format: ses_<12 hex + 14 Base62> length 30 (PR #4105)
	s1 := deriveOpencodeSession("my-conversation-id", "claude", "conn1")
	if !strings.HasPrefix(s1, "ses_") || len(s1) != 30 {
		t.Errorf("expected canonical session length 30, got %q (len %d)", s1, len(s1))
	}

	// Stable across calls
	s2 := deriveOpencodeSession("my-conversation-id", "claude", "conn1")
	if s1 != s2 {
		t.Errorf("expected stable session, got %q and %q", s1, s2)
	}

	// Isolated across different conversations
	s3 := deriveOpencodeSession("other-conversation", "claude", "conn1")
	if s1 == s3 {
		t.Errorf("different conversations should produce different sessions, got %q", s1)
	}

	// Isolated across different client tools
	sCodex := deriveOpencodeSession("my-conversation-id", "codex", "conn1")
	if s1 == sCodex {
		t.Errorf("different client tools should produce different sessions, got %q", s1)
	}

	// Fallback to connection ID
	sFallback := deriveOpencodeSession("", "", "my-conn-id")
	if !strings.HasPrefix(sFallback, "ses_") || len(sFallback) != 30 {
		t.Errorf("expected valid session on connection fallback, got %q", sFallback)
	}
}

func TestProcessCodexEvent_ParallelToolCallsItemKeying(t *testing.T) {
	state := &CodexStreamState{}
	respID := "chatcmpl-test"
	created := int64(123456789)

	// Item 0 added
	item0Added := `{"type":"response.output_item.added","item_id":"item_0","item":{"id":"item_0","call_id":"call_zero","type":"function_call","name":"tool_a"}}`
	chunks0 := ProcessCodexEvent(item0Added, state, respID, created)
	if len(chunks0) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks0))
	}

	// Item 1 added (in parallel, before item 0 delta)
	item1Added := `{"type":"response.output_item.added","item_id":"item_1","item":{"id":"item_1","call_id":"call_one","type":"function_call","name":"tool_b"}}`
	chunks1 := ProcessCodexEvent(item1Added, state, respID, created)
	if len(chunks1) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks1))
	}

	// Delta for Item 0
	delta0 := `{"type":"response.function_call_arguments.delta","item_id":"item_0","delta":"{\"arg\": 0}"}`
	chunkDelta0 := ProcessCodexEvent(delta0, state, respID, created)
	if len(chunkDelta0) != 1 {
		t.Fatalf("expected 1 chunk for delta0, got %d", len(chunkDelta0))
	}

	// Delta for Item 1
	delta1 := `{"type":"response.function_call_arguments.delta","item_id":"item_1","delta":"{\"arg\": 1}"}`
	chunkDelta1 := ProcessCodexEvent(delta1, state, respID, created)
	if len(chunkDelta1) != 1 {
		t.Fatalf("expected 1 chunk for delta1, got %d", len(chunkDelta1))
	}

	// Verify delta 0 has index 0
	var parsed0 struct {
		Choices []struct {
			Delta struct {
				ToolCalls []struct {
					Index int `json:"index"`
				} `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
	}
	raw0 := strings.TrimPrefix(strings.TrimSpace(chunkDelta0[0]), "data: ")
	if err := json.Unmarshal([]byte(raw0), &parsed0); err != nil {
		t.Fatalf("unmarshal delta0 failed: %v", err)
	}
	if len(parsed0.Choices) == 0 || len(parsed0.Choices[0].Delta.ToolCalls) == 0 {
		t.Fatalf("no tool calls in parsed delta0: %s", raw0)
	}
	if gotIdx := parsed0.Choices[0].Delta.ToolCalls[0].Index; gotIdx != 0 {
		t.Errorf("expected delta0 index 0, got %d", gotIdx)
	}

	// Verify delta 1 has index 1 (not merged into index 0!)
	var parsed1 struct {
		Choices []struct {
			Delta struct {
				ToolCalls []struct {
					Index int `json:"index"`
				} `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
	}
	raw1 := strings.TrimPrefix(strings.TrimSpace(chunkDelta1[0]), "data: ")
	if err := json.Unmarshal([]byte(raw1), &parsed1); err != nil {
		t.Fatalf("unmarshal delta1 failed: %v", err)
	}
	if len(parsed1.Choices) == 0 || len(parsed1.Choices[0].Delta.ToolCalls) == 0 {
		t.Fatalf("no tool calls in parsed delta1: %s", raw1)
	}
	if gotIdx := parsed1.Choices[0].Delta.ToolCalls[0].Index; gotIdx != 1 {
		t.Errorf("expected delta1 index 1, got %d (parallel tool call collision!)", gotIdx)
	}
}
