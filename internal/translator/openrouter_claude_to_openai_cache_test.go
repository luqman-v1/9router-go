package translator

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestOpenRouterClaudeToOpenAISystemCacheControlSurvives asserts that a
// Claude request whose system block carries a cache_control marker keeps that
// marker on the converted OpenAI system message's content blocks (not
// flattened to a plain string) for the OpenRouter Anthropic lane.
func TestOpenRouterClaudeToOpenAISystemCacheControlSurvives(t *testing.T) {
	claudeBody := []byte(`{
		"model": "claude-3-5-sonnet",
		"system": [
			{"type": "text", "text": "sys1"},
			{"type": "text", "text": "sys2", "cache_control": {"type": "ephemeral"}}
		],
		"messages": [
			{"role": "user", "content": "hi"}
		]
	}`)

	translated, err := TranslateClaudeToOpenAI(claudeBody, "openrouter", openRouterAnthropicModel)
	if err != nil {
		t.Fatalf("TranslateClaudeToOpenAI: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(translated, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msgs, _ := body["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatalf("no messages: %s", string(translated))
	}
	// system message is first (system role)
	var sysMsg map[string]any
	for _, m := range msgs {
		mm := m.(map[string]any)
		if mm["role"] == "system" {
			sysMsg = mm
		}
	}
	if sysMsg == nil {
		t.Fatalf("no system message: %s", string(translated))
	}
	content, ok := sysMsg["content"].([]any)
	if !ok {
		t.Fatalf("system content not a block slice: %v", sysMsg["content"])
	}
	found := false
	for _, b := range content {
		bm := b.(map[string]any)
		if bm["text"] == "sys2" && bm["cache_control"] != nil {
			found = true
		}
	}
	if !found {
		t.Errorf("system cache_control marker did not survive: %s", string(translated))
	}
}

// TestOpenRouterOpenAIFilterCapsFiveMarkers asserts that a direct OpenAI-format
// request carrying 5 cache_control markers is capped to 4 by
// FilterToOpenAIFormatJSON when PreserveCacheControl is set.
func TestOpenRouterOpenAIFilterCapsFiveMarkers(t *testing.T) {
	body := map[string]any{
		"model": "x",
		"system": []any{
			map[string]any{"type": "text", "text": "sys", "cache_control": map[string]any{"type": "ephemeral"}},
		},
		"messages": []any{
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "u", "cache_control": map[string]any{"type": "ephemeral"}},
			}},
		},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "a"}, "cache_control": map[string]any{"type": "ephemeral"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "b"}, "cache_control": map[string]any{"type": "ephemeral"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "c"}, "cache_control": map[string]any{"type": "ephemeral"}},
		},
	}
	raw, _ := json.Marshal(body)
	out, err := FilterToOpenAIFormatJSON(raw, FilterOptions{PreserveCacheControl: true})
	if err != nil {
		t.Fatalf("FilterToOpenAIFormatJSON: %v", err)
	}
	if got := strings.Count(string(out), "cache_control"); got != cacheControlMarkerBudget {
		t.Errorf("expected %d cache_control markers, got %d: %s", cacheControlMarkerBudget, got, string(out))
	}
}
