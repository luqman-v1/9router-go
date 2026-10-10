package chat

import (
	"encoding/json"
	"strings"
	"testing"
)

// wireHandler returns a ChatHandler with no repo: providerThinkingMode is ""
// and decode failures pass through.
func wireHandler() *ChatHandler {
	return &ChatHandler{}
}

func TestPrepareInboundFastPath(t *testing.T) {
	h := wireHandler()
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	out, wireModel, intent := h.prepareInboundThinking("openai", "m", body)
	if string(out) != string(body) {
		t.Fatalf("fast path rewrote: %s", out)
	}
	if wireModel != "m" || intent != nil {
		t.Fatalf("fast path = (%q, %+v)", wireModel, intent)
	}
}

func TestPrepareInboundSuffix(t *testing.T) {
	h := wireHandler()
	out, wireModel, intent := h.prepareInboundThinking("deepseek", "deepseek-chat(high)",
		[]byte(`{"model":"x","messages":[]}`))
	if wireModel != "deepseek-chat" {
		t.Fatalf("wireModel = %q", wireModel)
	}
	if intent == nil || intent.Mode != "level" || intent.Level != "high" {
		t.Fatalf("intent = %+v", intent)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, has := m["reasoning_effort"]; has {
		t.Fatalf("inbound must not write effort: %s", out)
	}
}

func TestPrepareInboundCapture(t *testing.T) {
	h := wireHandler()
	out, wireModel, intent := h.prepareInboundThinking("claude", "claude-haiku-4.5",
		[]byte(`{"reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`))
	if wireModel != "claude-haiku-4.5" {
		t.Fatalf("wireModel = %q", wireModel)
	}
	if intent == nil || intent.Mode != "level" || intent.Display != "summarized" {
		t.Fatalf("intent = %+v", intent)
	}
	if !strings.Contains(string(out), "reasoning_effort") {
		t.Fatalf("inbound must keep client fields: %s", out)
	}
}

func TestPrepareInboundMalformed(t *testing.T) {
	h := wireHandler()
	bad := []byte(`{nope`)
	out, wireModel, _ := h.prepareInboundThinking("openai", "m(none)", bad)
	if string(out) != string(bad) || wireModel != "m" {
		t.Fatalf("malformed = (%s, %q)", out, wireModel)
	}
}

func TestInjectProviderThinking(t *testing.T) {
	h := wireHandler()
	// nil repo → no-op.
	m := map[string]any{}
	h.injectProviderThinking("deepseek", m)
	if len(m) != 0 {
		t.Fatalf("nil repo injected: %+v", m)
	}
}
