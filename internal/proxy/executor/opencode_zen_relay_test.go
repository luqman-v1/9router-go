package executor

import (
	"testing"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
)

// zenRelayPath is the single place that decides which lane a relay forwards to.
func TestZenRelayPath(t *testing.T) {
	tests := []struct {
		name  string
		heads map[string]string
		model string
		lane  string
		want  string
	}{
		{
			name:  "an origin-only target falls back to the zen lane",
			heads: map[string]string{"x-relay-target": "https://opencode.ai"},
			model: "deepseek-v4-pro",
			lane:  zenResponsesPath,
			want:  "/zen/v1/responses",
		},
		{
			name:  "an origin with a trailing slash still resolves the lane",
			heads: map[string]string{"x-relay-target": "https://opencode.ai/"},
			model: "deepseek-v4-pro",
			lane:  zenResponsesPath,
			want:  "/zen/v1/responses",
		},
		{
			name:  "the messages lane resolves independently",
			heads: map[string]string{"x-relay-target": "https://opencode.ai"},
			model: "claude-sonnet-4-5",
			lane:  zenMessagesPath,
			want:  "/zen/v1/messages",
		},
		{
			name:  "no relay headers at all",
			heads: map[string]string{},
			model: "deepseek-v4-pro",
			lane:  zenMessagesPath,
			want:  "/zen/v1/messages",
		},
		{
			name:  "a nil config",
			heads: nil,
			model: "deepseek-v4-pro",
			lane:  zenResponsesPath,
			want:  "/zen/v1/responses",
		},
		{
			name:  "a free-tier model carries the inference prefix",
			heads: map[string]string{"x-relay-target": "https://opencode.ai"},
			model: "deepseek-v4-flash-free",
			lane:  "/chat/completions",
			want:  "/inference/openai/v1/chat/completions",
		},
		{
			name:  "a free-tier responses model carries the inference prefix",
			heads: map[string]string{"x-relay-target": "https://opencode.ai"},
			model: "muse-spark-1.3-contributor-free",
			lane:  zenResponsesPath,
			want:  "/inference/openai/v1/responses",
		},
		{
			name:  "a free-tier messages model keeps the legacy inference prefix",
			heads: map[string]string{"x-relay-target": "https://opencode.ai"},
			model: "claude-sonnet-4-6-free",
			lane:  zenMessagesPath,
			want:  "/inference/v1/messages",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &providers.ProviderConfig{BaseURL: "https://relay.example", StaticHeaders: tt.heads}
			if got := zenRelayPath(cfg, tt.model, tt.lane); got != tt.want {
				t.Errorf("zenRelayPath = %q, want %q", got, tt.want)
			}
		})
	}
}

// The relay headers are the whole point of an edge pool: dropping them turns
// every request into a 400 from the relay itself.
func TestZenHeadersCarriesRelayHeaders(t *testing.T) {
	cfg := &providers.ProviderConfig{
		StaticHeaders: map[string]string{
			"x-relay-target":     "https://opencode.ai",
			"x-relay-path":       "/zen/v1/responses",
			"x-opencode-client":  "cli",
			"x-opencode-project": "proj-123",
			"User-Agent":         "ignored-by-fingerprint",
		},
	}
	headers := zenHeaders(cfg, "session-1", true, false)
	for k, want := range map[string]string{
		"x-relay-target":     "https://opencode.ai",
		"x-relay-path":       "/zen/v1/responses",
		"x-opencode-client":  "cli",
		"x-opencode-project": "proj-123",
	} {
		if got := headers[k]; got != want {
			t.Errorf("header %q = %q, want %q", k, got, want)
		}
	}
	// The fingerprint UA still wins over a connection-supplied one — and on the
	// keyed zen lane that fingerprint is the v1 identity (see zenHeaders).
	if headers["User-Agent"] != proxy.DefaultOpenCodeV1UA {
		t.Errorf("User-Agent = %q, want the official fingerprint %q", headers["User-Agent"], proxy.DefaultOpenCodeV1UA)
	}
}
