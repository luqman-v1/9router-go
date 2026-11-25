package chat

import (
	"bytes"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

func seedCompatibleAMDNode(t *testing.T, baseURL, settingsJSON string) *db.Repo {
	t.Helper()
	database, cleanup := setupChatTestDB(t)
	t.Cleanup(cleanup)

	nodeData, err := json.Marshal(map[string]any{
		"prefix":  "amd",
		"apiType": "chat",
		"baseUrl": baseURL,
	})
	if err != nil {
		t.Fatalf("marshal node data: %v", err)
	}
	if _, err := database.Exec(
		`INSERT INTO providerNodes (id, type, name, data, createdAt, updatedAt) VALUES ('openai-compatible-chat-amd', 'openai-compatible', 'AMD', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(nodeData),
	); err != nil {
		t.Fatalf("seed providerNodes: %v", err)
	}

	connData, err := json.Marshal(map[string]any{
		"apiKey":                 "sk-amd-test",
		"allowPrivateNetworking": true,
	})
	if err != nil {
		t.Fatalf("marshal conn data: %v", err)
	}
	if _, err := database.Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES ('conn-amd', 'openai-compatible-chat-amd', 'apikey', 'AMD Key', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(connData),
	); err != nil {
		t.Fatalf("seed providerConnections: %v", err)
	}

	if settingsJSON != "" {
		if _, err := database.Exec(
			`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`,
			settingsJSON,
		); err != nil {
			t.Fatalf("seed settings: %v", err)
		}
	}

	return db.NewRepo(database)
}

func TestCompatibleNodeProviderThinking(t *testing.T) {
	tests := []struct {
		name            string
		settingsJSON    string
		requestBody     map[string]any
		wantHasThinking bool
		wantHasEffort   bool
		wantEffort      string
	}{
		{
			name:         "custom non-reasoning model with providerThinking off omits thinking and reasoning_effort",
			settingsJSON: `{"providerThinking":{"openai-compatible-chat-amd":{"mode":"off"}}}`,
			requestBody: map[string]any{
				"model":    "amd/my-custom-model",
				"stream":   false,
				"messages": []map[string]any{{"role": "user", "content": "hi"}},
			},
			wantHasThinking: false,
			wantHasEffort:   false,
		},
		{
			name:         "openai-dialect reasoning model with providerThinking off maps to reasoning_effort none",
			settingsJSON: `{"providerThinking":{"openai-compatible-chat-amd":{"mode":"off"}}}`,
			requestBody: map[string]any{
				"model":    "amd/gpt-5",
				"stream":   false,
				"messages": []map[string]any{{"role": "user", "content": "hi"}},
			},
			wantHasThinking: false,
			wantHasEffort:   true,
			wantEffort:      "none",
		},
		{
			name:         "client thinking budget on openai-dialect model translates to reasoning_effort and strips thinking",
			settingsJSON: `{}`,
			requestBody: map[string]any{
				"model":    "amd/gpt-5",
				"stream":   false,
				"thinking": map[string]any{"type": "enabled", "budget_tokens": 8000},
				"messages": []map[string]any{{"role": "user", "content": "hi"}},
			},
			wantHasThinking: false,
			wantHasEffort:   true,
			wantEffort:      "medium",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var capturedPath string
			var capturedBody []byte
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				capturedPath = r.URL.Path
				capturedBody, _ = io.ReadAll(r.Body)
				_ = r.Body.Close()
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","model":"ok","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`))
			}))
			defer upstream.Close()

			repo := seedCompatibleAMDNode(t, upstream.URL+"/radeon/api/v1", tc.settingsJSON)

			handler := NewChatHandler(repo)
			rawReq, err := json.Marshal(tc.requestBody)
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(rawReq))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.HandleChatCompletions(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("POST /v1/chat/completions = %d, want 200: %s", rec.Code, rec.Body.String())
			}
			if !strings.HasSuffix(capturedPath, "/radeon/api/v1/chat/completions") {
				t.Fatalf("upstream path = %q, want suffix /radeon/api/v1/chat/completions", capturedPath)
			}

			var wire map[string]any
			if err := json.Unmarshal(capturedBody, &wire); err != nil {
				t.Fatalf("unmarshal captured wire body %s: %v", capturedBody, err)
			}
			if _, hasThinking := wire["thinking"]; hasThinking != tc.wantHasThinking {
				t.Errorf("wire has thinking = %v, want %v; wire=%s", hasThinking, tc.wantHasThinking, capturedBody)
			}
			effort, hasEffort := wire["reasoning_effort"]
			if hasEffort != tc.wantHasEffort {
				t.Fatalf("wire has reasoning_effort = %v (%v), want %v; wire=%s", hasEffort, effort, tc.wantHasEffort, capturedBody)
			}
			if tc.wantHasEffort && effort != tc.wantEffort {
				t.Errorf("wire reasoning_effort = %v, want %q; wire=%s", effort, tc.wantEffort, capturedBody)
			}
		})
	}
}
