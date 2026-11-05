package translator

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSanitizeClaudePassthroughDropsClaudeCodeIdentitySystem(t *testing.T) {
	body := `{
		"system": "You are Claude Code, Anthropic's official CLI for Claude.",
		"messages": [{"role": "user", "content": "hi"}],
		"model": "claude-sonnet-4-5",
		"max_tokens": 1024
	}`
	out := SanitizeClaudePassthrough([]byte(body), false)
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if _, ok := m["system"]; ok {
		t.Fatalf("anthropic system identity was not removed: %s", out)
	}
}

func TestSanitizeClaudePassthroughKeepsNonIdentitySystem(t *testing.T) {
	body := `{
		"system": "You are a helpful coding assistant.",
		"messages": [{"role": "user", "content": "hi"}],
		"model": "claude-sonnet-4-5",
		"max_tokens": 1024
	}`
	out := SanitizeClaudePassthrough([]byte(body), false)
	if !strings.Contains(string(out), "You are a helpful coding assistant.") {
		t.Fatalf("legitimate system text was dropped: %s", out)
	}
}

func TestSanitizeClaudePassthroughPreservesAgentSDKUserPrompt(t *testing.T) {
	body := `{
		"system": "expert on Anthropic Agent SDK",
		"messages": [{"role": "user", "content": "hi"}],
		"model": "claude-sonnet-4-5",
		"max_tokens": 1024
	}`
	out := SanitizeClaudePassthrough([]byte(body), false)
	if !strings.Contains(string(out), "expert on Anthropic Agent SDK") {
		t.Fatalf("non-banner user prompt was dropped: %s", out)
	}
}

func TestSanitizeClaudePassthroughDropsArrayLeadingIdentity(t *testing.T) {
	body := `{
		"system": [
			{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude."},
			{"type": "text", "text": "You are a helpful assistant."}
		],
		"messages": [{"role": "user", "content": "hi"}],
		"model": "claude-sonnet-4-5",
		"max_tokens": 1024
	}`
	out := SanitizeClaudePassthrough([]byte(body), false)
	if strings.Contains(string(out), "You are Claude Code") {
		t.Fatalf("leading identity block not dropped: %s", out)
	}
	if !strings.Contains(string(out), "You are a helpful assistant") {
		t.Fatalf("legitimate system text dropped: %s", out)
	}
}

func TestSanitizeClaudePassthroughDropsAllIdentityArrayDeletesKey(t *testing.T) {
	body := `{
		"system": [
			{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude."},
			{"type": "text", "text": "you are claude code"}
		],
		"messages": [{"role": "user", "content": "hi"}],
		"model": "claude-sonnet-4-5",
		"max_tokens": 1024
	}`
	out := SanitizeClaudePassthrough([]byte(body), false)
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if _, ok := m["system"]; ok {
		t.Fatalf("system key not deleted after all-identity array: %s", out)
	}
}
