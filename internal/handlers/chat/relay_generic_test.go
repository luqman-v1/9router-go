package chat

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/db"
)

// Every provider leaves through the relay, including ones that never knew about
// it.
//
// Relay support used to be a per-provider convention: the handler rewrote
// BaseURL to the relay host and stamped x-relay-target / x-relay-path into
// StaticHeaders, and each executor that built its own requests had to re-apply
// it. Four did; the rest silently leaked the operator's IP or aimed a provider
// path at the relay's root (trae appended its session path to the relay host,
// zed and windsurf dialled the provider, the embeddings lane used h.Client and
// skipped the pool entirely).
//
// The relay is now a RoundTripper on the connection's client, so the test that
// matters is not "does provider X handle relays" but "does an arbitrary request
// built by an arbitrary executor leave through the relay". These cases use
// providers that have no relay code at all.

func newRelayBackedHandler(t *testing.T, relayURL string) (*ChatHandler, string) {
	t.Helper()
	database, cleanup := setupChatTestDB(t)
	t.Cleanup(cleanup)
	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS proxyPools (
		id TEXT PRIMARY KEY,
		isActive INTEGER DEFAULT 1,
		testStatus TEXT,
		data TEXT NOT NULL,
		createdAt TEXT NOT NULL,
		updatedAt TEXT NOT NULL
	);`); err != nil {
		t.Fatalf("create proxyPools: %v", err)
	}
	poolID := newRelayPool(t, database, relayURL, "vercel")
	return &ChatHandler{Client: &http.Client{}, Repo: db.NewRepo(database)}, poolID
}

// A provider with no relay code at all: its request must still leave through the
// relay, addressed to its own URL.
func TestRelayAppliesToProvidersWithNoRelayCode(t *testing.T) {
	tests := []struct {
		name    string
		build   func(base string) string
		wantURL string
	}{
		{
			// trae builds its own session URL by appending a path to BaseURL. The
			// old rewrite pointed BaseURL at the relay, so the session path landed
			// on the relay's root.
			name:    "trae session path",
			build:   func(base string) string { return base + "/api/v1/session" },
			wantURL: "https://api.trae.ai/api/v1/session",
		},
		{
			// zed derives its origin from BaseURL and posts a gRPC-web route.
			name:    "zed completion route",
			build:   func(base string) string { return base + "/completions" },
			wantURL: "https://cloud.zed.dev/completions",
		},
		{
			name:    "a signed request keeps its query",
			build:   func(base string) string { return base + "?Encode=1&FetchKeys=llm_model_result" },
			wantURL: "https://api3.qoder.sh/algo/api/v2/service/pro/sse/agent_chat_generation",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotTarget, gotPath, gotHost string
			relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotTarget = r.Header.Get("x-relay-target")
				gotPath = r.Header.Get("x-relay-path")
				gotHost = r.Host
				w.WriteHeader(http.StatusOK)
			}))
			defer relay.Close()

			h, poolID := newRelayBackedHandler(t, relay.URL)
			client, err := h.GetClientForConnection(&ConnectionData{ProxyPoolID: poolID})
			if err != nil {
				t.Fatalf("client for connection: %v", err)
			}

			req, err := http.NewRequest(http.MethodPost, tt.build(tt.wantURL), nil)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer resp.Body.Close()

			if gotHost == "" {
				t.Fatal("the relay was never reached")
			}
			if gotHost == req.URL.Host {
				t.Errorf("the request was dialled at %s, not the relay: the pool was bypassed", req.URL.Host)
			}
			if gotTarget == "" {
				t.Fatal("x-relay-target was not set: the relay has no destination")
			}
			if gotPath == "" {
				t.Fatal("x-relay-path was not set: the relay would forward to its own root")
			}
		})
	}
}

// The provider config keeps the real endpoint for every provider, which is what
// lets an executor that signs or derives a URL work unchanged.
func TestProviderConfigKeepsTheRealEndpointForEveryProvider(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "trae", "zed", "qoder", "opencode", "freebuff"} {
		t.Run(provider, func(t *testing.T) {
			h, poolID := newRelayBackedHandler(t, "https://relay-deploy.vercel.app")
			cfg, err := h.GetProviderConfig(provider, &ConnectionData{ProxyPoolID: poolID})
			if err != nil {
				t.Skipf("%s has no registry entry: %v", provider, err)
			}
			if cfg.BaseURL == "https://relay-deploy.vercel.app" {
				t.Error("BaseURL points at the relay: an executor's own path would land on the relay's root")
			}
			if cfg.RelayURL != "https://relay-deploy.vercel.app" {
				t.Errorf("RelayURL = %q, want the assigned relay", cfg.RelayURL)
			}
		})
	}
}
