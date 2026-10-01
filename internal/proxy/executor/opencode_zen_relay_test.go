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
		lane  string
		want  string
	}{
		{
			name:  "an origin-only target falls back to the zen lane",
			heads: map[string]string{"x-relay-target": "https://opencode.ai"},
			lane:  zenResponsesPath,
			want:  "/zen/v1/responses",
		},
		{
			name:  "an origin with a trailing slash still resolves the lane",
			heads: map[string]string{"x-relay-target": "https://opencode.ai/"},
			lane:  zenResponsesPath,
			want:  "/zen/v1/responses",
		},
		{
			name:  "the messages lane resolves independently",
			heads: map[string]string{"x-relay-target": "https://opencode.ai"},
			lane:  zenMessagesPath,
			want:  "/zen/v1/messages",
		},
		{
			name:  "no relay headers at all",
			heads: map[string]string{},
			lane:  zenMessagesPath,
			want:  "/zen/v1/messages",
		},
		{
			name:  "a nil config",
			heads: nil,
			lane:  zenResponsesPath,
			want:  "/zen/v1/responses",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &providers.ProviderConfig{BaseURL: "https://relay.example", StaticHeaders: tt.heads}
			if got := zenRelayPath(cfg, tt.lane); got != tt.want {
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
	headers := zenHeaders(cfg, "session-1", true)
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
	// The fingerprint UA still wins over a connection-supplied one.
	if headers["User-Agent"] != proxy.DefaultOpenCodeUA {
		t.Errorf("User-Agent = %q, want the official fingerprint %q", headers["User-Agent"], proxy.DefaultOpenCodeUA)
	}
}
