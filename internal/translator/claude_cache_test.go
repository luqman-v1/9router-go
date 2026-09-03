package translator

import (
	json "encoding/json/v2"
	"testing"
)

func TestLastCacheableToolIndex(t *testing.T) {
	if got := LastCacheableToolIndex(nil); got != -1 {
		t.Errorf("nil should be -1, got %d", got)
	}
	if got := LastCacheableToolIndex([]any{}); got != -1 {
		t.Errorf("empty should be -1, got %d", got)
	}
	tools := []any{
		map[string]any{"name": "a"},
		map[string]any{"name": "b"},
		map[string]any{"name": "c", "defer_loading": true},
	}
	if got := LastCacheableToolIndex(tools); got != 1 {
		t.Errorf("expected 1 (last non-deferred), got %d", got)
	}
	tools2 := []any{
		map[string]any{"name": "mcp_a", "defer_loading": true},
		map[string]any{"name": "mcp_b", "defer_loading": true},
	}
	if got := LastCacheableToolIndex(tools2); got != -1 {
		t.Errorf("all deferred should be -1, got %d", got)
	}
	tools3 := []any{
		map[string]any{"name": "a"},
		map[string]any{"name": "b"},
	}
	if got := LastCacheableToolIndex(tools3); got != 1 {
		t.Errorf("expected 1, got %d", got)
	}
}

func TestAnchorClaudeCache_MovesFromDeferredTail(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"a","description":"t","input_schema":{"type":"object","properties":{}}},{"name":"b","description":"t","input_schema":{"type":"object","properties":{}}},{"name":"mcp__x__y","description":"t","input_schema":{"type":"object","properties":{}},"defer_loading":true}]}`)
	out := AnchorClaudeCache(body)
	var req map[string]any
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	tools, _ := req["tools"].([]any)
	if len(tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(tools))
	}
	// Last tool is deferred, should NOT have cache_control
	last, _ := tools[2].(map[string]any)
	if _, has := last["cache_control"]; has {
		t.Error("deferred tail should not have cache_control")
	}
	mid, _ := tools[1].(map[string]any)
	if cc, has := mid["cache_control"].(map[string]any); !has || cc["type"] != "ephemeral" || cc["ttl"] != "1h" {
		t.Errorf("middle tool should have 1h cache, got %v", mid["cache_control"])
	}
	first, _ := tools[0].(map[string]any)
	if _, has := first["cache_control"]; has {
		t.Error("first tool should not have cache_control")
	}
}

func TestAnchorClaudeCache_AllDeferred_NoCache(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"mcp_a","description":"t","input_schema":{"type":"object","properties":{}},"defer_loading":true},{"name":"mcp_b","description":"t","input_schema":{"type":"object","properties":{}},"defer_loading":true}]}`)
	out := AnchorClaudeCache(body)
	var req map[string]any
	_ = json.Unmarshal(out, &req)
	tools, _ := req["tools"].([]any)
	for i, tr := range tools {
		if m, _ := tr.(map[string]any); m != nil {
			if _, has := m["cache_control"]; has {
				t.Errorf("tool %d deferred should not have cache_control", i)
			}
		}
	}
}

func TestAnchorClaudeCache_StripsClientCacheOnDeferred(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"mcp_a","description":"t","input_schema":{"type":"object","properties":{}},"defer_loading":true,"cache_control":{"type":"ephemeral"}}]}`)
	out := AnchorClaudeCache(body)
	var req map[string]any
	_ = json.Unmarshal(out, &req)
	tools, _ := req["tools"].([]any)
	m, _ := tools[0].(map[string]any)
	if _, has := m["cache_control"]; has {
		t.Error("should strip client cache_control from deferred tool")
	}
}

func TestAnchorClaudeCache_Normal_NoDefer(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"a","description":"t","input_schema":{"type":"object","properties":{}}},{"name":"b","description":"t","input_schema":{"type":"object","properties":{}}}]}`)
	out := AnchorClaudeCache(body)
	var req map[string]any
	_ = json.Unmarshal(out, &req)
	tools, _ := req["tools"].([]any)
	last, _ := tools[1].(map[string]any)
	if cc, has := last["cache_control"].(map[string]any); !has || cc["ttl"] != "1h" {
		t.Errorf("last tool should have 1h cache, got %v", last["cache_control"])
	}
	first, _ := tools[0].(map[string]any)
	if _, has := first["cache_control"]; has {
		t.Error("first tool should not have cache")
	}
}
