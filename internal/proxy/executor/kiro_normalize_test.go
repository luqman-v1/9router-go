package executor

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

func TestNormalizeKiroUpstreamModelId(t *testing.T) {
	cases := []struct{ in, want string }{
		// Date-suffixed snapshot ids Claude Code and cc-switch send.
		{"claude-sonnet-4-5-20250929", "claude-sonnet-4.5"},
		{"claude-opus-4-5-20251101", "claude-opus-4.5"},
		{"claude-haiku-4-5-20251001", "claude-haiku-4.5"},
		{"claude-3-5-sonnet-20241022", "claude-sonnet-4.5"},
		// Dash-separated Anthropic spellings.
		{"claude-sonnet-4-5", "claude-sonnet-4.5"},
		{"claude-opus-4-5", "claude-opus-4.5"},
		{"claude-opus-4-8", "claude-opus-4.8"},
		{"claude-opus-5-5", "claude-opus-5.5"},
		{"claude-sonnet-5", "claude-sonnet-5"},
		{"claude-fable-5", "claude-fable-5"},
		// The static catalog's capitalized MiniMax id 400s upstream.
		{"MiniMax-M2.5", "minimax-m2.5"},
		// Canonical ids stay canonical.
		{"auto", "auto"},
		{"deepseek-3.2", "deepseek-3.2"},
		{"glm-5", "glm-5"},
		{"qwen3-coder-next", "qwen3-coder-next"},
		{"kimi-k3", "kimi-k3"},
		{"minimax-m2.5", "minimax-m2.5"},
		{"minimax-m2.1", "minimax-m2.1"},
		{"gpt-5.6-sol", "gpt-5.6-sol"},
		{"gpt-5.6-terra", "gpt-5.6-terra"},
		{"gpt-5.6-luna", "gpt-5.6-luna"},
		// Unknown ids route through untouched.
		{"some-future-model", "some-future-model"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := normalizeKiroUpstreamModelId(tc.in); got != tc.want {
			t.Errorf("normalizeKiroUpstreamModelId(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestKiroUpstreamBody_NormalizesAnthropicSnapshotId(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-5-20250929","messages":[{"role":"user","content":"hi"}]}`)
	out, err := kiroUpstreamBody(&Request{Body: body, ModelName: "claude-sonnet-4-5-20250929"})
	if err != nil {
		t.Fatalf("kiroUpstreamBody: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if _, ok := m["conversationState"]; !ok {
		t.Fatalf("missing conversationState: %s", out)
	}
	// The envelope stamps the id at every turn; none may carry the snapshot id.
	if strings.Contains(string(out), "20250929") {
		t.Errorf("snapshot id leaked into the envelope: %s", out)
	}
	if !strings.Contains(string(out), `"modelId":"claude-sonnet-4.5"`) {
		t.Errorf("envelope does not carry the dotted alias: %s", out)
	}
}
