package handlers

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"9router/proxy/internal/db"
)

func setupTestDB(t *testing.T) (*sql.DB, func()) {
	tmpFile, err := os.CreateTemp("", "test_middleware_*.sqlite")
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

	schema := []string{
		`CREATE TABLE apiKeys (
			id TEXT PRIMARY KEY,
			key TEXT UNIQUE NOT NULL,
			name TEXT,
			machineId TEXT,
			isActive INTEGER DEFAULT 1,
			createdAt TEXT NOT NULL
		);`,
	}

	for _, query := range schema {
		if _, err := database.Exec(query); err != nil {
			cleanup()
			t.Fatalf("failed to create table: %v", err)
		}
	}

	// Seed key data
	_, err = database.Exec(`INSERT INTO apiKeys (id, key, name, machineId, isActive, createdAt) VALUES
		('1', 'valid-token', 'Test Key 1', 'mac-1', 1, '2026-07-18T00:00:00Z'),
		('2', 'inactive-token', 'Test Key 2', 'mac-2', 0, '2026-07-18T00:00:00Z');`)
	if err != nil {
		cleanup()
		t.Fatalf("failed to seed apiKeys: %v", err)
	}

	return database, cleanup
}

func TestRequireApiKeyMiddleware(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	middleware := RequireApiKey(repo)

	// Mock handler that returns 200 OK
	okHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	testHandler := middleware(okHandler)

	tests := []struct {
		name           string
		setupRequest   func() *http.Request
		expectedStatus int
	}{
		{
			name: "Valid key in Authorization header",
			setupRequest: func() *http.Request {
				req := httptest.NewRequest("GET", "http://example.com/v1/chat/completions", nil)
				req.Header.Set("Authorization", "Bearer valid-token")
				return req
			},
			expectedStatus: http.StatusOK,
		},
		{
			name: "Valid key in query parameter",
			setupRequest: func() *http.Request {
				return httptest.NewRequest("GET", "http://example.com/v1/chat/completions?key=valid-token", nil)
			},
			expectedStatus: http.StatusOK,
		},
		{
			name: "Inactive key in Authorization header",
			setupRequest: func() *http.Request {
				req := httptest.NewRequest("GET", "http://example.com/v1/chat/completions", nil)
				req.Header.Set("Authorization", "Bearer inactive-token")
				return req
			},
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name: "Missing API Key",
			setupRequest: func() *http.Request {
				return httptest.NewRequest("GET", "http://example.com/v1/chat/completions", nil)
			},
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name: "Malformed Authorization header",
			setupRequest: func() *http.Request {
				req := httptest.NewRequest("GET", "http://example.com/v1/chat/completions", nil)
				req.Header.Set("Authorization", "valid-token")
				return req
			},
			expectedStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := tt.setupRequest()
			rr := httptest.NewRecorder()
			testHandler.ServeHTTP(rr, req)

			if rr.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d. Body: %s", tt.expectedStatus, rr.Code, rr.Body.String())
			}
		})
	}
}

func TestGetAuthenticatedApiKey(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	middleware := RequireApiKey(repo)

	var retrievedKey string
	var hasKey bool

	mockHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiKeyObj := GetAuthenticatedApiKey(r)
		if apiKeyObj != nil {
			retrievedKey = apiKeyObj.Key
			hasKey = true
		}
		w.WriteHeader(http.StatusOK)
	})

	testHandler := middleware(mockHandler)

	req := httptest.NewRequest("GET", "http://example.com/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer valid-token")
	rr := httptest.NewRecorder()

	testHandler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	if !hasKey {
		t.Error("expected context to contain authenticated API key")
	}

	if retrievedKey != "valid-token" {
		t.Errorf("expected key 'valid-token', got '%s'", retrievedKey)
	}

	// Test case where no key is injected (directly calling mockHandler without middleware)
	reqNoMiddleware := httptest.NewRequest("GET", "http://example.com/v1/chat/completions", nil)
	apiKeyObj := GetAuthenticatedApiKey(reqNoMiddleware)
	if apiKeyObj != nil {
		t.Error("expected GetAuthenticatedApiKey to return nil when no key is injected")
	}
}

func TestExtractApiKey(t *testing.T) {
	tests := []struct {
		name     string
		req      *http.Request
		expected string
	}{
		{
			name:     "Bearer Authorization header",
			req:      func() *http.Request { r := httptest.NewRequest("GET", "/", nil); r.Header.Set("Authorization", "Bearer test-key-123"); return r }(),
			expected: "test-key-123",
		},
		{
			name:     "lowercase bearer",
			req:      func() *http.Request { r := httptest.NewRequest("GET", "/", nil); r.Header.Set("Authorization", "bearer test-key"); return r }(),
			expected: "test-key",
		},
		{
			name:     "query key param",
			req:      httptest.NewRequest("GET", "/?key=query-key", nil),
			expected: "query-key",
		},
		{
			name:     "query api_key param",
			req:      httptest.NewRequest("GET", "/?api_key=alt-key", nil),
			expected: "alt-key",
		},
		{
			name:     "query apiKey param",
			req:      httptest.NewRequest("GET", "/?apiKey=camel-key", nil),
			expected: "camel-key",
		},
		{
			name:     "X-API-Key header fallback",
			req:      func() *http.Request { r := httptest.NewRequest("GET", "/", nil); r.Header.Set("X-API-Key", "header-key"); return r }(),
			expected: "header-key",
		},
		{
			name:     "Bearer takes priority over query",
			req:      func() *http.Request { r := httptest.NewRequest("GET", "/?key=query-key", nil); r.Header.Set("Authorization", "Bearer bearer-key"); return r }(),
			expected: "bearer-key",
		},
		{
			name:     "no auth returns empty",
			req:      httptest.NewRequest("GET", "/", nil),
			expected: "",
		},
		{
			name:     "malformed auth with no space",
			req:      func() *http.Request { r := httptest.NewRequest("GET", "/", nil); r.Header.Set("Authorization", "no-space"); return r }(),
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractApiKey(tt.req)
			if got != tt.expected {
				t.Errorf("extractApiKey() = %q, want %q", got, tt.expected)
			}
		})
	}
}
