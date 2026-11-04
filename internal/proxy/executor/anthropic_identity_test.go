package executor

import (
	"strings"
	"testing"
)

func TestDropClaudeIdentitySystemBlockString(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"claude code identity", "You are Claude Code, Anthropic's official CLI for Claude.", ""},
		{"agent sdk identity", "Agent SDK", ""},
		{"billing header identity", "x-anthropic-billing-header: cc_version=1.0; cch=abcde;", ""},
		{"mixed claude code", "you are CLAUDE CODE issues", ""},
		{"official cli", "claude code official cli", ""},
		{"legitimate system text", "You are a helpful coding assistant.", "You are a helpful coding assistant."},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := dropClaudeIdentitySystemBlock(c.in)
			s, _ := got.(string)
			if s != c.want {
				t.Errorf("dropClaudeIdentitySystemBlock(%q) = %q, want %q", c.in, s, c.want)
			}
		})
	}
}

func TestDropClaudeIdentitySystemBlockArray(t *testing.T) {
	cases := []struct {
		name string
		in   []any
		want []any
	}{
		{
			name: "drop leading identity blocks then keep real one",
			in: []any{
				map[string]any{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude."},
				map[string]any{"type": "text", "text": "x-anthropic-billing-header: cc_version=1.0; cch=abcde;"},
				map[string]any{"type": "text", "text": "You are a helpful assistant."},
			},
			want: []any{
				map[string]any{"type": "text", "text": "You are a helpful assistant."},
			},
		},
		{
			name: "drop all identity blocks leaves empty",
			in: []any{
				map[string]any{"type": "text", "text": "Agent SDK"},
				map[string]any{"type": "text", "text": "you are claude code"},
			},
			want: []any{},
		},
		{
			name: "no identity keeps everything",
			in: []any{
				map[string]any{"type": "text", "text": "You are a helpful assistant."},
			},
			want: []any{
				map[string]any{"type": "text", "text": "You are a helpful assistant."},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := dropClaudeIdentitySystemBlock(c.in)
			gotBlocks, _ := got.([]any)
			if len(gotBlocks) != len(c.want) {
				t.Fatalf("got %d blocks, want %d: %#v", len(gotBlocks), len(c.want), gotBlocks)
			}
			for i, g := range gotBlocks {
				gm, _ := g.(map[string]any)
				wm, _ := c.want[i].(map[string]any)
				gt, _ := gm["text"].(string)
				wt, _ := wm["text"].(string)
				if gt != wt {
					t.Errorf("block %d text = %q, want %q", i, gt, wt)
				}
			}
		})
	}
}

func TestEnsureMessagesForDropsAnthropicSystem(t *testing.T) {
	body := `{
		"system": "You are Claude Code, Anthropic's official CLI for Claude.",
		"messages": [{"role": "user", "content": "hi"}],
		"model": "claude-sonnet-4-5",
		"max_tokens": 1024
	}`
	out := ensureMessagesFor([]byte(body), "anthropic", "claude-sonnet-4-5")
	if strings.Contains(string(out), "You are Claude Code") {
		t.Fatalf("anthropic system block was not dropped:\n%s", out)
	}
}

func TestEnsureMessagesForKeepsNonAnthropicSystem(t *testing.T) {
	body := `{
		"system": "You are Claude Code, Anthropic's official CLI for Claude.",
		"messages": [{"role": "user", "content": "hi"}],
		"model": "claude-sonnet-4-5",
		"max_tokens": 1024
	}`
	out := ensureMessagesFor([]byte(body), "opencode-zen", "claude-sonnet-4-5")
	if !strings.Contains(string(out), "You are Claude Code") {
		t.Fatalf("non-anthropic system block was dropped:\n%s", out)
	}
}

func TestEnsureMessagesForDropsArrayLeadingIdentity(t *testing.T) {
	body := `{
		"system": [
			{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude."},
			{"type": "text", "text": "You are a helpful assistant."}
		],
		"messages": [{"role": "user", "content": "hi"}],
		"model": "claude-sonnet-4-5",
		"max_tokens": 1024
	}`
	out := ensureMessagesFor([]byte(body), "anthropic", "claude-sonnet-4-5")
	if strings.Contains(string(out), "You are Claude Code") {
		t.Fatalf("leading identity block not dropped:\n%s", out)
	}
	if !strings.Contains(string(out), "You are a helpful assistant") {
		t.Fatalf("legitimate system text dropped:\n%s", out)
	}
}
