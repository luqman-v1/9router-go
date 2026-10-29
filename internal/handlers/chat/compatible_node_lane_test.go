package chat

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/db"
)

// #768: a custom openai-compatible node whose base URL carries a path beyond a
// version prefix — https://developer.amd.com.cn/radeon/api/v1 — received the
// request at the base URL itself, so the upstream answered 404. The issue's own
// log line shows the lane the gateway dialled:
//
//	forward to https://developer.amd.com.cn/radeon/api/v1: upstream returned 404
//
// Upstream treats a compatible node's baseUrl as a PREFIX and appends the lane
// (open-sse/executors/base.js buildUrl: `${normalized}${path}`), so the same
// configuration answers 401 there instead of 404. chatCompletionsURL leaves a
// multi-segment path alone, which is right for the Ollama Local host field — an
// endpoint the operator typed — and wrong for a node.
func TestCompatibleNodeBaseURLGetsItsLane(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		apiType  string
		base     string
		want     string
	}{
		{
			name:     "a node path beyond the version prefix gains the chat lane",
			provider: "openai-compatible-chat-amd",
			apiType:  "chat",
			base:     "https://developer.amd.com.cn/radeon/api/v1",
			want:     "https://developer.amd.com.cn/radeon/api/v1/chat/completions",
		},
		{
			name:     "a trailing slash does not double the separator",
			provider: "openai-compatible-chat-amd",
			apiType:  "chat",
			base:     "https://developer.amd.com.cn/radeon/api/v1/",
			want:     "https://developer.amd.com.cn/radeon/api/v1/chat/completions",
		},
		{
			name:     "a bare host still gains the v1 prefix",
			provider: "openai-compatible-chat-amd",
			apiType:  "chat",
			base:     "https://api.example.com",
			want:     "https://api.example.com/v1/chat/completions",
		},
		{
			name:     "a v1 root is not doubled",
			provider: "openai-compatible-chat-amd",
			apiType:  "chat",
			base:     "https://api.example.com/v1",
			want:     "https://api.example.com/v1/chat/completions",
		},
		{
			name:     "an apiType of responses picks the responses lane",
			provider: "openai-compatible-responses-amd",
			apiType:  "responses",
			base:     "https://developer.amd.com.cn/radeon/api/v1",
			want:     "https://developer.amd.com.cn/radeon/api/v1/responses",
		},
		{
			name:     "an anthropic-compatible node gains the messages lane",
			provider: "anthropic-compatible-acme",
			base:     "https://api.example.com/proxy/v1",
			want:     "https://api.example.com/proxy/v1/messages",
		},
		{
			name:     "a custom-embedding node keeps the chat lane the embeddings handler converts from",
			provider: "custom-embedding-emb",
			base:     "https://api.example.com/proxy/v1",
			want:     "https://api.example.com/proxy/v1/chat/completions",
		},
		{
			name:     "a stored full chat route is left alone",
			provider: "openai-compatible-chat-amd",
			apiType:  "chat",
			base:     "https://api.example.com/radeon/api/v1/chat/completions",
			want:     "https://api.example.com/radeon/api/v1/chat/completions",
		},
		{
			name:     "a stored full messages route is left alone",
			provider: "anthropic-compatible-acme",
			base:     "https://api.example.com/proxy/v1/messages",
			want:     "https://api.example.com/proxy/v1/messages",
		},
		{
			// These four shapes were dialled as configured before this rule
			// existed (the parent's chatCompletionsURL leaves any named route
			// alone), so appending a lane behind them would break a node that
			// works today.
			name:     "a stored legacy completions route is left alone",
			provider: "openai-compatible-chat-amd",
			apiType:  "chat",
			base:     "https://api.example.com/v1/completions",
			want:     "https://api.example.com/v1/completions",
		},
		{
			name:     "a stored /chat route is left alone",
			provider: "openai-compatible-chat-amd",
			apiType:  "chat",
			base:     "https://api.example.com/v1/chat",
			want:     "https://api.example.com/v1/chat",
		},
		{
			name:     "an ollama-shaped /api/chat node is left alone",
			provider: "openai-compatible-chat-ollama",
			apiType:  "chat",
			base:     "http://192.168.1.10:11434/api/chat",
			want:     "http://192.168.1.10:11434/api/chat",
		},
		{
			name:     "an ollama-shaped /api/generate node is left alone",
			provider: "openai-compatible-chat-ollama",
			apiType:  "chat",
			base:     "http://192.168.1.10:11434/api/generate",
			want:     "http://192.168.1.10:11434/api/generate",
		},
		{
			// Upstream's resolveOpenAICompatibleApiType falls back to the id when
			// no apiType is stored, and a connection row written before the
			// dashboard began syncing apiType carries none.
			name:     "a responses node with no stored apiType reads its id",
			provider: "openai-compatible-responses-acme",
			base:     "https://api.example.com/api/v1",
			want:     "https://api.example.com/api/v1/responses",
		},
		{
			name:     "a registry provider keeps the shared helper's shape rule",
			provider: "ollama-local",
			base:     "http://192.168.1.10:11434",
			want:     "http://192.168.1.10:11434/v1/chat/completions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := compatibleNodeLaneURL(tt.provider, tt.apiType, tt.base); got != tt.want {
				t.Errorf("compatibleNodeLaneURL(%q, %q, %q) = %q, want %q",
					tt.provider, tt.apiType, tt.base, got, tt.want)
			}
		})
	}
}

// The resolved config is what the request path actually dials, so the assertion
// that matters is on getProviderConfig — a helper that returns the right string
// while the handler keeps the old one would leave the bug in place.
func TestGetProviderConfig_CompatibleNodeBaseURLKeepsItsPath(t *testing.T) {
	const base = "https://developer.amd.com.cn/radeon/api/v1"

	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	if _, err := database.Exec(`INSERT INTO providerConnections
		(id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		VALUES ('conn-amd', 'openai-compatible-chat-amd', 'apikey', 'amd1', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		`{"apiKey":"rc-test","providerSpecificData":{"baseUrl":"`+base+`","apiType":"chat"}}`); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	handler := NewChatHandler(db.NewRepo(database))
	_, connData, err := handler.getBestConnection("openai-compatible-chat-amd", "conn-amd", nil, "DeepSeek-V4-Flash")
	if err != nil {
		t.Fatalf("getBestConnection: %v", err)
	}

	cfg, err := handler.getProviderConfig("openai-compatible-chat-amd", connData)
	if err != nil {
		t.Fatalf("getProviderConfig: %v", err)
	}
	if want := base + "/chat/completions"; cfg.BaseURL != want {
		t.Errorf("resolved BaseURL = %q, want %q — a node base URL is a prefix, not a route", cfg.BaseURL, want)
	}
}

// A node whose connection carries no baseUrl falls back to the node row, and
// that branch has its own copy of the completion. It must reach the same lane.
func TestGetProviderConfig_NodeRowBaseURLGetsItsLane(t *testing.T) {
	const base = "https://developer.amd.com.cn/radeon/api/v1"

	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	if _, err := database.Exec(`INSERT INTO providerNodes (id, type, name, data, createdAt, updatedAt)
		VALUES ('openai-compatible-chat-amd', 'openai-compatible', 'amd', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		`{"prefix":"amd","apiType":"chat","baseUrl":"`+base+`"}`); err != nil {
		t.Fatalf("seed node: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO providerConnections
		(id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		VALUES ('conn-amd', 'openai-compatible-chat-amd', 'apikey', 'amd1', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		`{"apiKey":"rc-test"}`); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	handler := NewChatHandler(db.NewRepo(database))
	_, connData, err := handler.getBestConnection("openai-compatible-chat-amd", "conn-amd", nil, "DeepSeek-V4-Flash")
	if err != nil {
		t.Fatalf("getBestConnection: %v", err)
	}

	cfg, err := handler.getProviderConfig("openai-compatible-chat-amd", connData)
	if err != nil {
		t.Fatalf("getProviderConfig: %v", err)
	}
	if want := base + "/chat/completions"; cfg.BaseURL != want {
		t.Errorf("resolved BaseURL = %q, want %q", cfg.BaseURL, want)
	}
}

// The end-to-end symptom of #768: the fake upstream asserts the path it was
// dialled on, so a resolution that never reaches the wire still fails here.
func TestCompatibleNodeRequestReachesChatLane(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"1","object":"chat.completion","model":"DeepSeek-V4-Flash",` +
			`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	if _, err := database.Exec(`INSERT INTO providerConnections
		(id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		VALUES ('conn-amd-e2e', 'openai-compatible-chat-amd', 'apikey', 'amd1', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		`{"apiKey":"rc-test","providerSpecificData":{"baseUrl":"`+upstream.URL+`/radeon/api/v1","apiType":"chat"}}`); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	handler := NewChatHandler(db.NewRepo(database))
	body := []byte(`{"model":"DeepSeek-V4-Flash","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &ModelInfo{Provider: "openai-compatible-chat-amd", Model: "DeepSeek-V4-Flash", ConnectionID: "conn-amd-e2e"}
	handler.handleSingleModel(req.Context(), rec, body, info, false, false)

	if want := "/radeon/api/v1/chat/completions"; gotPath != want {
		t.Errorf("upstream saw path %q, want %q (gateway status %d)", gotPath, want, rec.Code)
	}
}
