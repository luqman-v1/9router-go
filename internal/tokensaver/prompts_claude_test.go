package tokensaver

import (
	json "encoding/json/v2"
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

func TestInjectSystemPromptClaudePreservesCacheControl(t *testing.T) {
	prompt := "be terse"
	// Simulating Claude Code system prompt structure where the last block has cache_control
	body := []byte(`{
		"system": [
			{"type": "text", "text": "base instruction 1"},
			{"type": "text", "text": "base instruction 2", "cache_control": {"type": "ephemeral"}}
		],
		"messages": [{"role": "user", "content": "hi"}]
	}`)
	out, did := InjectSystemPromptClaude(body, prompt)
	if !did {
		t.Fatal("expected modification")
	}
	var req map[string]any
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatal(err)
	}
	blocks, _ := req["system"].([]any)
	if len(blocks) != 3 {
		t.Fatalf("expected 3 system blocks, got %d", len(blocks))
	}

	// The block with cache_control must remain the last block so prompt caching breakpoint is preserved
	lastBlock := blocks[2].(map[string]any)
	if lastBlock["cache_control"] == nil {
		t.Errorf("expected last block to carry cache_control, got %v", lastBlock)
	}
	if lastBlock["text"] != "base instruction 2" {
		t.Errorf("expected last block text 'base instruction 2', got %v", lastBlock["text"])
	}

	// Injected block must be placed before cache_control block
	injectedBlock := blocks[1].(map[string]any)
	if injectedBlock["text"] != prompt {
		t.Errorf("expected injected block at index 1 with text %q, got %v", prompt, injectedBlock["text"])
	}
}
