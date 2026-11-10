package handlerutil

import (
	"testing"

	"9router/proxy/internal/constants"
)

func TestWithDefaultUserAgent(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
		wantUA  string
	}{
		{
			name:    "nil headers get the default",
			headers: nil,
			wantUA:  constants.DefaultClientUserAgent,
		},
		{
			name:    "headers without a User-Agent get the default",
			headers: map[string]string{"Authorization": "Bearer k"},
			wantUA:  constants.DefaultClientUserAgent,
		},
		{
			name:    "a caller-supplied identity wins",
			headers: map[string]string{"User-Agent": "antigravity/cli/1.5.1"},
			wantUA:  "antigravity/cli/1.5.1",
		},
		{
			name:    "a lowercase header name still counts as set",
			headers: map[string]string{"user-agent": "grok-shell/1.0.44"},
			wantUA:  "grok-shell/1.0.44",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := WithDefaultUserAgent(tt.headers)
			ua := got["User-Agent"]
			if ua == "" {
				ua = got["user-agent"]
			}
			if ua != tt.wantUA {
				t.Errorf("User-Agent = %q, want %q", ua, tt.wantUA)
			}
		})
	}
}

// The dashboard's probe configs share one header map across concurrent
// requests, so adding the default must not write through to the caller's map.
func TestWithDefaultUserAgentDoesNotMutateInput(t *testing.T) {
	headers := map[string]string{"Authorization": "Bearer k"}
	got := WithDefaultUserAgent(headers)

	if _, ok := headers["User-Agent"]; ok {
		t.Fatal("input map was mutated")
	}
	if got["User-Agent"] != constants.DefaultClientUserAgent {
		t.Fatalf("returned map lacks the default: %v", got)
	}
	if got["Authorization"] != "Bearer k" {
		t.Fatalf("returned map dropped a caller header: %v", got)
	}
}

// A map that already carries an identity is returned as-is, which is what keeps
// the shared probe configs from being copied on every request.
func TestWithDefaultUserAgentReturnsInputWhenSet(t *testing.T) {
	headers := map[string]string{"User-Agent": "node"}
	if got := WithDefaultUserAgent(headers); len(got) != len(headers) {
		t.Fatalf("expected the input map back, got %v", got)
	}
}
