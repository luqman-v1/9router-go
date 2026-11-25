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
	if cc, has := mid["cache_control"].(map[string]any); !has || cc["type"] != "ephemeral" || cc["ttl"] != nil {
		t.Errorf("middle tool should have the 5m cache, got %v", mid["cache_control"])
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
	if cc, has := last["cache_control"].(map[string]any); !has || cc["ttl"] != nil {
		t.Errorf("last tool should have the 5m cache, got %v", last["cache_control"])
	}
	first, _ := tools[0].(map[string]any)
	if _, has := first["cache_control"]; has {
		t.Error("first tool should not have cache")
	}
}

func TestAnchorClaudeCache_CapsAt4Markers(t *testing.T) {
	// System has 1 block, Tools has 1 block, Messages has 4 blocks with cache_control
	// Total markers = 6. Capping must hold head anchors (system, tool) and keep only 2 tail message markers (total 4).
	body := []byte(`{
		"system": [{"type": "text", "text": "system instruction"}],
		"tools": [{"name": "toolA", "input_schema": {"type": "object"}}],
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "u1", "cache_control": {"type": "ephemeral"}}]},
			{"role": "assistant", "content": [{"type": "text", "text": "a1", "cache_control": {"type": "ephemeral"}}]},
			{"role": "user", "content": [{"type": "text", "text": "u2", "cache_control": {"type": "ephemeral"}}]},
			{"role": "assistant", "content": [{"type": "text", "text": "a2", "cache_control": {"type": "ephemeral"}}]}
		]
	}`)

	out := AnchorClaudeCache(body)
	var req map[string]any
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if total := countCacheControlBlocks(req); total != 4 {
		t.Fatalf("expected exactly 4 cache_control blocks, got %d", total)
	}

	// Head anchors preserved at the 5m default
	sys := req["system"].([]any)
	sysBlock := sys[0].(map[string]any)
	if cc, ok := sysBlock["cache_control"].(map[string]any); !ok || cc["ttl"] != nil {
		t.Errorf("system head anchor missing or not 5m: %v", sysBlock["cache_control"])
	}

	tools := req["tools"].([]any)
	toolBlock := tools[0].(map[string]any)
	if cc, ok := toolBlock["cache_control"].(map[string]any); !ok || cc["ttl"] != nil {
		t.Errorf("tool head anchor missing or not 5m: %v", toolBlock["cache_control"])
	}

	// In messages, u1 and a1 (first 2) should have had cache_control deleted, u2 and a2 (tail 2) kept
	msgs := req["messages"].([]any)
	m0 := msgs[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	if _, has := m0["cache_control"]; has {
		t.Error("u1 cache_control should have been stripped")
	}
	m1 := msgs[1].(map[string]any)["content"].([]any)[0].(map[string]any)
	if _, has := m1["cache_control"]; has {
		t.Error("a1 cache_control should have been stripped")
	}
	m2 := msgs[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	if _, has := m2["cache_control"]; !has {
		t.Error("u2 cache_control should be preserved")
	}
	m3 := msgs[3].(map[string]any)["content"].([]any)[0].(map[string]any)
	if _, has := m3["cache_control"]; !has {
		t.Error("a2 cache_control should be preserved")
	}
}

func TestAnchorClaudeCache_SingleObjectContent(t *testing.T) {
	// Client sends content as a single object {type: "text", text: "..."} instead of [{...}]
	body := []byte(`{
		"messages": [
			{"role": "user", "content": {"type": "text", "text": "hello single object", "cache_control": {"type": "ephemeral"}}}
		]
	}`)

	out := AnchorClaudeCache(body)
	var req map[string]any
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	msgs := req["messages"].([]any)
	content, ok := msgs[0].(map[string]any)["content"].([]any)
	if !ok {
		t.Fatalf("content should be normalized to []any, got %T", msgs[0].(map[string]any)["content"])
	}
	if len(content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(content))
	}
	block := content[0].(map[string]any)
	if block["text"] != "hello single object" {
		t.Errorf("unexpected text: %v", block["text"])
	}
	// Client cache_control on single object was stripped before re-anchoring
}
