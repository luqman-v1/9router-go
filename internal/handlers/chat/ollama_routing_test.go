package chat

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/proxy/executor"
	"9router/proxy/internal/providers"
)

// #192: an ollama.com API key was forwarded to http://localhost:11434. The
// registry entry named the self-hosted daemon, so every cloud connection dialled
// a loopback port that exists only where `ollama serve` is running, and the
// request failed with "connection refused" before it ever left the machine.
func TestOllamaCloudDoesNotDialLoopback(t *testing.T) {
	cfg, ok := providers.KnownProviders["ollama"]
	if !ok {
		t.Fatal("ollama provider is not registered")
	}
	if want := "https://ollama.com/v1/chat/completions"; cfg.BaseURL != want {
		t.Errorf("ollama BaseURL = %q, want %q — a cloud key must not reach localhost", cfg.BaseURL, want)
	}
}

// The two Ollama providers are distinct products with distinct endpoints, and
// conflating them is what made #192 confusing: one is a hosted API keyed at
// ollama.com, the other a daemon on the operator's own machine. Sharing a
// baseUrl would silently route a cloud key to a local socket again, or point a
// local connection at the internet.
func TestOllamaCloudAndLocalStaySeparate(t *testing.T) {
	cloud, cloudOK := providers.KnownProviders["ollama"]
	local, localOK := providers.KnownProviders["ollama-local"]
	if !cloudOK || !localOK {
		t.Fatalf("both ollama providers must be registered (cloud=%v local=%v)", cloudOK, localOK)
	}
	if cloud.BaseURL == local.BaseURL {
		t.Fatalf("ollama and ollama-local share a base URL: %q", cloud.BaseURL)
	}
	if want := "http://localhost:11434/v1/chat/completions"; local.BaseURL != want {
		t.Errorf("ollama-local BaseURL = %q, want %q — self-hosting is the whole point of that provider", local.BaseURL, want)
	}
}

// chatCompletionsURL turns a bare host into the route a POST needs. The
// dashboard's Ollama Local host field is typed as "http://192.168.1.10:11434",
// so a dotted IP is the case that actually occurs; an early implementation
// treated a dot in the last segment as a filename and passed it through
// unchanged, which is the bug this table pins.
func TestChatCompletionsURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"bare loopback host gains v1 lane", "http://localhost:11434", "http://localhost:11434/v1/chat/completions"},
		{"bare dotted IP host gains v1 lane", "http://192.168.1.10:11434", "http://192.168.1.10:11434/v1/chat/completions"},
		{"trailing slash on a bare host", "http://192.168.1.10:11434/", "http://192.168.1.10:11434/v1/chat/completions"},
		{"v1 prefix is not doubled", "https://api.example.com/v1", "https://api.example.com/v1/chat/completions"},
		{"v1 prefix with slash is not doubled", "https://api.example.com/v1/", "https://api.example.com/v1/chat/completions"},
		{"full route is left alone", "https://api.example.com/v1/chat/completions", "https://api.example.com/v1/chat/completions"},
		{"anthropic messages lane is left alone", "https://api.anthropic.com/v1/messages", "https://api.anthropic.com/v1/messages"},
		{"responses lane is left alone", "https://chatgpt.com/backend-api/codex/responses", "https://chatgpt.com/backend-api/codex/responses"},
		{"ollama native chat lane is left alone", "https://ollama.com/api/chat", "https://ollama.com/api/chat"},
		{"embeddings lane is left alone", "https://api.example.com/v1/embeddings", "https://api.example.com/v1/embeddings"},
		{"a named route under v1 is left alone", "https://api.example.com/v1/proxy", "https://api.example.com/v1/proxy"},
		{"a beta version prefix is not doubled", "https://gen.example.com/v1beta", "https://gen.example.com/v1beta/chat/completions"},
		{"empty input is returned untouched", "", ""},
		{"whitespace-only input is returned untouched", "   ", "   "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := chatCompletionsURL(tt.in); got != tt.want {
				t.Errorf("chatCompletionsURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// The dashboard writes a per-connection endpoint override into
// providerSpecificData.baseUrl, while ConnectionData.BaseURL carries only the
// top-level baseUrl key. Without hydration the override was parsed and thrown
// away, so the request fell back to the registry default — which for ollama was
// the loopback port of #192. The assertion is on the URL the handler resolves,
// not on the parse, because the parse was never the broken part.
func TestGetProviderConfig_HydratesProviderSpecificBaseURL(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	const selfHosted = "http://192.168.1.10:11434"
	if _, err := database.Exec(`INSERT INTO providerConnections
		(id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		VALUES ('conn-ollama', 'ollama-local', 'none', 'Local Ollama', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		`{"providerSpecificData":{"baseUrl":"`+selfHosted+`"}}`); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	handler := NewChatHandler(db.NewRepo(database))
	_, connData, err := handler.getBestConnection("ollama-local", "conn-ollama", nil, "")
	if err != nil {
		t.Fatalf("getBestConnection: %v", err)
	}
	if connData.BaseURL != selfHosted {
		t.Fatalf("ConnectionData.BaseURL = %q, want the stored override %q", connData.BaseURL, selfHosted)
	}

	cfg, err := handler.getProviderConfig("ollama-local", connData)
	if err != nil {
		t.Fatalf("getProviderConfig: %v", err)
	}
	if want := selfHosted + "/v1/chat/completions"; cfg.BaseURL != want {
		t.Errorf("resolved BaseURL = %q, want %q", cfg.BaseURL, want)
	}
}

// An operator who already stored a top-level baseUrl keeps it, and a URL that
// names a full route is never rewritten — silently appending /v1 to a working
// endpoint would break every connection already configured that way.
func TestGetProviderConfig_TopLevelBaseURLWinsAndKeepsRoute(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	const full = "https://api.example.com/v1/chat/completions"
	const override = "http://192.168.1.10:11434"
	if _, err := database.Exec(`INSERT INTO providerConnections
		(id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		VALUES ('conn-both', 'ollama', 'apikey', 'Both Writers', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		`{"apiKey":"sk-x","baseUrl":"`+full+`","providerSpecificData":{"baseUrl":"`+override+`"}}`); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	handler := NewChatHandler(db.NewRepo(database))
	_, connData, err := handler.getBestConnection("ollama", "conn-both", nil, "")
	if err != nil {
		t.Fatalf("getBestConnection: %v", err)
	}

	cfg, err := handler.getProviderConfig("ollama", connData)
	if err != nil {
		t.Fatalf("getProviderConfig: %v", err)
	}
	if cfg.BaseURL != full {
		t.Errorf("resolved BaseURL = %q, want the top-level baseUrl %q left intact", cfg.BaseURL, full)
	}
}

// The end-to-end symptom of #192: a request against an ollama.com connection
// reaches the configured upstream and nowhere near localhost. The registry
// default is deliberately not consulted here — only a per-connection override
// could redirect this, and the fake upstream is the only thing that should be
// dialled.
func TestOllamaConnectionProxiesToConfiguredUpstream(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"1","object":"chat.completion","model":"gpt-oss:120b",` +
			`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	if _, err := database.Exec(`INSERT INTO providerConnections
		(id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		VALUES ('conn-ollama-e2e', 'ollama', 'apikey', 'Ollama Cloud', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		`{"apiKey":"oll-test-key","baseUrl":"`+upstream.URL+`"}`); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	handler := NewChatHandler(db.NewRepo(database))
	_, connData, err := handler.getBestConnection("ollama", "conn-ollama-e2e", nil, "gpt-oss:120b")
	if err != nil {
		t.Fatalf("getBestConnection: %v", err)
	}
	cfg, err := handler.getProviderConfig("ollama", connData)
	if err != nil {
		t.Fatalf("getProviderConfig: %v", err)
	}


	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()
	executeExecutor(t, rec, req, cfg, connData, []byte(`{"model":"gpt-oss:120b","messages":[{"role":"user","content":"hi"}]}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("upstream received path %q, want /v1/chat/completions", gotPath)
	}
}

// executeExecutor runs a request through the provider's real registered
// executor, the same dispatch handleAccountFallback performs, so the assertion
// covers the URL actually dialled rather than a hand-rolled request.
func executeExecutor(t *testing.T, w http.ResponseWriter, r *http.Request, cfg *providers.ProviderConfig, connData *ConnectionData, body []byte) {
	t.Helper()
	exec := executor.Get("ollama")
	if exec == nil {
		t.Fatal("no executor registered for ollama")
	}
	execReq := &executor.Request{
		Ctx:      r.Context(),
		Client:   http.DefaultClient,
		Config:   cfg,
		APIKey:   "oll-test-key",
		Body:     body,
		IsStream: false,
	}
	if connData != nil {
		execReq.ConnData = connData.ProviderSpecificData
	}
	if err := exec(w, execReq); err != nil {
		t.Fatalf("executor: %v", err)
	}
}