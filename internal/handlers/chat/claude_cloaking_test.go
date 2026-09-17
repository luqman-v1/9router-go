package chat

import (
	json "encoding/json/v2"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
)

func TestIsAnthropicUpstream(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		cfg      *providers.ProviderConfig
		want     bool
	}{
		{
			name:     "direct claude messages",
			provider: "claude",
			cfg:      &providers.ProviderConfig{BaseURL: "https://api.anthropic.com/v1/messages"},
			want:     true,
		},
		{
			name:     "direct anthropic messages with query",
			provider: "anthropic",
			cfg:      &providers.ProviderConfig{BaseURL: "https://api.anthropic.com/v1/messages?beta=true"},
			want:     true,
		},
		{
			name:     "edge relay forward to anthropic",
			provider: "claude",
			cfg: &providers.ProviderConfig{
				BaseURL: "https://my-relay.vercel.app",
				StaticHeaders: map[string]string{
					"x-relay-target": "https://api.anthropic.com",
					"x-relay-path":   "/v1/messages",
				},
			},
			want: true,
		},
		{
			name:     "edge relay forward with existing query",
			provider: "anthropic",
			cfg: &providers.ProviderConfig{
				BaseURL: "https://my-relay.vercel.app",
				StaticHeaders: map[string]string{
					"x-relay-target": "https://api.anthropic.com",
					"x-relay-path":   "/v1/messages?beta=true",
				},
			},
			want: true,
		},
		{
			name:     "non-anthropic provider",
			provider: "openai",
			cfg:      &providers.ProviderConfig{BaseURL: "https://api.anthropic.com/v1/messages"},
			want:     false,
		},
		{
			name:     "anthropic custom endpoint",
			provider: "claude",
			cfg:      &providers.ProviderConfig{BaseURL: "https://custom.host/v1/messages"},
			want:     false,
		},
		{
			name:     "nil config",
			provider: "claude",
			cfg:      nil,
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAnthropicUpstream(tt.provider, tt.cfg); got != tt.want {
				t.Fatalf("isAnthropicUpstream(%s, %+v) = %v, want %v", tt.provider, tt.cfg, got, tt.want)
			}
		})
	}
}

func TestAppendBetaQuery(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{
			input: "https://api.anthropic.com/v1/messages",
			want:  "https://api.anthropic.com/v1/messages?beta=true",
		},
		{
			input: "/v1/messages",
			want:  "/v1/messages?beta=true",
		},
		{
			input: "https://api.anthropic.com/v1/messages?existing=1",
			want:  "https://api.anthropic.com/v1/messages?existing=1&beta=true",
		},
		{
			input: "/v1/messages?beta=true",
			want:  "/v1/messages?beta=true",
		},
		{
			input: "https://api.anthropic.com/v1/messages?foo=bar&beta=true",
			want:  "https://api.anthropic.com/v1/messages?foo=bar&beta=true",
		},
	}

	for _, tt := range tests {
		if got := appendBetaQuery(tt.input); got != tt.want {
			t.Errorf("appendBetaQuery(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestApplyClaudeCloaking(t *testing.T) {
	body := []byte(`{"model":"claude-3-opus-20240229","messages":[{"role":"user","content":"hello"}]}`)
	out := applyClaudeCloaking(body, "oauth-token-123", "sess-1")
	if string(out) == string(body) {
		t.Fatalf("expected cloaking to modify body")
	}
	s := string(out)
	if !strings.Contains(s, "x-anthropic-billing-header:") || !strings.Contains(s, "metadata") {
		t.Fatalf("missing cloaked headers in cloaked request: %s", s)
	}
}

func TestStandardKey_SkipsToolCloakingAndDecoys(t *testing.T) {
	body := []byte(`{
		"model": "claude-3-opus-20240229",
		"messages": [{"role": "user", "content": "run calc"}],
		"tools": [{"name": "calculator", "description": "calc"}]
	}`)
	apiKey := "sk-ant-api03-regular-key"
	isOAuth := false

	pipedBody := body
	var claudeToolMap map[string]string
	if isOAuth || strings.Contains(apiKey, "sk-ant-oat") {
		pipedBody = applyClaudeCloaking(pipedBody, apiKey, "sess-1")
		var reqMap map[string]any
		if err := json.Unmarshal(pipedBody, &reqMap); err == nil {
			claudeToolMap = cloakClaudeTools(reqMap)
			if out, err := json.Marshal(reqMap); err == nil {
				pipedBody = out
			}
		}
	}

	if claudeToolMap != nil {
		t.Fatalf("expected nil claudeToolMap for standard key, got %v", claudeToolMap)
	}
	if string(pipedBody) != string(body) {
		t.Fatalf("expected body unchanged for standard key, got %s", string(pipedBody))
	}
}

func TestOAuthKey_AppliesToolCloakingAndDecoys(t *testing.T) {
	body := []byte(`{
		"model": "claude-3-opus-20240229",
		"messages": [{"role": "user", "content": "run calc"}],
		"tools": [{"name": "calculator", "description": "calc"}]
	}`)
	apiKey := "sk-ant-oat-token"
	isOAuth := true

	pipedBody := body
	var claudeToolMap map[string]string
	if isOAuth || strings.Contains(apiKey, "sk-ant-oat") {
		pipedBody = applyClaudeCloaking(pipedBody, apiKey, "sess-1")
		var reqMap map[string]any
		if err := json.Unmarshal(pipedBody, &reqMap); err == nil {
			claudeToolMap = cloakClaudeTools(reqMap)
			if out, err := json.Marshal(reqMap); err == nil {
				pipedBody = out
			}
		}
	}

	if claudeToolMap == nil || claudeToolMap["calculator_ide"] != "calculator" {
		t.Fatalf("expected calculator_ide mapped to calculator, got %v", claudeToolMap)
	}
	s := string(pipedBody)
	if !strings.Contains(s, "calculator_ide") {
		t.Fatalf("tools should be suffixed with _ide: %s", s)
	}
	if !strings.Contains(s, `"name":"Task"`) {
		t.Fatalf("decoy tools should be injected for OAuth: %s", s)
	}
}
