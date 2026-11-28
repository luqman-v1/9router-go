package translator

import (
	"encoding/json"
	"testing"

	"9router/proxy/internal/providers"
)

// dashscopeCacheProviders are the DashScope-family providers that keep Claude
// cache_control breakpoints on the Claude→OpenAI conversion (upstream #2069).
var dashscopeCacheProviders = []string{"alicode", "alicode-intl", "alims-intl", "alitp-intl", "qwencloud"}

func findMessageByRole(body map[string]any, role string) map[string]any {
	msgs, _ := body["messages"].([]any)
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		if mm["role"] == role {
			return mm
		}
	}
	return nil
}

// TestDashScopeClaudeCacheControlSurvivesConversion asserts that a Claude
// request targeting a DashScope cache-preserving provider keeps its
// cache_control markers through TranslateClaudeToOpenAI on system, user and
// assistant text blocks, and on tools.
func TestDashScopeClaudeCacheControlSurvivesConversion(t *testing.T) {
	claudeBody := []byte(`{
		"model": "qwen3-coder",
		"system": [
			{"type": "text", "text": "sys1"},
			{"type": "text", "text": "sys2", "cache_control": {"type": "ephemeral"}}
		],
		"messages": [
			{"role": "user", "content": [
				{"type": "text", "text": "hello", "cache_control": {"type": "ephemeral"}}
			]},
			{"role": "assistant", "content": [
				{"type": "text", "text": "assistant text", "cache_control": {"type": "ephemeral"}},
				{"type": "tool_use", "id": "tu1", "name": "lookup", "input": {}}
			]}
		],
		"tools": [
			{"name": "lookup", "description": "look up", "input_schema": {"type": "object"}, "cache_control": {"type": "ephemeral"}}
		]
	}`)

	for _, provider := range dashscopeCacheProviders {
		t.Run(provider, func(t *testing.T) {
			translated, err := TranslateClaudeToOpenAI(claudeBody, provider, "qwen3-coder")
			if err != nil {
				t.Fatalf("TranslateClaudeToOpenAI: %v", err)
			}
			var body map[string]any
			if err := json.Unmarshal(translated, &body); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			// system marker
			sysMsg := findMessageByRole(body, "system")
			if sysMsg == nil {
				t.Fatalf("no system message: %s", string(translated))
			}
			sysContent, ok := sysMsg["content"].([]any)
			if !ok {
				t.Fatalf("system content not a block slice: %v", sysMsg["content"])
			}
			sysFound := false
			for _, b := range sysContent {
				bm := b.(map[string]any)
				if bm["cache_control"] != nil {
					sysFound = true
				}
			}
			if !sysFound {
				t.Errorf("system cache_control marker did not survive: %s", string(translated))
			}

			// user marker
			userMsg := findMessageByRole(body, "user")
			if userMsg == nil {
				t.Fatalf("no user message: %s", string(translated))
			}
			userContent, ok := userMsg["content"].([]any)
			if !ok {
				t.Fatalf("user content not a block slice: %v", userMsg["content"])
			}
			userFound := false
			for _, b := range userContent {
				bm := b.(map[string]any)
				if bm["cache_control"] != nil {
					userFound = true
				}
			}
			if !userFound {
				t.Errorf("user cache_control marker did not survive: %s", string(translated))
			}

			// assistant marker
			assistantMsg := findMessageByRole(body, "assistant")
			if assistantMsg == nil {
				t.Fatalf("no assistant message: %s", string(translated))
			}
			if ac, ok := assistantMsg["content"].([]any); ok {
				astFound := false
				for _, b := range ac {
					bm := b.(map[string]any)
					if bm["cache_control"] != nil {
						astFound = true
					}
				}
				if !astFound {
					t.Errorf("assistant cache_control marker did not survive: %s", string(translated))
				}
			} else {
				t.Errorf("assistant content not a block slice: %v", assistantMsg["content"])
			}

			// tool marker
			tools, _ := body["tools"].([]any)
			if len(tools) != 1 {
				t.Fatalf("expected 1 tool, got %d", len(tools))
			}
			tool, _ := tools[0].(map[string]any)
			if tool["cache_control"] == nil {
				t.Errorf("tool lost cache_control: %s", string(translated))
			}
		})
	}
}

// TestDashScopeBillingOnlySystemTransfersCacheControl checks that a system block
// reduced to "" by billing-header stripping transfers its cache_control marker
// to the preceding retained block instead of emitting an empty text block.
func TestDashScopeBillingOnlySystemTransfersCacheControl(t *testing.T) {
	claudeBody := []byte(`{
		"model": "qwen3-coder",
		"system": [
			{"type": "text", "text": "sys1"},
			{"type": "text", "text": "x-anthropic-billing-header: cc_version=1;\n", "cache_control": {"type": "ephemeral"}}
		],
		"messages": [
			{"role": "user", "content": "hi"}
		]
	}`)

	translated, err := TranslateClaudeToOpenAI(claudeBody, "alicode", "qwen3-coder")
	if err != nil {
		t.Fatalf("TranslateClaudeToOpenAI: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(translated, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	sysMsg := findMessageByRole(body, "system")
	if sysMsg == nil {
		t.Fatalf("no system message: %s", string(translated))
	}
	sysContent, ok := sysMsg["content"].([]any)
	if !ok {
		t.Fatalf("system content not a block slice: %v", sysMsg["content"])
	}
	if len(sysContent) != 1 {
		t.Fatalf("expected 1 system block, got %d: %s", len(sysContent), string(translated))
	}
	block, _ := sysContent[0].(map[string]any)
	if block["text"] != "sys1" {
		t.Errorf("unexpected system text: %v", block["text"])
	}
	if block["cache_control"] == nil {
		t.Errorf("cache_control marker did not transfer to preceding block: %s", string(translated))
	}
}

// TestDashScopeNonCacheProviderFlattens asserts groq (and other non-cache
// providers) still flatten system content and omit cache_control markers.
func TestDashScopeNonCacheProviderFlattens(t *testing.T) {
	claudeBody := []byte(`{
		"model": "llama",
		"system": [
			{"type": "text", "text": "sys1"},
			{"type": "text", "text": "sys2", "cache_control": {"type": "ephemeral"}}
		],
		"messages": [
			{"role": "user", "content": [
				{"type": "text", "text": "hello", "cache_control": {"type": "ephemeral"}}
			]}
		],
		"tools": [
			{"name": "lookup", "description": "look up", "input_schema": {"type": "object"}, "cache_control": {"type": "ephemeral"}}
		]
	}`)

	translated, err := TranslateClaudeToOpenAI(claudeBody, "groq", "llama-3.1-70b")
	if err != nil {
		t.Fatalf("TranslateClaudeToOpenAI: %v", err)
	}
	out := string(translated)
	if contains(out, "cache_control") {
		t.Errorf("groq output unexpectedly retained cache_control: %s", out)
	}
	var body map[string]any
	if err := json.Unmarshal(translated, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	sysMsg := findMessageByRole(body, "system")
	if sysMsg == nil {
		t.Fatalf("no system message: %s", out)
	}
	if _, ok := sysMsg["content"].(string); !ok {
		t.Errorf("groq system content not flattened to string: %v", sysMsg["content"])
	}
	userMsg := findMessageByRole(body, "user")
	if userMsg == nil {
		t.Fatalf("no user message: %s", out)
	}
	if _, ok := userMsg["content"].(string); !ok {
		t.Errorf("groq user content not flattened to string: %v", userMsg["content"])
	}
}

// TestDashScopeUnmarkedSingleTextStaysPlainString asserts unmarked single-text
// content is not wrapped into a content-block array.
func TestDashScopeUnmarkedSingleTextStaysPlainString(t *testing.T) {
	claudeBody := []byte(`{
		"model": "qwen3-coder",
		"system": "plain system",
		"messages": [
			{"role": "user", "content": "hi"}
		]
	}`)

	translated, err := TranslateClaudeToOpenAI(claudeBody, "alicode", "qwen3-coder")
	if err != nil {
		t.Fatalf("TranslateClaudeToOpenAI: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(translated, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	sysMsg := findMessageByRole(body, "system")
	if sysMsg == nil {
		t.Fatalf("no system message: %s", string(translated))
	}
	if _, ok := sysMsg["content"].(string); !ok {
		t.Errorf("system content not a plain string: %v", sysMsg["content"])
	}
	userMsg := findMessageByRole(body, "user")
	if userMsg == nil {
		t.Fatalf("no user message: %s", string(translated))
	}
	if _, ok := userMsg["content"].(string); !ok {
		t.Errorf("user content not a plain string: %v", userMsg["content"])
	}
}

// TestPreservesClaudeCacheControlMatchesProviders verifies the translator-level
// predicate agrees with the provider registry for the DashScope providers and
// the OpenRouter Anthropic lane.
func TestPreservesClaudeCacheControlMatchesProviders(t *testing.T) {
	for _, p := range dashscopeCacheProviders {
		if got := PreservesClaudeCacheControl(p, "any-model"); got != providers.PreservesCacheControl(p) {
			t.Errorf("provider %s: translator=%v providers=%v", p, got, providers.PreservesCacheControl(p))
		}
		if !providers.PreservesCacheControl(p) {
			t.Errorf("provider %s expected PreserveCacheControl=true in registry", p)
		}
	}
	if !PreservesClaudeCacheControl("openrouter", "anthropic/claude-3.5-sonnet") {
		t.Error("openrouter anthropic lane should preserve cache_control")
	}
	if PreservesClaudeCacheControl("groq", "llama-3.1-70b") {
		t.Error("groq should not preserve cache_control")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
