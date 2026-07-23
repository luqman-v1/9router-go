package oauth

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

func setupOAuthTestDB(t *testing.T) (*sql.DB, func()) {
	tmpFile, err := os.CreateTemp("", "test_oauth_*.sqlite")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	tmpFile.Close()

	database, err := db.OpenDatabase(tmpFile.Name())
	if err != nil {
		os.Remove(tmpFile.Name())
		t.Fatalf("OpenDatabase failed: %v", err)
	}

	schema := `
	CREATE TABLE IF NOT EXISTS providerConnections (
		id TEXT PRIMARY KEY,
		provider TEXT NOT NULL,
		authType TEXT NOT NULL,
		name TEXT,
		isActive INTEGER DEFAULT 1,
		data TEXT NOT NULL,
		createdAt TEXT,
		updatedAt TEXT
	);`
	if _, err := database.Exec(schema); err != nil {
		database.Close()
		os.Remove(tmpFile.Name())
		t.Fatalf("exec schema failed: %v", err)
	}

	cleanup := func() {
		database.Close()
		os.Remove(tmpFile.Name())
	}
	return database, cleanup
}

func TestHandleOAuthImport_missingProvider(t *testing.T) {
	handler := NewOAuthHandler(nil)
	req := httptest.NewRequest("POST", "/api/oauth//import", strings.NewReader(`{"accessToken":"test"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.HandleOAuthImport(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleOAuthImport_missingToken(t *testing.T) {
	handler := NewOAuthHandler(nil)
	req := httptest.NewRequest("POST", "/api/oauth/codex/import", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.HandleOAuthImport(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleOAuthImport_codex(t *testing.T) {
	database, cleanup := setupOAuthTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	handler := NewOAuthHandler(repo)

	body := `{"accessToken":"sk-codex-test","name":"My Codex"}`
	req := httptest.NewRequest("POST", "/api/oauth/codex/import", strings.NewReader(body))
	req.SetPathValue("provider", "codex")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleOAuthImport(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["provider"] != "codex" {
		t.Errorf("expected provider=codex, got %v", resp["provider"])
	}
}

func TestHandleOAuthKiroSocialAuthorize_invalidProvider(t *testing.T) {
	handler := NewOAuthHandler(nil)
	req := httptest.NewRequest("GET", "/api/oauth/kiro/social-authorize?provider=twitter", nil)
	rec := httptest.NewRecorder()
	handler.HandleOAuthKiroSocialAuthorize(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleOAuthKiroSocialAuthorize_google(t *testing.T) {
	handler := NewOAuthHandler(nil)
	req := httptest.NewRequest("GET", "/api/oauth/kiro/social-authorize?provider=google", nil)
	rec := httptest.NewRecorder()
	handler.HandleOAuthKiroSocialAuthorize(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["provider"] != "google" {
		t.Errorf("expected provider=google, got %v", resp["provider"])
	}
	if resp["authUrl"] == nil {
		t.Error("expected authUrl")
	}
	if resp["codeVerifier"] == nil {
		t.Error("expected codeVerifier")
	}
}

func TestHandleOAuthKiroSocialExchange_missingCode(t *testing.T) {
	handler := NewOAuthHandler(nil)
	req := httptest.NewRequest("POST", "/api/oauth/kiro/social-exchange", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.HandleOAuthKiroSocialExchange(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleOAuthCodexBulkImport(t *testing.T) {
	database, cleanup := setupOAuthTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	handler := NewOAuthHandler(repo)

	body := `{"tokens":[{"accessToken":"token-one"},{"accessToken":"token-two-longer"}]}`
	req := httptest.NewRequest("POST", "/api/oauth/codex/bulk-import", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleOAuthCodexBulkImport(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	json.Unmarshal(rec.Body.Bytes(), &resp)
	count, _ := resp["count"].(float64)
	if count != 2 {
		t.Errorf("expected count=2, got %v", count)
	}
}

func TestTitleProvider(t *testing.T) {
	if titleProvider("google") != "Google" {
		t.Errorf("expected Google, got %s", titleProvider("google"))
	}
	if titleProvider("github") != "GitHub" {
		t.Errorf("expected GitHub, got %s", titleProvider("github"))
	}
	if titleProvider("other") != "other" {
		t.Errorf("expected other, got %s", titleProvider("other"))
	}
}
