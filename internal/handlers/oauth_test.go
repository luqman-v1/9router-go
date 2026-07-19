package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

func TestHandleOAuthImport_missingProvider(t *testing.T) {
	handler := NewChatHandler(nil)
	req := httptest.NewRequest("POST", "/api/oauth//import", strings.NewReader(`{"accessToken":"test"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.HandleOAuthImport(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleOAuthImport_missingToken(t *testing.T) {
	handler := NewChatHandler(nil)
	req := httptest.NewRequest("POST", "/api/oauth/codex/import", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.HandleOAuthImport(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleOAuthImport_codex(t *testing.T) {
	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

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
	handler := NewChatHandler(nil)
	req := httptest.NewRequest("GET", "/api/oauth/kiro/social-authorize?provider=twitter", nil)
	rec := httptest.NewRecorder()
	handler.HandleOAuthKiroSocialAuthorize(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleOAuthKiroSocialAuthorize_google(t *testing.T) {
	handler := NewChatHandler(nil)
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
	handler := NewChatHandler(nil)
	req := httptest.NewRequest("POST", "/api/oauth/kiro/social-exchange", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.HandleOAuthKiroSocialExchange(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleOAuthCodexBulkImport(t *testing.T) {
	database, cleanup := setupMultimodalTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

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
