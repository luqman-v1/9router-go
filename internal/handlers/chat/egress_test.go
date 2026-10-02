package chat

import (
	"strings"
	"testing"

	"9router/proxy/internal/providers"
)

// "Did this request use the proxy?" has to be answerable from one usage line,
// and answered in terms an operator recognises. It was neither: the only proxy
// log was once-per-pool at debug level, and even the pool id that eventually
// replaced it is a UUID nobody maps to a name in the dashboard.

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
			name: "a relay pool is named, not printed as a UUID",
			connData: &ConnectionData{
				ProxyPoolID:        "06a2c494-ef06-4d3f-a034-dad29ff3aebf",
				ResolvedProxyPool:  "vercel-relay",
			},
			cfg: &providers.ProviderConfig{
				BaseURL: "https://vercel-relay.vercel.app",
				StaticHeaders: map[string]string{
					"x-relay-target": "https://opencode.ai",
					"x-relay-path":   "/zen/v1/responses",
				},
			},
			// The operator named the pool in the dashboard; that name is what they
			// will recognise when grepping a log for a slow or blocked request.
			wantKind: "relay", wantValue: "vercel-relay",
		},
		{
			name: "a pool with no name falls back to its id",
			connData: &ConnectionData{
				ProxyPoolID: "06a2c494-ef06-4d3f-a034-dad29ff3aebf",
			},
			cfg: &providers.ProviderConfig{
				BaseURL:       "https://relay.vercel.app",
				StaticHeaders: map[string]string{"x-relay-target": "https://opencode.ai"},
			},
			wantKind: "relay", wantValue: "06a2c494-ef06-4d3f-a034-dad29ff3aebf",
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
			name:      "a nil connection without a relay is direct",
			connData:  nil,
			cfg:       &providers.ProviderConfig{BaseURL: "https://api.openai.com/v1/chat/completions"},
			wantKind:  "direct",
			wantValue: "direct",
		},
		{
			name: "a relay with no connection row falls back to the relay host",
			connData: nil,
			cfg: &providers.ProviderConfig{
				BaseURL:       "https://relay.vercel.app",
				StaticHeaders: map[string]string{"x-relay-target": "https://api.openai.com"},
			},
			wantKind:  "relay",
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

// The label lands on every usage row, so it has to stay readable in a log line:
// a pool name with a newline in it would forge extra log entries.
func TestResolveEgress_KeepsTheLabelSingleLine(t *testing.T) {
	e := resolveEgress(&ConnectionData{
		ProxyPoolID:       "p1",
		ResolvedProxyPool: "sg\nINF [usage] forged line",
	}, &providers.ProviderConfig{
		BaseURL:       "https://relay.example",
		StaticHeaders: map[string]string{"x-relay-target": "https://api.openai.com"},
	})
	if got := e.LogValue(); strings.ContainsAny(got, "\r\n") {
		t.Errorf("a pool name with a newline would forge log lines, got %q", got)
	}
}

func TestAssignedPoolIsNilSafe(t *testing.T) {
	id, name := assignedPool(nil)
	if id != "" || name != "" {
		t.Errorf("assignedPool(nil) = %q/%q, want empty", id, name)
	}
	id, name = assignedPool(&ConnectionData{ProxyPoolID: "p1", ResolvedProxyPool: "sg"})
	if id != "p1" || name != "sg" {
		t.Errorf("assignedPool = %q/%q, want p1/sg", id, name)
	}
}
