package translator

import (
	"encoding/json"
	"strings"
	"testing"
)

// openRouterAnthropicModel is the OpenRouter wire id of an Anthropic-served
// model. OpenRouter forwards the whole OpenAI body to the provider named in the
// model id, so a model under this prefix reaches Anthropic's own API, which
// honours the cache_control breakpoints a Claude Code caller sent.
const openRouterAnthropicModel = "anthropic/claude-3.5-sonnet"

// translateForTest converts a Claude body and runs the OpenAI filter the way
// tryForwardWithConnection does, returning the filtered bytes and the parsed
// request body.
func translateForTest(t *testing.T, claudeBody []byte, provider, model string) (string, map[string]any) {
	t.Helper()
	translated, err := TranslateClaudeToOpenAI(claudeBody, provider, model)
	if err != nil {
		t.Fatalf("TranslateClaudeToOpenAI: %v", err)
	}
	filtered, err := FilterToOpenAIFormatJSON(translated, FilterOptions{
		PreserveCacheControl: PreservesOpenRouterAnthropicCacheControl(provider, model),
	})
	if err != nil {
		t.Fatalf("FilterToOpenAIFormatJSON: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(filtered, &body); err != nil {
		t.Fatalf("unmarshal filtered: %v", err)
	}
	return string(filtered), body
}

// TestOpenRouterAnthropicCacheControlSurvivesConversion drives the full
// Claude→OpenAI conversion for a Claude Code request targeting an OpenRouter
// Anthropic model and asserts the cache_control breakpoints survive to the
// outbound body, both on a message content block and on a tool (upstream
// OmniRoute #19755 parity).
func TestOpenRouterAnthropicCacheControlSurvivesConversion(t *testing.T) {
	claudeBody := []byte(`{
		"model": "claude-3-5-sonnet",
		"messages": [
			{"role": "user", "content": [
				{"type": "text", "text": "hello", "cache_control": {"type": "ephemeral"}}
			]}
		],
		"tools": [
			{"name": "lookup", "description": "look up", "input_schema": {"type": "object"}, "cache_control": {"type": "ephemeral"}}
		]
	}`)

	out, body := translateForTest(t, claudeBody, "openrouter", openRouterAnthropicModel)

	// The message marker must survive on the content block, not be flattened
	// into a plain string.
	msgs, _ := body["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	content, _ := msgs[0].(map[string]any)["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(content))
	}
	block, _ := content[0].(map[string]any)
	if block["cache_control"] == nil {
		t.Errorf("message content block lost cache_control: %s", out)
	}

	// The tool marker must survive the OpenAI normalisation.
	tools, _ := body["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	tool, _ := tools[0].(map[string]any)
	if tool["cache_control"] == nil {
		t.Errorf("tool lost cache_control: %s", out)
	}
}

// TestOpenRouterAnthropicCacheControlStrippedForOtherModels asserts a
// non-Anthropic OpenRouter model (and any other provider) loses the
// cache_control markers on the way out.
func TestOpenRouterAnthropicCacheControlStrippedForOtherModels(t *testing.T) {
	claudeBody := []byte(`{
		"model": "claude-3-5-sonnet",
		"messages": [
			{"role": "user", "content": [
				{"type": "text", "text": "hello", "cache_control": {"type": "ephemeral"}}
			]}
		],
		"tools": [
			{"name": "lookup", "description": "look up", "input_schema": {"type": "object"}, "cache_control": {"type": "ephemeral"}}
		]
	}`)

	tests := []struct {
		name     string
		provider string
		model    string
	}{
		{"openrouter non-anthropic model", "openrouter", "meta-llama/llama-3.1-70b-instruct"},
		{"other provider anthropic-prefixed model", "alicode", openRouterAnthropicModel},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, body := translateForTest(t, claudeBody, tt.provider, tt.model)
			if strings.Contains(out, "cache_control") {
				t.Errorf("cache_control survived for provider=%s model=%s: %s", tt.provider, tt.model, out)
			}
			// The filter may also drop the turn entirely when it flattens to
			// nothing the upstream accepts; either way no marker may survive.
			msgs, _ := body["messages"].([]any)
			if len(msgs) == 0 {
				return
			}
			content, _ := msgs[0].(map[string]any)["content"].([]any)
			if len(content) == 0 {
				return
			}
			block, _ := content[0].(map[string]any)
			if block["cache_control"] != nil {
				t.Errorf("message block kept cache_control: %v", block)
			}
		})
	}
}

// TestOpenRouterAnthropicCacheControlCappedAtFour asserts a Claude request that
// carries more than four cache_control breakpoints is capped to the last four
// markers, because Anthropic answers a larger request with a 400.
func TestOpenRouterAnthropicCacheControlCappedAtFour(t *testing.T) {
	// Six message blocks each carry a marker; the last four must survive.
	claudeBody := []byte(`{
		"model": "claude-3-5-sonnet",
		"messages": [
			{"role": "user", "content": [
				{"type": "text", "text": "a", "cache_control": {"type": "ephemeral"}},
				{"type": "text", "text": "b", "cache_control": {"type": "ephemeral"}},
				{"type": "text", "text": "c", "cache_control": {"type": "ephemeral"}}
			]},
			{"role": "assistant", "content": [
				{"type": "text", "text": "d", "cache_control": {"type": "ephemeral"}}
			]},
			{"role": "user", "content": [
				{"type": "text", "text": "e", "cache_control": {"type": "ephemeral"}},
				{"type": "text", "text": "f", "cache_control": {"type": "ephemeral"}}
			]}
		]
	}`)

	out, body := translateForTest(t, claudeBody, "openrouter", openRouterAnthropicModel)

	if got := strings.Count(out, "cache_control"); got != cacheControlMarkerBudget {
		t.Errorf("expected %d cache_control markers, got %d: %s", cacheControlMarkerBudget, got, out)
	}

	// The surviving markers are the last four: d, e, f plus the final block of
	// the first turn (c). The earliest two (a, b) are dropped.
	msgs, _ := body["messages"].([]any)
	firstTurn := msgs[0].(map[string]any)["content"].([]any)
	survivors := make([]string, 0, 4)
	for _, b := range firstTurn {
		bm := b.(map[string]any)
		if bm["cache_control"] == nil {
			continue
		}
		survivors = append(survivors, bm["text"].(string))
	}
	if len(survivors) != 1 || survivors[0] != "c" {
		t.Errorf("expected only block c to survive in the first turn, got %v", survivors)
	}

	// And no marker may be left on the two dropped blocks.
	for i, b := range firstTurn {
		if i < 2 {
			if bm := b.(map[string]any); bm["cache_control"] != nil {
				t.Errorf("block %d should have been dropped, still carries cache_control", i)
			}
		}
	}
}

// TestOpenRouterAnthropicCacheControlCappedToolsAndMessages covers the mixed
// case: tool markers are capped together with the message markers, so the total
// never exceeds four.
func TestOpenRouterAnthropicCacheControlCappedToolsAndMessages(t *testing.T) {
	claudeBody := []byte(`{
		"model": "claude-3-5-sonnet",
		"messages": [
			{"role": "user", "content": [
				{"type": "text", "text": "hi", "cache_control": {"type": "ephemeral"}}
			]}
		],
		"tools": [
			{"name": "a", "description": "t", "input_schema": {"type": "object"}, "cache_control": {"type": "ephemeral"}},
			{"name": "b", "description": "t", "input_schema": {"type": "object"}, "cache_control": {"type": "ephemeral"}},
			{"name": "c", "description": "t", "input_schema": {"type": "object"}, "cache_control": {"type": "ephemeral"}},
			{"name": "d", "description": "t", "input_schema": {"type": "object"}, "cache_control": {"type": "ephemeral"}},
			{"name": "e", "description": "t", "input_schema": {"type": "object"}, "cache_control": {"type": "ephemeral"}}
		]
	}`)

	out, _ := translateForTest(t, claudeBody, "openrouter", openRouterAnthropicModel)

	if got := strings.Count(out, "cache_control"); got != cacheControlMarkerBudget {
		t.Errorf("expected %d cache_control markers, got %d: %s", cacheControlMarkerBudget, got, out)
	}
}
