package translator

import (
	json "encoding/json/v2"
	"testing"
)

// Regression for #838: Claude Code sends cache markers on earlier system
// blocks, which must be cleared when the last system block is re-anchored at 1h.
func TestAnchorClaudeCache_ClearsEarlierSystemMarkers(t *testing.T) {
	body := []byte(`{
		"tools": [{"name": "read", "input_schema": {"type": "object"}}],
		"system": [
			{"type": "text", "text": "first", "cache_control": {"type": "ephemeral"}},
			{"type": "text", "text": "second", "cache_control": {"type": "ephemeral", "ttl": "1h"}},
			{"type": "text", "text": "third", "cache_control": {"type": "ephemeral", "ttl": "5m"}},
			{"type": "text", "text": "fourth"}
		],
		"messages": [{"role": "user", "content": [
			{"type": "text", "text": "test", "cache_control": {"type": "ephemeral"}}
		]}]
	}`)
	out := AnchorClaudeCache(body)
	var req map[string]any
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatal(err)
	}
	if total := countCacheControlBlocks(req); total > cacheControlMarkerBudget {
		t.Fatalf("expected <= 4 markers, got %d", total)
	}
	system := req["system"].([]any)
	for i := range len(system) - 1 {
		if cc := system[i].(map[string]any)["cache_control"]; cc != nil {
			t.Errorf("system[%d]: stale cache_control not removed: %v", i, cc)
		}
	}
	last := system[len(system)-1].(map[string]any)["cache_control"].(map[string]any)
	if last["ttl"] != "1h" {
		t.Errorf("last system block must be anchored at 1h: %v", last)
	}
	tools := req["tools"].([]any)
	if tools[0].(map[string]any)["cache_control"].(map[string]any)["ttl"] != "1h" {
		t.Error("tool 1h anchor must remain")
	}
	msg := req["messages"].([]any)[0].(map[string]any)
	content := msg["content"].([]any)[0].(map[string]any)
	if cc := content["cache_control"].(map[string]any); cc["ttl"] != nil {
		t.Errorf("trailing 5m cache marker must remain: %v", cc)
	}
	if second := AnchorClaudeCache(out); string(second) != string(out) {
		t.Error("anchoring must be idempotent")
	}
}

func TestAnchorClaudeCache_PreservesLastSystemAnchor(t *testing.T) {
	tests := []struct {
		name   string
		system string
	}{
		{"single unmarked", `[{"type":"text","text":"a"}]`},
		{"single 5m", `[{"type":"text","text":"a","cache_control":{"type":"ephemeral"}}]`},
		{"single 1h", `[{"type":"text","text":"a","cache_control":{"type":"ephemeral","ttl":"1h"}}]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := AnchorClaudeCache([]byte(`{"system":` + tt.system + `,"messages":[{"role":"user","content":"test"}]}`))
			var req map[string]any
			if err := json.Unmarshal(out, &req); err != nil {
				t.Fatal(err)
			}
			sys := req["system"].([]any)
			cc, ok := sys[len(sys)-1].(map[string]any)["cache_control"].(map[string]any)
			if !ok || cc["ttl"] != "1h" {
				t.Errorf("last block should have 1h ttl, got %v", cc)
			}
		})
	}
}
