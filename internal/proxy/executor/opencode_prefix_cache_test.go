package executor

import (
	"strconv"
	"strings"
	"testing"

	json "encoding/json/v2"
)

// DeepSeek's upstream prompt cache is prefix-based: it caches the longest
// byte-identical prefix of the serialized request. The gateway injects
// reasoning_content into every assistant message, then re-serializes the
// whole body — so the serialization itself must be deterministic or the
// prefix breaks on every request. json/v2 randomizes map member order by
// default; InjectReasoningContent must marshal with Deterministic(true).

func deepseekBody(turns int) []byte {
	var b strings.Builder
	b.WriteString(`{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"sys"}`)
	for i := 0; i < turns; i++ {
		b.WriteString(`,{"role":"assistant","content":"a` + strconv.Itoa(i) + `"}`)
		b.WriteString(`,{"role":"user","content":"u` + strconv.Itoa(i) + `"}`)
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

func TestInjectReasoningContent_DeepSeekInjectsEveryAssistant(t *testing.T) {
	res := InjectReasoningContent(deepseekBody(3), "opencode")

	var m map[string]any
	if err := json.Unmarshal(res, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for i, raw := range m["messages"].([]any) {
		msg := raw.(map[string]any)
		if msg["role"] == "assistant" {
			if rc, _ := msg["reasoning_content"].(string); rc != " " {
				t.Errorf("assistant at index %d must carry reasoning_content, got %v", i, msg["reasoning_content"])
			}
		}
	}
}

// A two-turn session: turn 2 is turn 1 plus appended messages. Every byte of
// turn 1's processed body (up to its last shared message) must be identical
// in turn 2's body — that shared byte prefix is what DeepSeek caches on.
func TestInjectReasoningContent_StablePrefixAcrossTurns(t *testing.T) {
	turn1 := string(InjectReasoningContent(deepseekBody(2), "opencode"))
	turn2 := string(InjectReasoningContent(deepseekBody(3), "opencode"))

	shared := `{"messages":[{"content":"sys","role":"user"},{"content":"a0","reasoning_content":" ","role":"assistant"},{"content":"u0","role":"user"},{"content":"a1","reasoning_content":" ","role":"assistant"},{"content":"u1","role":"user"}`
	if !strings.HasPrefix(turn1, shared) {
		t.Errorf("turn 1 body not in canonical form:\ngot: %s", turn1)
	}
	if !strings.HasPrefix(turn2, shared) {
		t.Errorf("turn 2 body broke the shared prefix with turn 1:\ngot: %s", turn2)
	}
}

// Kimi keeps the injection only for assistant messages carrying tool_calls:
// its upstream rejects those without a reasoning_content field.
func TestInjectReasoningContent_KimiInjectsToolCallMessages(t *testing.T) {
	input := []byte(`{"model":"kimi-k2.5","messages":[
		{"role":"assistant","content":"old","tool_calls":[{"id":"t1","type":"function","function":{"name":"read","arguments":"{}"}}]},
		{"role":"user","content":"hi"}
	]}`)

	res := InjectReasoningContent(input, "opencode")
	var m map[string]any
	if err := json.Unmarshal(res, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msgs := m["messages"].([]any)
	if rc, _ := msgs[0].(map[string]any)["reasoning_content"].(string); rc != " " {
		t.Errorf("kimi assistant with tool_calls must keep reasoning_content, got %v", msgs[0].(map[string]any)["reasoning_content"])
	}
}
