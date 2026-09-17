package tokensaver

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInjectSystemPromptClaude(t *testing.T) {
	prompt := "be terse"

	// No system field: must set top-level system, never touch messages.
	body := []byte(`{"model":"claude-opus-5","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
	out, did := InjectSystemPromptClaude(body, prompt)
	if !did {
		t.Fatal("expected modification")
	}
	var req map[string]any
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatal(err)
	}
	if sys, _ := req["system"].(string); !strings.Contains(sys, prompt) {
		t.Fatalf("system field not set: %v", req["system"])
	}
	msgs, _ := req["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages must be untouched, got %d", len(msgs))
	}

	// Existing string system: appended, idempotent.
	body2 := []byte(`{"system":"existing","messages":[]}`)
	out2, _ := InjectSystemPromptClaude(body2, prompt)
	var req2 map[string]any
	json.Unmarshal(out2, &req2)
	if sys, _ := req2["system"].(string); !strings.Contains(sys, "existing") || !strings.Contains(sys, prompt) {
		t.Fatalf("expected merged system, got %v", req2["system"])
	}
	out3, did3 := InjectSystemPromptClaude(out2, prompt)
	if did3 || string(out3) != string(out2) {
		t.Fatalf("expected idempotent no-op, did=%v", did3)
	}

	// Existing block-style system: appends a text block.
	body4 := []byte(`{"system":[{"type":"text","text":"existing"}],"messages":[]}`)
	out4, did4 := InjectSystemPromptClaude(body4, prompt)
	if !did4 {
		t.Fatal("expected block append")
	}
	var req4 map[string]any
	json.Unmarshal(out4, &req4)
	blocks, _ := req4["system"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 system blocks, got %d", len(blocks))
	}
}

func TestInjectSystemPromptClaudeDoesNotTouchOpenAIMessages(t *testing.T) {
	// The Claude injector must never insert a role:"system" message — that
	// form is rejected by the Anthropic Messages API (400 messages.0).
	prompt := "be terse"
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	out, _ := InjectSystemPromptClaude(body, prompt)
	var req map[string]any
	json.Unmarshal(out, &req)
	if _, hasSystem := req["system"]; !hasSystem {
		t.Fatal("expected top-level system")
	}
	msgs, _ := req["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages must not gain a system entry, got %d", len(msgs))
	}
}
