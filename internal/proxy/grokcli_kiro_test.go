package proxy

import (
	"strings"
	"testing"

	"9router/proxy/internal/providers"
)

// Upstream tries the Amazon surfaces before the Kiro IDE gateway
// (open-sse/executors/kiro.js getOrderedBaseUrls), regionalizing the AWS hosts.
func TestKiroEndpointsOrdering(t *testing.T) {
	t.Run("amazon surfaces first, IDE gateway last", func(t *testing.T) {
		got := kiroEndpoints(&providers.ProviderConfig{}, nil)
		if len(got) != 3 {
			t.Fatalf("expected 3 kiro endpoints, got %d (%v)", len(got), got)
		}
		if !strings.Contains(got[0], "://q.") {
			t.Errorf("expected the q surface first, got %q", got[0])
		}
		if !strings.Contains(got[1], "://codewhisperer.") {
			t.Errorf("expected the codewhisperer surface second, got %q", got[1])
		}
		if !strings.Contains(got[2], "kiro.dev") {
			t.Errorf("expected the Kiro IDE gateway last, got %q", got[2])
		}
	})

	t.Run("aws hosts follow the account region only when covered", func(t *testing.T) {
		got := kiroEndpoints(&providers.ProviderConfig{}, map[string]any{"region": "eu-central-1"})
		if !strings.Contains(got[0], "q.eu-central-1.amazonaws.com") {
			t.Errorf("expected the q host in eu-central-1, got %q", got[0])
		}
		if !strings.Contains(got[1], "codewhisperer.us-east-1.amazonaws.com") {
			t.Errorf("expected codewhisperer to stay us-east-1 (only region served), got %q", got[1])
		}
	})

	t.Run("never builds a host in a region that service does not serve", func(t *testing.T) {
		got := kiroEndpoints(&providers.ProviderConfig{}, map[string]any{"region": "ap-southeast-1"})
		if !strings.Contains(got[0], "q.us-east-1.amazonaws.com") {
			t.Errorf("expected q to stay us-east-1 for uncovered ap-southeast-1, got %q", got[0])
		}
		if !strings.Contains(got[1], "codewhisperer.us-east-1.amazonaws.com") {
			t.Errorf("expected codewhisperer to stay us-east-1, got %q", got[1])
		}
	})

	t.Run("refuses to splice an attacker-controlled region into hostname", func(t *testing.T) {
		got := kiroEndpoints(&providers.ProviderConfig{}, map[string]any{"region": "x.attacker.tld#"})
		for _, u := range got {
			if strings.Contains(u, "attacker") {
				t.Errorf("hostile region leaked into endpoint %q", u)
			}
		}
	})

	t.Run("a configured non-amazon mirror is the only endpoint", func(t *testing.T) {
		cfg := &providers.ProviderConfig{BaseURL: "https://mirror.internal/kiro"}
		got := kiroEndpoints(cfg, nil)
		if len(got) != 1 || got[0] != "https://mirror.internal/kiro" {
			t.Errorf("expected only the configured mirror, got %v", got)
		}
	})
}

func TestKiroTokenType(t *testing.T) {
	tests := []struct {
		authMethod string
		want       string
	}{
		{authMethod: "api_key", want: "API_KEY"},
		{authMethod: "external_idp", want: "EXTERNAL_IDP"},
		{authMethod: "imported", want: ""},
		{authMethod: "builder-id", want: ""},
		{authMethod: "", want: ""},
	}
	for _, tt := range tests {
		psd := map[string]any{}
		if tt.authMethod != "" {
			psd["authMethod"] = tt.authMethod
		}
		if got := kiroTokenType(psd); got != tt.want {
			t.Errorf("kiroTokenType(%q) = %q, want %q", tt.authMethod, got, tt.want)
		}
	}
}

// Upstream KIRO_ENDPOINT_FALLBACK_STATUSES: 400 is a payload problem and must
// never be retried against another surface.
func TestKiroEndpointFallbackStatus(t *testing.T) {
	fallback := []int{401, 403, 404}
	for _, status := range fallback {
		if !kiroEndpointFallbackStatus(status) {
			t.Errorf("expected %d to rotate to the next kiro surface", status)
		}
	}
	for _, status := range []int{200, 400, 429, 500, 503} {
		if kiroEndpointFallbackStatus(status) {
			t.Errorf("expected %d to be terminal for kiro", status)
		}
	}
}
