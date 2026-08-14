package proxy_test

import (
	"testing"
	"9router/proxy/internal/proxy"
)

func TestShouldBypassNoProxy(t *testing.T) {
	tests := []struct {
		target   string
		noProxy  string
		expected bool
	}{
		{"https://api.openai.com/v1", "", false},
		{"https://api.openai.com/v1", "*", true},
		{"https://api.openai.com/v1", "api.openai.com", true},
		{"https://api.openai.com/v1", ".openai.com", true},
		{"https://api.openai.com/v1", "openai.com", true},
		{"https://localhost:8080/v1", "localhost,127.0.0.1", true},
		{"https://api.anthropic.com/v1", "api.openai.com,localhost", false},
	}

	for _, tt := range tests {
		got := proxy.ShouldBypassNoProxy(tt.target, tt.noProxy)
		if got != tt.expected {
			t.Errorf("ShouldBypassNoProxy(%q, %q) = %v, expected %v", tt.target, tt.noProxy, got, tt.expected)
		}
	}
}

func TestBuildEdgeRelayHeaders(t *testing.T) {
	targetURL := "https://api.openai.com/v1/chat/completions"
	relayHeaders := proxy.BuildEdgeRelayHeaders(targetURL, map[string]string{
		"Authorization": "Bearer key",
	})

	if relayHeaders["x-relay-target"] != "https://api.openai.com" {
		t.Errorf("expected x-relay-target https://api.openai.com, got %s", relayHeaders["x-relay-target"])
	}
	if relayHeaders["x-relay-path"] != "/v1/chat/completions" {
		t.Errorf("expected x-relay-path /v1/chat/completions, got %s", relayHeaders["x-relay-path"])
	}
	if relayHeaders["Authorization"] != "Bearer key" {
		t.Errorf("expected Authorization preserved, got %s", relayHeaders["Authorization"])
	}
}
