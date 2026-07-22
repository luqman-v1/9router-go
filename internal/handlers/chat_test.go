package handlers

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"9router/proxy/internal/db"
	"9router/proxy/internal/dbtest"
	"os"
	"9router/proxy/internal/handlerutil"
)

func setupChatTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	tmpFile, err := os.CreateTemp("", "test_chat_*.sqlite")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	tmpFile.Close()

	database, err := db.OpenDatabase(tmpFile.Name())
	if err != nil {
		os.Remove(tmpFile.Name())
		t.Fatalf("OpenDatabase failed: %v", err)
	}

	cleanup := func() {
		database.Close()
		os.Remove(tmpFile.Name())
	}

	if err := dbtest.CreateTables(database); err != nil {
		cleanup()
		t.Fatalf("CreateTables failed: %v", err)
	}

	// Seed API key (used by auth/resolve tests)
	if _, err := database.Exec(`INSERT INTO apiKeys (id, key, name, isActive, createdAt) VALUES
		('1', 'test-api-key', 'Test Key', 1, '2026-07-18T00:00:00Z')`); err != nil {
		cleanup()
		t.Fatalf("failed to seed apiKeys: %v", err)
	}

	// Seed provider connections (used by resolve/fallback tests)
	deepseekData, _ := json.Marshal(map[string]interface{}{"apiKey": "sk-test-deepseek-key"})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-1', 'deepseek', 'apikey', 'DeepSeek Test', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(deepseekData)); err != nil {
		cleanup()
		t.Fatalf("failed to seed providerConnections: %v", err)
	}

	groqData, _ := json.Marshal(map[string]interface{}{"apiKey": "gsk-test-groq-key"})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-2', 'groq', 'apikey', 'Groq Test', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(groqData)); err != nil {
		cleanup()
		t.Fatalf("failed to seed groq connection: %v", err)
	}

	// Seed model alias used by resolve tests
	if _, err := database.Exec(`INSERT INTO kv (scope, key, value) VALUES ('modelAliases', 'fast-model', '"deepseek/deepseek-chat"')`); err != nil {
		cleanup()
		t.Fatalf("failed to seed model alias: %v", err)
	}

	// Seed combo used by resolve tests
	comboModels, _ := json.Marshal([]string{"deepseek/deepseek-chat"})
	if _, err := database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES ('c1', 'my-combo', 'fallback', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(comboModels)); err != nil {
		cleanup()
		t.Fatalf("failed to seed combo: %v", err)
	}
	return database, cleanup
}
func TestResolveModel_ProviderSlashModel(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	info, err := handler.resolveModel("deepseek/deepseek-chat")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Provider != "deepseek" {
		t.Errorf("expected provider 'deepseek', got '%s'", info.Provider)
	}
	if info.Model != "deepseek-chat" {
		t.Errorf("expected model 'deepseek-chat', got '%s'", info.Model)
	}
}

func TestResolveModel_AliasResolution(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	info, err := handler.resolveModel("fast-model")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Provider != "deepseek" {
		t.Errorf("expected provider 'deepseek', got '%s'", info.Provider)
	}
	if info.Model != "deepseek-chat" {
		t.Errorf("expected model 'deepseek-chat', got '%s'", info.Model)
	}
}

func TestResolveModel_ComboResolution(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	info, err := handler.resolveModel("my-combo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Combos use the first model in the list
	if info.Provider != "deepseek" {
		t.Errorf("expected provider 'deepseek', got '%s'", info.Provider)
	}
	if info.Model != "deepseek-chat" {
		t.Errorf("expected model 'deepseek-chat', got '%s'", info.Model)
	}
}

func TestResolveModel_ProviderAlias(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	// "ds" is an alias for "deepseek"
	info, err := handler.resolveModel("ds/deepseek-chat")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Provider != "deepseek" {
		t.Errorf("expected provider 'deepseek', got '%s'", info.Provider)
	}
	if info.Model != "deepseek-chat" {
		t.Errorf("expected model 'deepseek-chat', got '%s'", info.Model)
	}
}

func TestResolveModel_EmptyModel(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	_, err := handler.resolveModel("")
	if err == nil {
		t.Error("expected error for empty model, got nil")
	}
}

func TestGetBestConnection(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	conn, connData, err := handler.getBestConnection("deepseek", "", nil, "deepseek-chat")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conn.ID != "conn-1" {
		t.Errorf("expected connection ID 'conn-1', got '%s'", conn.ID)
	}
	if connData.APIKey != "sk-test-deepseek-key" {
		t.Errorf("expected apiKey 'sk-test-deepseek-key', got '%s'", connData.APIKey)
	}
}

func TestGetBestConnection_NotFound(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	_, _, err := handler.getBestConnection("nonexistent-provider", "", nil, "")
	if err == nil {
		t.Error("expected error for nonexistent provider, got nil")
	}
}

func TestHandleChatCompletions_NonStreaming(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	// Start a mock upstream server
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the request was forwarded correctly
		body, _ := io.ReadAll(r.Body)
		var req map[string]interface{}
		json.Unmarshal(body, &req)

		if req["model"] != "deepseek-chat" {
			t.Errorf("expected model 'deepseek-chat' in upstream, got '%v'", req["model"])
		}

		// Verify auth header
		auth := r.Header.Get("Authorization")
		if auth != "Bearer sk-test-deepseek-key" {
			t.Errorf("expected Bearer auth, got '%s'", auth)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"resp-1","choices":[{"message":{"content":"hello from upstream"}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`))
	}))
	defer upstream.Close()

	// Insert a custom connection with the mock upstream URL
	customData, _ := json.Marshal(map[string]interface{}{
		"apiKey":  "sk-test-deepseek-key",
		"baseUrl": upstream.URL,
	})
	_, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-mock', 'deepseek', 'apikey', 'Mock DeepSeek', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(customData))
	if err != nil {
		t.Fatalf("failed to insert mock connection: %v", err)
	}

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	reqBody := `{"model":"deepseek/deepseek-chat","messages":[{"role":"user","content":"hello"}],"stream":false}`
	req := httptest.NewRequest("POST", "/chat/completions", strings.NewReader(reqBody))
	rec := httptest.NewRecorder()

	handler.HandleChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 OK, got %d, body: %s", rec.Code, rec.Body.String())
	}

	// Verify response contains upstream data
	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
}

func TestHandleChatCompletions_MissingModel(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	reqBody := `{"messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest("POST", "/chat/completions", strings.NewReader(reqBody))
	rec := httptest.NewRecorder()

	handler.HandleChatCompletions(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request, got %d", rec.Code)
	}
}

func TestHandleChatCompletions_InvalidJSON(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	req := httptest.NewRequest("POST", "/chat/completions", strings.NewReader("not json"))
	rec := httptest.NewRecorder()

	handler.HandleChatCompletions(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request, got %d", rec.Code)
	}
}

func TestHandleMessages_ClaudeFormat(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	// Start a mock upstream that expects OpenAI format (after translation from Claude)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]interface{}
		json.Unmarshal(body, &req)

		// After translation, should have OpenAI-format messages
		messages, ok := req["messages"].([]interface{})
		if !ok {
			t.Error("expected messages array in translated request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if len(messages) == 0 {
			t.Error("expected at least one message after translation")
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"resp-1","choices":[{"message":{"content":"translated response"}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`))
	}))
	defer upstream.Close()

	// Insert mock connection
	customData, _ := json.Marshal(map[string]interface{}{
		"apiKey":  "sk-test-deepseek-key",
		"baseUrl": upstream.URL,
	})
	_, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-mock', 'deepseek', 'apikey', 'Mock DeepSeek', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(customData))
	if err != nil {
		t.Fatalf("failed to insert mock connection: %v", err)
	}

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	// Send a Claude-format request
	claudeBody := `{"model":"deepseek/deepseek-chat","messages":[{"role":"user","content":"hello"}],"max_tokens":100,"stream":false}`
	req := httptest.NewRequest("POST", "/messages", strings.NewReader(claudeBody))
	rec := httptest.NewRecorder()

	handler.HandleMessages(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 OK, got %d, body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleChatCompletions_Streaming(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	// Mock upstream that returns SSE chunks
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)

		flusher, _ := w.(http.Flusher)
		w.Write([]byte(`data: {"id":"chatcmpl-1","choices":[{"delta":{"content":"hello"},"index":0}]}` + "\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
		w.Write([]byte(`data: {"id":"chatcmpl-1","choices":[{"delta":{"content":" world"},"index":0}]}` + "\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
		w.Write([]byte("data: [DONE]\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	customData, _ := json.Marshal(map[string]interface{}{
		"apiKey":  "sk-test-deepseek-key",
		"baseUrl": upstream.URL,
	})
	_, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-mock', 'deepseek', 'apikey', 'Mock DeepSeek', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(customData))
	if err != nil {
		t.Fatalf("failed to insert mock connection: %v", err)
	}

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	reqBody := `{"model":"deepseek/deepseek-chat","messages":[{"role":"user","content":"hello"}],"stream":true}`
	req := httptest.NewRequest("POST", "/chat/completions", strings.NewReader(reqBody))
	rec := httptest.NewRecorder()

	handler.HandleChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 OK, got %d, body: %s", rec.Code, rec.Body.String())
	}

	// Verify SSE format in response
	body := rec.Body.String()
	if !strings.Contains(body, "data: ") {
		t.Errorf("expected SSE data chunks in response, got: %s", body)
	}
}

func TestHandleChatCompletions_UpstreamError(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	// Deactivate seeded connections so only the mock is available
	_, err := database.Exec(`UPDATE providerConnections SET isActive = 0 WHERE provider = 'deepseek'`)
	if err != nil {
		t.Fatalf("failed to deactivate seeded connections: %v", err)
	}

	// Mock upstream that returns 500 (non-retryable, no account fallback)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"message":"server error","type":"server_error"}}`))
	}))
	defer upstream.Close()

	customData, _ := json.Marshal(map[string]interface{}{
		"apiKey":  "sk-bad-key",
		"baseUrl": upstream.URL,
	})
	_, err = database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-mock', 'deepseek', 'apikey', 'Mock DeepSeek', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(customData))
	if err != nil {
		t.Fatalf("failed to insert mock connection: %v", err)
	}

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	reqBody := `{"model":"deepseek/deepseek-chat","messages":[{"role":"user","content":"hello"}],"stream":false}`
	req := httptest.NewRequest("POST", "/chat/completions", strings.NewReader(reqBody))
	rec := httptest.NewRecorder()

	handler.HandleChatCompletions(rec, req)

	// Non-retryable errors are forwarded directly
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 from upstream, got %d", rec.Code)
	}
}

func TestHandleChatCompletions_AccountFallback_401(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	// Mock upstream 1: returns 401 (triggers account fallback)
	upstream1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"invalid api key","type":"auth_error"}}`))
	}))
	defer upstream1.Close()

	// Mock upstream 2: returns 200 (fallback succeeds)
	upstream2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"resp-fallback","choices":[{"message":{"content":"hello from fallback"}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`))
	}))
	defer upstream2.Close()

	// Insert two connections for deepseek: first will fail with 401, second will succeed
	data1, _ := json.Marshal(map[string]interface{}{
		"apiKey":  "sk-bad-key",
		"baseUrl": upstream1.URL,
	})
	_, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-fail', 'deepseek', 'apikey', 'Failing DeepSeek', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(data1))
	if err != nil {
		t.Fatalf("failed to insert failing connection: %v", err)
	}

	data2, _ := json.Marshal(map[string]interface{}{
		"apiKey":  "sk-good-key",
		"baseUrl": upstream2.URL,
	})
	_, err = database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-ok', 'deepseek', 'apikey', 'Good DeepSeek', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(data2))
	if err != nil {
		t.Fatalf("failed to insert good connection: %v", err)
	}

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	reqBody := `{"model":"deepseek/deepseek-chat","messages":[{"role":"user","content":"hello"}],"stream":false}`
	req := httptest.NewRequest("POST", "/chat/completions", strings.NewReader(reqBody))
	rec := httptest.NewRecorder()

	handler.HandleChatCompletions(rec, req)

	// Should succeed via account fallback
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 OK via account fallback, got %d, body: %s", rec.Code, rec.Body.String())
	}

	// Verify model lock created for failed connection, cleared for successful one
	failLocked, err := repo.IsConnectionModelLocked("conn-fail", "deepseek-chat")
	if err != nil {
		t.Fatalf("IsConnectionModelLocked(conn-fail) failed: %v", err)
	}
	if !failLocked {
		t.Error("expected model lock on failed connection (conn-fail)")
	}

	okLocked, err := repo.IsConnectionModelLocked("conn-ok", "deepseek-chat")
	if err != nil {
		t.Fatalf("IsConnectionModelLocked(conn-ok) failed: %v", err)
	}
	if okLocked {
		t.Error("expected no model lock on successful connection (conn-ok)")
	}
}

func TestHandleChatCompletions_AccountFallback_429(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	// Mock upstream: returns 429 for all requests
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit_error"}}`))
	}))
	defer upstream.Close()

	// Deactivate seeded connections, insert one mock
	_, err := database.Exec(`UPDATE providerConnections SET isActive = 0 WHERE provider = 'deepseek'`)
	if err != nil {
		t.Fatalf("failed to deactivate seeded connections: %v", err)
	}

	mockData, _ := json.Marshal(map[string]interface{}{
		"apiKey":  "sk-rate-limited",
		"baseUrl": upstream.URL,
	})
	_, err = database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-rl', 'deepseek', 'apikey', 'Rate Limited', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(mockData))
	if err != nil {
		t.Fatalf("failed to insert mock connection: %v", err)
	}

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	reqBody := `{"model":"deepseek/deepseek-chat","messages":[{"role":"user","content":"hello"}],"stream":false}`
	req := httptest.NewRequest("POST", "/chat/completions", strings.NewReader(reqBody))
	rec := httptest.NewRecorder()

	handler.HandleChatCompletions(rec, req)

	// All accounts exhausted, should return the 429
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429 after all accounts exhausted, got %d", rec.Code)
	}

	// Verify per-connection model lock was created for 429
	locked, err := repo.IsConnectionModelLocked("conn-rl", "deepseek-chat")
	if err != nil {
		t.Fatalf("IsConnectionModelLocked failed: %v", err)
	}
	if !locked {
		t.Fatal("expected conn-rl per-connection lock after 429 error")
	}
}

func TestWriteJSONError(t *testing.T) {
	rec := httptest.NewRecorder()
	handlerutil.WriteJSONError(rec, http.StatusBadRequest, "test error")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}

	var errResp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("failed to parse error response: %v", err)
	}

	errObj, ok := errResp["error"].(map[string]interface{})
	if !ok {
		t.Fatal("expected error object in response")
	}
	if errObj["message"] != "test error" {
		t.Errorf("expected message 'test error', got '%v'", errObj["message"])
	}
}

func TestExtractAPIKey(t *testing.T) {
	tests := []struct {
		name     string
		data     *ConnectionData
		expected string
	}{
		{
			name:     "API key present",
			data:     &ConnectionData{APIKey: "sk-test"},
			expected: "sk-test",
		},
		{
			name:     "Access token fallback",
			data:     &ConnectionData{AccessToken: "at-test"},
			expected: "at-test",
		},
		{
			name:     "API key takes priority",
			data:     &ConnectionData{APIKey: "sk-test", AccessToken: "at-test"},
			expected: "sk-test",
		},
		{
			name:     "Empty data",
			data:     &ConnectionData{},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractAPIKey(tt.data)
			if result != tt.expected {
				t.Errorf("expected '%s', got '%s'", tt.expected, result)
			}
		})
	}
}

func TestResolveProviderAlias(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"ds", "deepseek"},
		{"ant", "anthropic"},
		{"oa", "openai"},
		{"deepseek", "deepseek"},
		{"unknown-provider", "unknown-provider"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := resolveProviderAlias(tt.input)
			if result != tt.expected {
				t.Errorf("expected '%s', got '%s'", tt.expected, result)
			}
		})
	}
}

func TestGetProviderConfig(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	// Known provider
	cfg, err := handler.getProviderConfig("deepseek", &ConnectionData{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.BaseURL != "https://api.deepseek.com/chat/completions" {
		t.Errorf("unexpected base URL: %s", cfg.BaseURL)
	}
	if cfg.AuthScheme != "bearer" {
		t.Errorf("expected auth scheme 'bearer', got '%s'", cfg.AuthScheme)
	}

	// Custom base URL override
	cfg, err = handler.getProviderConfig("deepseek", &ConnectionData{BaseURL: "http://custom:8080/v1/chat/completions"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.BaseURL != "http://custom:8080/v1/chat/completions" {
		t.Errorf("expected custom base URL, got '%s'", cfg.BaseURL)
	}
}

func TestHandleChatCompletions_NoConnection(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	reqBody := `{"model":"openai/gpt-4o","messages":[{"role":"user","content":"hello"}],"stream":false}`
	req := httptest.NewRequest("POST", "/chat/completions", strings.NewReader(reqBody))
	rec := httptest.NewRecorder()

	handler.HandleChatCompletions(rec, req)

	if rec.Code != http.StatusNotFound && rec.Code != http.StatusBadGateway {
		t.Errorf("expected 404 or 502, got %d, body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleMessages_MissingModel(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	reqBody := `{"messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest("POST", "/messages", strings.NewReader(reqBody))
	rec := httptest.NewRecorder()

	handler.HandleMessages(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request, got %d", rec.Code)
	}
}

func TestHandleMessages_InvalidJSON(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	req := httptest.NewRequest("POST", "/messages", strings.NewReader("{invalid"))
	rec := httptest.NewRecorder()

	handler.HandleMessages(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request, got %d", rec.Code)
	}
}

func TestHandleMessages_ClaudeStreamTranslation(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		w.Write([]byte("data: {\"id\":\"chatcmpl-2\",\"model\":\"deepseek-chat\",\"choices\":[{\"delta\":{\"role\":\"assistant\"},\"index\":0}]}\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
		w.Write([]byte("data: {\"id\":\"chatcmpl-2\",\"model\":\"deepseek-chat\",\"choices\":[{\"delta\":{\"content\":\"hello\"},\"index\":0}]}\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
		w.Write([]byte("data: {\"id\":\"chatcmpl-2\",\"model\":\"deepseek-chat\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\",\"index\":0}]}\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
		w.Write([]byte("data: [DONE]\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	customData, _ := json.Marshal(map[string]interface{}{
		"apiKey":  "sk-test-key",
		"baseUrl": upstream.URL,
	})
	_, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-claude-stream', 'deepseek', 'apikey', 'Claude Stream', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(customData))
	if err != nil {
		t.Fatalf("failed to insert mock connection: %v", err)
	}

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	claudeBody := `{"model":"deepseek/deepseek-chat","messages":[{"role":"user","content":"hi"}],"max_tokens":100,"stream":true}`
	req := httptest.NewRequest("POST", "/messages", strings.NewReader(claudeBody))
	rec := httptest.NewRecorder()

	handler.HandleMessages(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d, body: %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	if !strings.Contains(body, "data:") {
		t.Errorf("expected SSE data in response, got: %s", body)
	}
}

func TestHandleMessages_NoConnection(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	claudeBody := `{"model":"openai/gpt-4o","messages":[{"role":"user","content":"hi"}],"stream":false}`
	req := httptest.NewRequest("POST", "/messages", strings.NewReader(claudeBody))
	rec := httptest.NewRecorder()

	handler.HandleMessages(rec, req)

	if rec.Code != http.StatusNotFound && rec.Code != http.StatusBadGateway {
		t.Errorf("expected 404 or 502, got %d, body: %s", rec.Code, rec.Body.String())
	}
}

func TestSetupRoutes(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	r := chi.NewRouter()
	SetupRoutes(r, repo, nil)

	// Verify POST /v1/chat/completions is registered
	req := httptest.NewRequest("POST", "/chat/completions", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code == http.StatusMethodNotAllowed || w.Code == http.StatusNotFound {
		t.Errorf("expected /v1/chat/completions route to be registered, got status %d", w.Code)
	}

	// Verify POST /v1/messages is registered
	req = httptest.NewRequest("POST", "/messages", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code == http.StatusMethodNotAllowed || w.Code == http.StatusNotFound {
		t.Errorf("expected /v1/messages route to be registered, got status %d", w.Code)
	}
}

func TestHandleChatCompletions_NilBody(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	req := httptest.NewRequest("POST", "/chat/completions", nil)
	rec := httptest.NewRecorder()

	handler.HandleChatCompletions(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for nil body, got %d", rec.Code)
	}
}

func TestResolveModel_PrefixProvider(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	// Seed a providerNode with prefix "bn"
	nodeData := `{"prefix":"bn","apiType":"openai-compatible","baseUrl":"https://bn.example.com/v1/chat/completions"}`
	_, err := database.Exec(`INSERT INTO providerNodes (id, type, name, data, createdAt, updatedAt) VALUES
		('openai-compatible-chat-bn', 'openai-compatible', 'Bun Node', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		nodeData)
	if err != nil {
		t.Fatalf("failed to seed providerNode: %v", err)
	}

	// Seed a connection for this providerNode
	connData, _ := json.Marshal(map[string]interface{}{
		"apiKey": "sk-bn-key",
	})
	_, err = database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-bn', 'openai-compatible-chat-bn', 'apikey', 'Bun Connection', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(connData))
	if err != nil {
		t.Fatalf("failed to seed connection: %v", err)
	}

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	// "bn/claude-sonnet-4.5" should resolve via prefix -> providerNode -> connection
	info, err := handler.resolveModel("bn/claude-sonnet-4.5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Provider != "openai-compatible-chat-bn" {
		t.Errorf("expected provider 'openai-compatible-chat-bn', got '%s'", info.Provider)
	}
	if info.Model != "claude-sonnet-4.5" {
		t.Errorf("expected model 'claude-sonnet-4.5', got '%s'", info.Model)
	}
	if info.ConnectionID != "conn-bn" {
		t.Errorf("expected connectionID 'conn-bn', got '%s'", info.ConnectionID)
	}
}

func TestResolveModel_PrefixProvider_NoConnection(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	// Seed a providerNode with prefix "bn" but no connection
	nodeData := `{"prefix":"bn","apiType":"openai-compatible","baseUrl":"https://bn.example.com/v1/chat/completions"}`
	_, err := database.Exec(`INSERT INTO providerNodes (id, type, name, data, createdAt, updatedAt) VALUES
		('openai-compatible-chat-bn', 'openai-compatible', 'Bun Node', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		nodeData)
	if err != nil {
		t.Fatalf("failed to seed providerNode: %v", err)
	}

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	// resolveModel falls through to standard "provider/model" when the prefix node has no connection.
	// The actual failure happens at getBestConnection time (returned as 404 by the handler).
	info, err := handler.resolveModel("bn/claude-sonnet-4.5")
	if err != nil {
		t.Fatalf("resolveModel should not error (it defers connection lookup): %v", err)
	}
	if info.Provider != "bn" {
		t.Errorf("expected fallback provider 'bn', got '%s'", info.Provider)
	}
	if info.Model != "claude-sonnet-4.5" {
		t.Errorf("expected model 'claude-sonnet-4.5', got '%s'", info.Model)
	}
	if info.ConnectionID != "" {
		t.Errorf("expected empty connectionID when prefix has no connection, got '%s'", info.ConnectionID)
	}

	// Verify the full handler returns 404 since no connection exists
	reqBody := `{"model":"bn/claude-sonnet-4.5","messages":[{"role":"user","content":"hello"}],"stream":false}`
	req := httptest.NewRequest("POST", "/chat/completions", strings.NewReader(reqBody))
	rec := httptest.NewRecorder()
	handler.HandleChatCompletions(rec, req)

	if rec.Code != http.StatusNotFound && rec.Code != http.StatusBadGateway {
		t.Errorf("expected 404 or 502 when prefix node has no connection, got %d, body: %s", rec.Code, rec.Body.String())
	}
}

func TestGetProviderConfig_ProviderNode(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	// Seed a providerNode
	nodeData := `{"prefix":"bn","apiType":"openai-compatible","baseUrl":"https://bn.example.com/v1/chat/completions"}`
	_, err := database.Exec(`INSERT INTO providerNodes (id, type, name, data, createdAt, updatedAt) VALUES
		('openai-compatible-chat-bn', 'openai-compatible', 'Bun Node', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		nodeData)
	if err != nil {
		t.Fatalf("failed to seed providerNode: %v", err)
	}

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	// getProviderConfig with a providerNode ID should use the node's baseUrl
	cfg, err := handler.getProviderConfig("openai-compatible-chat-bn", &ConnectionData{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.BaseURL != "https://bn.example.com/v1/chat/completions" {
		t.Errorf("expected baseUrl from providerNode, got '%s'", cfg.BaseURL)
	}
	if cfg.AuthScheme != "bearer" {
		t.Errorf("expected auth scheme 'bearer', got '%s'", cfg.AuthScheme)
	}
}

func TestHandleChatCompletions_ComboFallback(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	// Mock upstream 1: returns 500 (will be tried first)
	failingUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"message":"model overloaded","type":"server_error"}}`))
	}))
	defer failingUpstream.Close()

	// Mock upstream 2: returns 200 (fallback that succeeds)
	succeedingUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]interface{}
		json.Unmarshal(body, &req)

		// Verify the second model name was used
		if req["model"] != "qwen3-32b" {
			t.Errorf("expected model 'qwen3-32b' in upstream, got '%v'", req["model"])
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"resp-fallback","choices":[{"message":{"content":"hello from fallback"}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`))
	}))
	defer succeedingUpstream.Close()

	// Insert mock connections for both upstreams (high priority so they get picked)
	failingData, _ := json.Marshal(map[string]interface{}{
		"apiKey":  "sk-failing-key",
		"baseUrl": failingUpstream.URL,
	})
	_, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('mock-1', 'deepseek', 'apikey', 'Failing DeepSeek', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(failingData))
	if err != nil {
		t.Fatalf("failed to insert failing connection: %v", err)
	}

	succeedingData, _ := json.Marshal(map[string]interface{}{
		"apiKey":  "sk-succeeding-key",
		"baseUrl": succeedingUpstream.URL,
	})
	_, err = database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('mock-2', 'groq', 'apikey', 'Succeeding Groq', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(succeedingData))
	if err != nil {
		t.Fatalf("failed to insert succeeding connection: %v", err)
	}

	// Insert a combo where the first model fails and the second succeeds
	comboModels, _ := json.Marshal([]string{"deepseek/deepseek-chat", "groq/qwen3-32b"})
	_, err = database.Exec(`INSERT INTO combos (id, name, models, createdAt, updatedAt) VALUES
		('combo-fallback', 'combo-fallback-test', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(comboModels))
	if err != nil {
		t.Fatalf("failed to insert combo: %v", err)
	}

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	reqBody := `{"model":"combo-fallback-test","messages":[{"role":"user","content":"hello"}],"stream":false}`
	req := httptest.NewRequest("POST", "/chat/completions", strings.NewReader(reqBody))
	rec := httptest.NewRecorder()

	handler.HandleChatCompletions(rec, req)

	// Should succeed via the second model in the combo
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 OK via combo fallback, got %d, body: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp["id"] != "resp-fallback" {
		t.Errorf("expected response id 'resp-fallback', got '%v'", resp["id"])
	}
}

func TestHandleChatCompletions_ComboAllFail(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	// Mock upstream: returns 500 for all requests
	failingUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit_error"}}`))
	}))
	defer failingUpstream.Close()

	// Insert connections for both providers pointing to the same failing upstream
	failingData, _ := json.Marshal(map[string]interface{}{
		"apiKey":  "sk-failing-key",
		"baseUrl": failingUpstream.URL,
	})
	_, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('mock-ds', 'deepseek', 'apikey', 'Failing DeepSeek', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(failingData))
	if err != nil {
		t.Fatalf("failed to insert connection: %v", err)
	}

	failingData2, _ := json.Marshal(map[string]interface{}{
		"apiKey":  "sk-failing-key",
		"baseUrl": failingUpstream.URL,
	})
	_, err = database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('mock-gq', 'groq', 'apikey', 'Failing Groq', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(failingData2))
	if err != nil {
		t.Fatalf("failed to insert connection: %v", err)
	}

	// Insert a combo where all models fail
	comboModels, _ := json.Marshal([]string{"deepseek/deepseek-chat", "groq/llama-3-70b"})
	_, err = database.Exec(`INSERT INTO combos (id, name, models, createdAt, updatedAt) VALUES
		('combo-allfail', 'combo-allfail-test', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(comboModels))
	if err != nil {
		t.Fatalf("failed to insert combo: %v", err)
	}

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	reqBody := `{"model":"combo-allfail-test","messages":[{"role":"user","content":"hello"}],"stream":false}`
	req := httptest.NewRequest("POST", "/chat/completions", strings.NewReader(reqBody))
	rec := httptest.NewRecorder()

	handler.HandleChatCompletions(rec, req)

	// Should return the last upstream error status
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429 from last failed model, got %d, body: %s", rec.Code, rec.Body.String())
	}
}

func TestGetBestConnection_PinnedID(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	// Pinned connection ID should skip provider-based lookup
	conn, connData, err := handler.getBestConnection("deepseek", "conn-1", nil, "deepseek-chat")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conn.ID != "conn-1" {
		t.Errorf("expected connection ID 'conn-1', got '%s'", conn.ID)
	}
	if connData.APIKey != "sk-test-deepseek-key" {
		t.Errorf("expected apiKey 'sk-test-deepseek-key', got '%s'", connData.APIKey)
	}
}

func TestGetBestConnection_PinnedID_NotFound(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	_, _, err := handler.getBestConnection("deepseek", "nonexistent-conn", nil, "deepseek-chat")
	if err == nil {
		t.Error("expected error for nonexistent pinned connection, got nil")
	}
}

func TestHandleChatCompletions_PrefixProvider(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	// Start a mock upstream
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]interface{}
		json.Unmarshal(body, &req)

		if req["model"] != "claude-sonnet-4.5" {
			t.Errorf("expected model 'claude-sonnet-4.5' in upstream, got '%v'", req["model"])
		}

		auth := r.Header.Get("Authorization")
		if auth != "Bearer sk-bn-key" {
			t.Errorf("expected Bearer sk-bn-key, got '%s'", auth)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"resp-bn","choices":[{"message":{"content":"hello from bn"}}],"usage":{"prompt_tokens":5,"completion_tokens":3}}`))
	}))
	defer upstream.Close()

	// Seed providerNode with upstream URL
	nodeData, _ := json.Marshal(map[string]interface{}{
		"prefix":  "bn",
		"apiType": "openai-compatible",
		"baseUrl": upstream.URL,
	})
	_, err := database.Exec(`INSERT INTO providerNodes (id, type, name, data, createdAt, updatedAt) VALUES
		('openai-compatible-chat-bn', 'openai-compatible', 'Bun Node', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(nodeData))
	if err != nil {
		t.Fatalf("failed to seed providerNode: %v", err)
	}

	// Seed connection for this node
	connData, _ := json.Marshal(map[string]interface{}{
		"apiKey": "sk-bn-key",
	})
	_, err = database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-bn', 'openai-compatible-chat-bn', 'apikey', 'Bun Conn', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(connData))
	if err != nil {
		t.Fatalf("failed to seed connection: %v", err)
	}

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	reqBody := `{"model":"bn/claude-sonnet-4.5","messages":[{"role":"user","content":"hello"}],"stream":false}`
	req := httptest.NewRequest("POST", "/chat/completions", strings.NewReader(reqBody))
	rec := httptest.NewRecorder()

	handler.HandleChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 OK, got %d, body: %s", rec.Code, rec.Body.String())
	}
}

// Test handleAccountFallback — non-retryable 500 stops immediately, no lock created.
func TestAccountFallback_NonRetryableError(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"message":"server error","type":"server_error"}}`))
	}))
	defer upstream.Close()

	_, err := database.Exec(`UPDATE providerConnections SET isActive = 0 WHERE provider = 'deepseek'`)
	if err != nil {
		t.Fatalf("failed to deactivate connections: %v", err)
	}

	mockData, _ := json.Marshal(map[string]interface{}{
		"apiKey":  "sk-bad-key",
		"baseUrl": upstream.URL,
	})
	_, err = database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-500', 'deepseek', 'apikey', '500 Connection', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(mockData))
	if err != nil {
		t.Fatalf("failed to insert connection: %v", err)
	}

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	reqBody := `{"model":"deepseek/deepseek-chat","messages":[{"role":"user","content":"hello"}],"stream":false}`
	req := httptest.NewRequest("POST", "/chat/completions", strings.NewReader(reqBody))
	rec := httptest.NewRecorder()
	handler.HandleChatCompletions(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}

	// Verify no lock was created for 500 error
	locked, err := repo.IsConnectionModelLocked("conn-500", "deepseek-chat")
	if err != nil {
		t.Fatalf("IsConnectionModelLocked failed: %v", err)
	}
	if locked {
		t.Error("expected no model lock for non-retryable 500 error")
	}
}

// Test handleAccountFallback — lock expiration: expired lock allows retry.
func TestAccountFallback_LockExpiration(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	// Insert an expired lock (lockedUntil in the past)
	expiredTime := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
	lockData, _ := json.Marshal(db.ModelLock{
		LockedUntil: expiredTime,
		LastError:   "401 upstream error",
		ErrorCode:   401,
	})
	_, err := database.Exec(`INSERT OR REPLACE INTO kv (scope, key, value) VALUES ('modelLock', 'DEEPSEEK/DEEPSEEK-CHAT', ?)`, string(lockData))
	if err != nil {
		t.Fatalf("failed to insert expired lock: %v", err)
	}

	// Verify the expired lock in kv is ignored by connection-based health
	// (kv table modelLock is legacy; new code reads modelLock_* from providerConnections.data)

	// Request should succeed since lock is expired
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"resp-ok","choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":5,"completion_tokens":3}}`))
	}))
	defer upstream.Close()

	mockData, _ := json.Marshal(map[string]interface{}{
		"apiKey":  "sk-ok-key",
		"baseUrl": upstream.URL,
	})
	_, err = database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-ok', 'deepseek', 'apikey', 'OK Connection', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(mockData))
	if err != nil {
		t.Fatalf("failed to insert connection: %v", err)
	}

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	reqBody := `{"model":"deepseek/deepseek-chat","messages":[{"role":"user","content":"hello"}],"stream":false}`
	req := httptest.NewRequest("POST", "/chat/completions", strings.NewReader(reqBody))
	rec := httptest.NewRecorder()
	handler.HandleChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d (lock should be expired)", rec.Code)
	}
}

// Test handleAccountFallback — lock duration: 401 = 120s, 429 = 60s.
func TestAccountFallback_LockDuration(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)

	// Lock with 401
	err := repo.LockModel("deepseek", "model-401", 120, "401 error", 401, 0)
	if err != nil {
		t.Fatalf("LockModel failed: %v", err)
	}
	lock, err := repo.GetModelLock("deepseek", "model-401")
	if err != nil {
		t.Fatalf("GetModelLock failed: %v", err)
	}
	if lock == nil {
		t.Fatal("expected non-nil lock for 401")
	}
	if lock.ErrorCode != 401 {
		t.Errorf("expected error code 401, got %d", lock.ErrorCode)
	}

	// Lock with 429
	err = repo.LockModel("deepseek", "model-429", 60, "429 error", 429, 0)
	if err != nil {
		t.Fatalf("LockModel failed: %v", err)
	}
	lock, err = repo.GetModelLock("deepseek", "model-429")
	if err != nil {
		t.Fatalf("GetModelLock failed: %v", err)
	}
	if lock == nil {
		t.Fatal("expected non-nil lock for 429")
	}
	if lock.ErrorCode != 429 {
		t.Errorf("expected error code 429, got %d", lock.ErrorCode)
	}

	// UnlockModel should remove the lock
	err = repo.UnlockModel("deepseek", "model-401")
	if err != nil {
		t.Fatalf("UnlockModel failed: %v", err)
	}
	locked, err := repo.IsModelLocked("deepseek", "model-401")
	if err != nil {
		t.Fatalf("IsModelLocked failed: %v", err)
	}
	if locked {
		t.Error("expected model to be unlocked after UnlockModel")
	}
}

// Test handleAccountFallback — all connections fail with retryable (429).
func TestAccountFallback_AllExhaustedRetryable(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit_error"}}`))
	}))
	defer upstream.Close()

	_, err := database.Exec(`UPDATE providerConnections SET isActive = 0 WHERE provider = 'deepseek'`)
	if err != nil {
		t.Fatalf("failed to deactivate: %v", err)
	}

	mockData, _ := json.Marshal(map[string]interface{}{
		"apiKey":  "sk-key",
		"baseUrl": upstream.URL,
	})
	_, err = database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-rl-1', 'deepseek', 'apikey', 'RL Conn 1', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(mockData))
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	_, err = database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-rl-2', 'deepseek', 'apikey', 'RL Conn 2', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(mockData))
	if err != nil {
		t.Fatalf("failed: %v", err)
	}

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	reqBody := `{"model":"deepseek/deepseek-chat","messages":[{"role":"user","content":"hello"}],"stream":false}`
	req := httptest.NewRequest("POST", "/chat/completions", strings.NewReader(reqBody))
	rec := httptest.NewRecorder()
	handler.HandleChatCompletions(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d", rec.Code)
	}

	// Both connections should have per-connection locks
	locked1, err := repo.IsConnectionModelLocked("conn-rl-1", "deepseek-chat")
	if err != nil {
		t.Fatalf("IsConnectionModelLocked failed: %v", err)
	}
	if !locked1 {
		t.Error("expected conn-rl-1 per-connection lock after 429")
	}
	locked2, err := repo.IsConnectionModelLocked("conn-rl-2", "deepseek-chat")
	if err != nil {
		t.Fatalf("IsConnectionModelLocked failed: %v", err)
	}
	if !locked2 {
		t.Error("expected conn-rl-2 per-connection lock after 429")
	}
}

// Test handleAccountFallback — pinned connection bypasses account fallback loop.
func TestAccountFallback_PinnedConnection(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"resp-pinned","choices":[{"message":{"content":"pinned"}}],"usage":{"prompt_tokens":5,"completion_tokens":3}}`))
	}))
	defer upstream.Close()

	pinnedData, _ := json.Marshal(map[string]interface{}{
		"apiKey":  "sk-pinned",
		"baseUrl": upstream.URL,
	})
	_, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-pinned', 'deepseek', 'apikey', 'Pinned', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(pinnedData))
	if err != nil {
		t.Fatalf("failed: %v", err)
	}

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	body := []byte(`{"model":"deepseek-chat","messages":[{"role":"user","content":"hello"}]}`)
	rec := httptest.NewRecorder()
	err = handler.handleAccountFallback(rec, "deepseek", "deepseek-chat", "conn-pinned", body, false, false, "/v1/chat/completions")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}
func TestAccountFallback_PinnedConnection_NotFound(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	body := []byte(`{"model":"deepseek-chat","messages":[{"role":"user","content":"hello"}]}`)
	rec := httptest.NewRecorder()
	err := handler.handleAccountFallback(rec, "deepseek", "deepseek-chat", "nonexistent-conn", body, false, false, "/v1/chat/completions")
	if err == nil {
		t.Fatal("expected error for nonexistent pinned connection, got nil")
	}
}
