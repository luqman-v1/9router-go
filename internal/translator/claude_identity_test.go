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

// A middle-of-history system turn is folded into the same string the identity
// banner lives in. Cutting the whole string would take the reminder with it —
// a silent deletion, and the client never learns the directive was lost.
func TestSanitizeClaudePassthroughKeepsTextAppendedAfterIdentityBanner(t *testing.T) {
	body := `{
		"system": "You are Claude Code, Anthropic's official CLI for Claude.\n\nIMPORTANT REMINDER: answer in French.",
		"messages": [{"role": "user", "content": "hi"}],
		"model": "claude-sonnet-4-5",
		"max_tokens": 1024
	}`
	out := SanitizeClaudePassthrough([]byte(body), false)
	if strings.Contains(string(out), "You are Claude Code") {
		t.Fatalf("the identity banner must still be removed: %s", out)
	}
	if !strings.Contains(string(out), "IMPORTANT REMINDER") {
		t.Fatalf("text appended after the banner was dropped: %s", out)
	}
}

// The probe the reviewer ran: a banner-only string system plus a system turn
// mid-history. Lifting the turn appends it to that same string, and the
// identity pass must leave it there.
func TestSanitizeClaudePassthroughKeepsLiftedSystemTurnUnderBanner(t *testing.T) {
	body := `{
		"system": "You are Claude Code, Anthropic's official CLI for Claude.",
		"max_tokens": 64,
		"messages": [
			{"role": "user", "content": "one"},
			{"role": "assistant", "content": "two"},
			{"role": "system", "content": "IMPORTANT REMINDER: answer in French."},
			{"role": "user", "content": "three"}
		]
	}`
	out := string(SanitizeClaudePassthrough([]byte(body), false))
	if strings.Contains(out, "You are Claude Code") {
		t.Fatalf("the identity banner must still be removed: %s", out)
	}
	if !strings.Contains(out, "IMPORTANT REMINDER") {
		t.Fatalf("the lifted reminder was dropped: %s", out)
	}
}
