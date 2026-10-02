package chat

import (
	"testing"

	"9router/proxy/internal/providers"
)

// "Did this request use the proxy?" has to be answerable from one usage line.
// It was not: the only proxy log was once-per-pool at debug level, so a second
// request through the same pool printed nothing, and the usage rows carried no
// proxy field at all. These cases pin the resolution that closes it.

func TestResolveEgress_ReportsThePathARequestLeftBy(t *testing.T) {
	tests := []struct {
		name      string
		connData  *ConnectionData
		cfg       *providers.ProviderConfig
		wantKind  string
		wantValue string
	}{
		{
			name:     "a connection with no proxy is direct",
			connData: &ConnectionData{},
			cfg:      &providers.ProviderConfig{BaseURL: "https://api.openai.com/v1/chat/completions"},
			wantKind: "direct", wantValue: "direct",
		},
		{
			name: "a relay pool is reported as the relay host",
			connData: &ConnectionData{
				ProxyPoolID: "pool-1",
			},
			cfg: &providers.ProviderConfig{
				BaseURL: "https://relay.vercel.app",
				StaticHeaders: map[string]string{
					"x-relay-target": "https://api.openai.com",
					"x-relay-path":   "/v1/chat/completions",
				},
			},
			// The pool id is what an operator looks for in the dashboard, so it
			// outranks the relay URL in the log line.
			wantKind: "relay", wantValue: "pool-1",
		},
		{
			name: "a legacy connection proxy is reported by URL",
			connData: &ConnectionData{
				ConnectionProxyEnabled: true,
				ConnectionProxyURL:     "http://proxy.example:8080",
			},
			cfg:      &providers.ProviderConfig{BaseURL: "https://api.openai.com/v1/chat/completions"},
			wantKind: "http", wantValue: "http://proxy.example:8080",
		},
		{
			name: "a proxy that is enabled but has no URL is not an egress",
			connData: &ConnectionData{
				ConnectionProxyEnabled: true,
				ConnectionProxyURL:     "   ",
			},
			cfg:      &providers.ProviderConfig{BaseURL: "https://api.openai.com/v1/chat/completions"},
			wantKind: "direct", wantValue: "direct",
		},
		{
			name:     "a nil connection without a relay is direct",
			connData: nil,
			cfg:      &providers.ProviderConfig{BaseURL: "https://api.openai.com/v1/chat/completions"},
			wantKind: "direct", wantValue: "direct",
		},
		{
			name:     "a relay is still a relay with no connection row",
			connData: nil,
			cfg:      &providers.ProviderConfig{BaseURL: "https://relay.vercel.app", StaticHeaders: map[string]string{"x-relay-target": "https://api.openai.com"}},
			wantKind: "relay",
			// No pool to attribute it to, so the relay host is the best answer.
			wantValue: "https://relay.vercel.app",
		},
		{
			name: "providerSpecificData carries the legacy proxy",
			connData: &ConnectionData{
				ProviderSpecificData: map[string]any{
					"connectionProxyEnabled": true,
					"connectionProxyUrl":     "http://legacy.proxy:3128",
				},
			},
			cfg:      &providers.ProviderConfig{BaseURL: "https://api.openai.com/v1/chat/completions"},
			wantKind: "http", wantValue: "http://legacy.proxy:3128",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveEgress(tt.connData, tt.cfg)
			if got.Kind != tt.wantKind {
				t.Errorf("Kind = %q, want %q", got.Kind, tt.wantKind)
			}
			if got.LogValue() != tt.wantValue {
				t.Errorf("LogValue() = %q, want %q", got.LogValue(), tt.wantValue)
			}
		})
	}
}

// A relay config carries the upstream in a header, so the relay host is in
// BaseURL. A plain proxy config has no such marker, which is exactly why the
// resolver cannot read the proxy out of the provider config alone.
func TestResolveEgress_TargetIsTheRelayHostNotTheProvider(t *testing.T) {
	e := resolveEgress(&ConnectionData{ProxyPoolID: "pool-9"}, &providers.ProviderConfig{
		BaseURL:       "https://my-relay.vercel.app",
		StaticHeaders: map[string]string{"x-relay-target": "https://api.openai.com"},
	})
	if e.Target == "https://api.openai.com" {
		t.Error("Target must be the relay host — that is where the request was addressed")
	}
	if e.Target != "https://my-relay.vercel.app" {
		t.Errorf("Target = %q, want the relay host", e.Target)
	}
}

func TestAssignedPoolIDIsNilSafe(t *testing.T) {
	if got := assignedPoolID(nil); got != "" {
		t.Errorf("assignedPoolID(nil) = %q, want empty", got)
	}
	if got := assignedPoolID(&ConnectionData{ProxyPoolID: "p1"}); got != "p1" {
		t.Errorf("assignedPoolID = %q, want p1", got)
	}
}
