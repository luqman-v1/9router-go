package media

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/chat"
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
		var req map[string]interface{}
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

	data, _ := json.Marshal(map[string]interface{}{
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

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp["object"] != "list" {
		t.Errorf("expected object 'list', got %v", resp["object"])
	}
	dataArr, _ := resp["data"].([]interface{})
	if len(dataArr) != 1 {
		t.Fatalf("expected 1 data item, got %d", len(dataArr))
	}
	item, _ := dataArr[0].(map[string]interface{})
	emb, _ := item["embedding"].([]interface{})
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
	var resp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	errObj, _ := resp["error"].(map[string]interface{})
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
	var resp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	errObj, _ := resp["error"].(map[string]interface{})
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

	data, _ := json.Marshal(map[string]interface{}{
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
	var resp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	errMsg, _ := resp["error"].(string)
	if errMsg != "upstream failed" {
		t.Errorf("expected error 'upstream failed', got %v", resp["error"])
	}
}
