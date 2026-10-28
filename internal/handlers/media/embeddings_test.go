package media

import (
	"database/sql"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/providers"
)

func newTestMediaHandler(repo *db.Repo) *MediaHandler {
	chatH := chat.NewChatHandler(repo)
	return NewMediaHandler(repo, nil, chatH)
}

func setupEmbeddingsTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()

	tmpFile, err := os.CreateTemp("", "test_embeddings_*.sqlite")
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

	schemas := []string{
		`CREATE TABLE apiKeys (
			id TEXT PRIMARY KEY,
			key TEXT UNIQUE NOT NULL,
			name TEXT,
			machineId TEXT,
			isActive INTEGER DEFAULT 1,
			createdAt TEXT NOT NULL
		)`,
		`CREATE TABLE providerConnections (
			id TEXT PRIMARY KEY,
			provider TEXT NOT NULL,
			authType TEXT NOT NULL,
			name TEXT,
			email TEXT,
			priority INTEGER,
			isActive INTEGER DEFAULT 1,
			data TEXT NOT NULL,
			createdAt TEXT NOT NULL,
			updatedAt TEXT NOT NULL
		)`,
		`CREATE TABLE kv (
			scope TEXT NOT NULL,
			key TEXT NOT NULL,
			value TEXT NOT NULL,
			PRIMARY KEY (scope, key)
		)`,
	}

	for _, query := range schemas {
		if _, err := database.Exec(query); err != nil {
			cleanup()
			t.Fatalf("failed to create table: %v", err)
		}
	}
	if err := db.EnsureAdditiveColumns(database); err != nil {
		cleanup()
		t.Fatalf("additive columns: %v", err)
	}

	_, err = database.Exec(`INSERT INTO apiKeys (id, key, name, isActive, createdAt) VALUES
		('1', 'test-api-key', 'Test Key', 1, '2026-07-18T00:00:00Z')`)
	if err != nil {
		cleanup()
		t.Fatalf("failed to seed apiKeys: %v", err)
	}

	return database, cleanup
}

func TestHandleEmbeddings_Success(t *testing.T) {
	database, cleanup := setupEmbeddingsTestDB(t)
	defer cleanup()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected Content-Type application/json, got %q", ct)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer sk-test" {
			t.Errorf("expected Authorization Bearer sk-test, got %q", auth)
		}
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("failed to parse upstream body: %v", err)
		}
		if model, ok := req["model"].(string); !ok || model != "text-embedding-ada-002" {
			t.Errorf("expected model text-embedding-ada-002, got %v", req["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"object":"list","data":[{"embedding":[0.1,0.2,0.3]}]}`))
	}))
	defer upstream.Close()

	data, _ := json.Marshal(map[string]any{
		"apiKey":  "sk-test",
		"baseUrl": upstream.URL,
	})
	_, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-1', 'openai', 'apikey', 'OpenAI Test', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(data))
	if err != nil {
		t.Fatalf("failed to seed provider connection: %v", err)
	}

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	body := `{"model":"openai/text-embedding-ada-002"}`
	req := httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleEmbeddings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp["object"] != "list" {
		t.Errorf("expected object 'list', got %v", resp["object"])
	}
	dataArr, _ := resp["data"].([]any)
	if len(dataArr) != 1 {
		t.Fatalf("expected 1 data item, got %d", len(dataArr))
	}
	item, _ := dataArr[0].(map[string]any)
	emb, _ := item["embedding"].([]any)
	if len(emb) != 3 {
		t.Errorf("expected 3 embedding values, got %d", len(emb))
	}
}

func TestHandleEmbeddings_MissingModel(t *testing.T) {
	database, cleanup := setupEmbeddingsTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	body := `{}`
	req := httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleEmbeddings(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
	var resp map[string]any
	json.Unmarshal(rec.Body.Bytes(), &resp)
	errObj, _ := resp["error"].(map[string]any)
	if msg, _ := errObj["message"].(string); msg != "missing model" {
		t.Errorf("expected error message 'missing model', got %v", errObj["message"])
	}
}

func TestHandleEmbeddings_InvalidJSON(t *testing.T) {
	database, cleanup := setupEmbeddingsTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	body := `{invalid json}`
	req := httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleEmbeddings(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
	var resp map[string]any
	json.Unmarshal(rec.Body.Bytes(), &resp)
	errObj, _ := resp["error"].(map[string]any)
	if msg, _ := errObj["message"].(string); msg != "invalid JSON body" {
		t.Errorf("expected error message 'invalid JSON body', got %v", errObj["message"])
	}
}

func TestHandleEmbeddings_UpstreamError(t *testing.T) {
	database, cleanup := setupEmbeddingsTestDB(t)
	defer cleanup()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"upstream failed"}`))
	}))
	defer upstream.Close()

	data, _ := json.Marshal(map[string]any{
		"apiKey":  "sk-test",
		"baseUrl": upstream.URL,
	})
	_, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-1', 'openai', 'apikey', 'OpenAI Test', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(data))
	if err != nil {
		t.Fatalf("failed to seed provider connection: %v", err)
	}

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	body := `{"model":"openai/text-embedding-ada-002"}`
	req := httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleEmbeddings(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
	var resp map[string]any
	json.Unmarshal(rec.Body.Bytes(), &resp)
	errMsg, _ := resp["error"].(string)
	if errMsg != "upstream failed" {
		t.Errorf("expected error 'upstream failed', got %v", resp["error"])
	}
}

func TestHandleEmbeddings_ForwardStaticHeaders(t *testing.T) {
	database, cleanup := setupEmbeddingsTestDB(t)
	defer cleanup()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected Content-Type application/json, got %q", ct)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer gh-token" {
			t.Errorf("expected Authorization Bearer gh-token, got %q", auth)
		}
		if integID := r.Header.Get("copilot-integration-id"); integID != "vscode-chat" {
			t.Errorf("expected copilot-integration-id vscode-chat, got %q", integID)
		}
		if apiVer := r.Header.Get("x-github-api-version"); apiVer != "2025-04-01" {
			t.Errorf("expected x-github-api-version 2025-04-01, got %q", apiVer)
		}
		if editorVer := r.Header.Get("editor-version"); editorVer != "vscode/1.110.0" {
			t.Errorf("expected editor-version vscode/1.110.0, got %q", editorVer)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"object":"list","data":[{"embedding":[0.1,0.2]}]}`))
	}))
	defer upstream.Close()

	data, _ := json.Marshal(map[string]any{
		"apiKey":     "gh-token",
		"baseUrl":    upstream.URL,
		"authHeader": "Authorization",
		"authScheme": "Bearer",
		"staticHeaders": map[string]string{
			"copilot-integration-id": "vscode-chat",
			"x-github-api-version":   "2025-04-01",
			"editor-version":         "vscode/1.110.0",
		},
	})
	_, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-gh', 'github', 'apikey', 'GitHub Copilot', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(data))
	if err != nil {
		t.Fatalf("failed to seed provider connection: %v", err)
	}

	repo := db.NewRepo(database)
	handler := newTestMediaHandler(repo)

	body := `{"model":"github/text-embedding-3-small"}`
	req := httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleEmbeddings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// GitHub Copilot authenticates its embeddings endpoint with the short-lived
// token it hands out alongside the OAuth credential, not with the stored one.
// The request carries that token in place of the Authorization header.
func TestHandleEmbeddings_CopilotTokenOverride(t *testing.T) {
	var mu sync.Mutex
	capturedHeaders := make(http.Header)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		for k, v := range r.Header {
			capturedHeaders[k] = v
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1]}],"model":"text-embedding-3-small","usage":{"prompt_tokens":8,"total_tokens":8}}`))
	}))
	defer upstream.Close()

	origCfg := providers.KnownProviders["github"]
	testCfg := origCfg
	testCfg.BaseURL = upstream.URL + "/embeddings"
	providers.KnownProviders["github"] = testCfg
	defer func() { providers.KnownProviders["github"] = origCfg }()

	d, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	repo := db.NewRepo(d)
	connDataJSON, _ := json.Marshal(map[string]any{
		"apiKey": "sk-token",
		"providerSpecificData": map[string]any{
			"copilotToken": "tid=copilot-token-abc",
		},
	})
	_ = repo.CreateProviderConnectionFull("conn-copilot-token", "github", "apikey", "Copilot Token Test", nil, string(connDataJSON))

	handler := newTestMediaHandler(repo)
	req := httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader(`{"model":"github/text-embedding-3-small","input":"test"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.HandleEmbeddings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}
	mu.Lock()
	defer mu.Unlock()
	if auth := capturedHeaders.Get("Authorization"); auth != "Bearer tid=copilot-token-abc" {
		t.Fatalf("expected Authorization Bearer tid=copilot-token-abc, got %q", auth)
	}
}

// The Copilot token is a GitHub credential, and upstream sends it only from
// the GitHub embeddings adapter. A connection for any other provider that
// happens to carry the key must keep its own Authorization header — the token
// used to be applied to whichever upstream the connection pointed at, which
// replaced the connection's own key and leaked the GitHub credential.
func TestHandleEmbeddings_CopilotTokenIgnoredForOtherProvider(t *testing.T) {
	var mu sync.Mutex
	capturedHeaders := make(http.Header)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		for k, v := range r.Header {
			capturedHeaders[k] = v
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1]}],"model":"text-embedding-3-small"}`))
	}))
	defer upstream.Close()

	d, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	repo := db.NewRepo(d)
	connDataJSON, _ := json.Marshal(map[string]any{
		"apiKey":  "sk-openai-own-key",
		"baseUrl": upstream.URL + "/chat/completions",
		"providerSpecificData": map[string]any{
			"copilotToken": "tid=should-not-be-here",
		},
	})
	if err := repo.CreateProviderConnectionFull("conn-openai-copilot-token", "openai", "apikey", "OpenAI With Stray Copilot Token", nil, string(connDataJSON)); err != nil {
		t.Fatalf("failed to seed provider connection: %v", err)
	}

	handler := newTestMediaHandler(repo)
	req := httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader(`{"model":"openai/text-embedding-3-small","input":"test"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.HandleEmbeddings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if auth := capturedHeaders.Get("Authorization"); auth != "Bearer sk-openai-own-key" {
		t.Fatalf("expected Authorization Bearer sk-openai-own-key, got %q", auth)
	}
}

// "gh" is the published alias of the GitHub provider, so a request addressed
// to "gh/<model>" still reaches the GitHub embeddings adapter and still
// authenticates with the Copilot token.
func TestHandleEmbeddings_CopilotTokenOverrideViaAlias(t *testing.T) {
	var mu sync.Mutex
	capturedHeaders := make(http.Header)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		for k, v := range r.Header {
			capturedHeaders[k] = v
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1]}],"model":"text-embedding-3-small"}`))
	}))
	defer upstream.Close()

	origCfg := providers.KnownProviders["github"]
	testCfg := origCfg
	testCfg.BaseURL = upstream.URL + "/embeddings"
	providers.KnownProviders["github"] = testCfg
	defer func() { providers.KnownProviders["github"] = origCfg }()

	d, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	repo := db.NewRepo(d)
	connDataJSON, _ := json.Marshal(map[string]any{
		"apiKey": "sk-token",
		"providerSpecificData": map[string]any{
			"copilotToken": "tid=copilot-token-alias",
		},
	})
	if err := repo.CreateProviderConnectionFull("conn-copilot-token-alias", "github", "apikey", "Copilot Token Alias Test", nil, string(connDataJSON)); err != nil {
		t.Fatalf("failed to seed provider connection: %v", err)
	}

	handler := newTestMediaHandler(repo)
	req := httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader(`{"model":"gh/text-embedding-3-small","input":"test"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.HandleEmbeddings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if auth := capturedHeaders.Get("Authorization"); auth != "Bearer tid=copilot-token-alias" {
		t.Fatalf("expected Authorization Bearer tid=copilot-token-alias, got %q", auth)
	}
}
