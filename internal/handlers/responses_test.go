package handlers

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
)

func setupResponsesTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()

	tmpFile, err := os.CreateTemp("", "test_responses_*.sqlite")
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
			lastUsedAt TEXT,
			consecutiveUseCount INTEGER DEFAULT 0,
			createdAt TEXT NOT NULL,
			updatedAt TEXT NOT NULL
		)`,
		`CREATE TABLE kv (
			scope TEXT NOT NULL,
			key TEXT NOT NULL,
			value TEXT NOT NULL,
			PRIMARY KEY (scope, key)
		)`,
		`CREATE TABLE combos (
			id TEXT PRIMARY KEY,
			name TEXT UNIQUE NOT NULL,
			kind TEXT,
			models TEXT NOT NULL,
			createdAt TEXT NOT NULL,
			updatedAt TEXT NOT NULL
		)`,
		`CREATE TABLE settings (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			data TEXT NOT NULL
		)`,
		`CREATE TABLE providerNodes (
			id TEXT PRIMARY KEY,
			type TEXT,
			name TEXT,
			data TEXT NOT NULL,
			createdAt TEXT NOT NULL,
			updatedAt TEXT NOT NULL
		)`,
	}

	for _, query := range schemas {
		if _, err := database.Exec(query); err != nil {
			cleanup()
			t.Fatalf("failed to create table: %v", err)
		}
	}

	return database, cleanup
}

func TestHandleResponses_SingleModel_Success(t *testing.T) {
	database, cleanup := setupResponsesTestDB(t)
	defer cleanup()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("expected path /responses, got %s", r.URL.Path)
		}

		body, _ := io.ReadAll(r.Body)
		var req map[string]interface{}
		json.Unmarshal(body, &req)
		if req["model"] != "deepseek-chat" {
			t.Errorf("expected model deepseek-chat, got %v", req["model"])
		}

		auth := r.Header.Get("Authorization")
		if auth != "Bearer sk-test-key" {
			t.Errorf("expected Bearer sk-test-key, got %s", auth)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"resp-test","output":[{"type":"text","text":"hello"}]}`))
	}))
	defer upstream.Close()

	connData, _ := json.Marshal(map[string]interface{}{
		"apiKey":  "sk-test-key",
		"baseUrl": upstream.URL,
	})
	_, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-1', 'deepseek', 'apikey', 'Test', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(connData))
	if err != nil {
		t.Fatalf("failed to insert connection: %v", err)
	}

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	body := `{"model":"deepseek/deepseek-chat","stream":false}`
	req := httptest.NewRequest("POST", "/responses", strings.NewReader(body))
	rec := httptest.NewRecorder()

	handler.HandleResponses(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp["id"] != "resp-test" {
		t.Errorf("expected id resp-test, got %v", resp["id"])
	}
}

func TestHandleResponses_SingleModel_ConnectionNotFound(t *testing.T) {
	database, cleanup := setupResponsesTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	body := `{"model":"nonexistent/foo","stream":false}`
	req := httptest.NewRequest("POST", "/responses", strings.NewReader(body))
	rec := httptest.NewRecorder()

	handler.HandleResponses(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleResponses_SingleModel_NoAPIKey(t *testing.T) {
	database, cleanup := setupResponsesTestDB(t)
	defer cleanup()

	connData, _ := json.Marshal(map[string]interface{}{})
	_, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-1', 'deepseek', 'apikey', 'No Key', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(connData))
	if err != nil {
		t.Fatalf("failed to insert connection: %v", err)
	}

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	body := `{"model":"deepseek/deepseek-chat","stream":false}`
	req := httptest.NewRequest("POST", "/responses", strings.NewReader(body))
	rec := httptest.NewRecorder()

	handler.HandleResponses(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleResponses_ComboFallback_FirstFailsSecondSucceeds(t *testing.T) {
	database, cleanup := setupResponsesTestDB(t)
	defer cleanup()

	upstream1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"server error"}`))
	}))
	defer upstream1.Close()

	upstream2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"resp-combo","output":[{"type":"text","text":"from fallback"}]}`))
	}))
	defer upstream2.Close()

	conn1Data, _ := json.Marshal(map[string]interface{}{
		"apiKey":  "sk-key-1",
		"baseUrl": upstream1.URL,
	})
	_, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-1', 'deepseek', 'apikey', 'First', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(conn1Data))
	if err != nil {
		t.Fatalf("failed to insert conn-1: %v", err)
	}

	conn2Data, _ := json.Marshal(map[string]interface{}{
		"apiKey":  "sk-key-2",
		"baseUrl": upstream2.URL,
	})
	_, err = database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-2', 'groq', 'apikey', 'Second', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(conn2Data))
	if err != nil {
		t.Fatalf("failed to insert conn-2: %v", err)
	}

	comboModels, _ := json.Marshal([]string{"deepseek/deepseek-chat", "groq/qwen/qwen3-32b"})
	_, err = database.Exec(`INSERT INTO combos (id, name, models, createdAt, updatedAt) VALUES
		('combo-1', 'fallback-combo', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(comboModels))
	if err != nil {
		t.Fatalf("failed to insert combo: %v", err)
	}

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	body := `{"model":"fallback-combo","stream":false}`
	req := httptest.NewRequest("POST", "/responses", strings.NewReader(body))
	rec := httptest.NewRecorder()

	handler.HandleResponses(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp["id"] != "resp-combo" {
		t.Errorf("expected id resp-combo, got %v", resp["id"])
	}
}

func TestHandleResponses_ComboFallback_AllFail(t *testing.T) {
	database, cleanup := setupResponsesTestDB(t)
	defer cleanup()

	upstream1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"first failed"}`))
	}))
	defer upstream1.Close()

	upstream2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"second failed"}`))
	}))
	defer upstream2.Close()

	conn1Data, _ := json.Marshal(map[string]interface{}{
		"apiKey":  "sk-key-1",
		"baseUrl": upstream1.URL,
	})
	_, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-1', 'deepseek', 'apikey', 'First', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(conn1Data))
	if err != nil {
		t.Fatalf("failed to insert conn-1: %v", err)
	}

	conn2Data, _ := json.Marshal(map[string]interface{}{
		"apiKey":  "sk-key-2",
		"baseUrl": upstream2.URL,
	})
	_, err = database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-2', 'groq', 'apikey', 'Second', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(conn2Data))
	if err != nil {
		t.Fatalf("failed to insert conn-2: %v", err)
	}

	comboModels, _ := json.Marshal([]string{"deepseek/deepseek-chat", "groq/qwen/qwen3-32b"})
	_, err = database.Exec(`INSERT INTO combos (id, name, models, createdAt, updatedAt) VALUES
		('combo-2', 'all-fail-combo', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(comboModels))
	if err != nil {
		t.Fatalf("failed to insert combo: %v", err)
	}

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	body := `{"model":"all-fail-combo","stream":false}`
	req := httptest.NewRequest("POST", "/responses", strings.NewReader(body))
	rec := httptest.NewRecorder()

	handler.HandleResponses(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Errorf("expected 502, got %d: %s", rec.Code, rec.Body.String())
	}
}
