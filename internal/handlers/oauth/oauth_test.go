package oauth

import (
	"context"
	"database/sql"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

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
	if err := db.EnsureAdditiveColumns(database); err != nil {
		database.Close()
		os.Remove(tmpFile.Name())
		t.Fatalf("additive columns: %v", err)
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

func TestHandleOAuthKiroSocialExchange_codeOnlyRejected(t *testing.T) {
	handler := NewOAuthHandler(nil)
	// A code-only payload is the regression: the backend requires the PKCE
	// verifier produced by social-authorize, so omitting it must be rejected.
	req := httptest.NewRequest("POST", "/api/oauth/kiro/social-exchange", strings.NewReader(`{"code":"code-123","provider":"google"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.HandleOAuthKiroSocialExchange(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for code-only payload, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "missing code or codeVerifier") {
		t.Errorf("expected missing code or codeVerifier error, got %s", rec.Body.String())
	}
}

func TestHandleOAuthKiroSocialExchange_HonorsContextAndTimeout(t *testing.T) {
	t.Run("request context canceled", func(t *testing.T) {
		unblock := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-unblock:
			}
		}))
		t.Cleanup(func() {
			close(unblock)
			server.Close()
		})

		origURL := kiroSocialTokenURL
		kiroSocialTokenURL = server.URL
		t.Cleanup(func() { kiroSocialTokenURL = origURL })

		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
		defer cancel()

		handler := NewOAuthHandler(nil)
		req := httptest.NewRequest(http.MethodPost, "/api/oauth/kiro/social-exchange", strings.NewReader(`{"code":"c","codeVerifier":"v","provider":"google"}`)).WithContext(ctx)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		handler.HandleOAuthKiroSocialExchange(rec, req)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("expected 502, got %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("client timeout enforced", func(t *testing.T) {
		unblock := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-unblock:
			}
		}))
		t.Cleanup(func() {
			close(unblock)
			server.Close()
		})

		origURL := kiroSocialTokenURL
		origClient := kiroSocialHTTPClient
		kiroSocialTokenURL = server.URL
		kiroSocialHTTPClient = &http.Client{Timeout: 50 * time.Millisecond}
		t.Cleanup(func() {
			kiroSocialTokenURL = origURL
			kiroSocialHTTPClient = origClient
		})

		handler := NewOAuthHandler(nil)
		req := httptest.NewRequest(http.MethodPost, "/api/oauth/kiro/social-exchange", strings.NewReader(`{"code":"c","codeVerifier":"v","provider":"google"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		handler.HandleOAuthKiroSocialExchange(rec, req)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("expected 502, got %d: %s", rec.Code, rec.Body.String())
		}
	})
}

func TestHandleKiroAutoImport_MissingCache(t *testing.T) {
	temp := t.TempDir()
	t.Setenv("HOME", temp)
	t.Setenv("USERPROFILE", temp)
	handler := NewOAuthHandler(nil)
	req := httptest.NewRequest("GET", "/api/oauth/kiro/auto-import", nil)
	rec := httptest.NewRecorder()
	handler.HandleKiroAutoImport(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["found"] != false {
		t.Errorf("expected found=false with empty HOME cache, got %v", resp)
	}
}

func TestHandleKiroAPIKey_MissingKey(t *testing.T) {
	handler := NewOAuthHandler(nil)
	req := httptest.NewRequest("POST", "/api/oauth/kiro/api-key", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.HandleKiroAPIKey(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleKiroImport_MissingToken(t *testing.T) {
	handler := NewOAuthHandler(nil)
	req := httptest.NewRequest("POST", "/api/oauth/kiro/import", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.HandleKiroImport(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleKiroImportCliProxy_InvalidJSON(t *testing.T) {
	handler := NewOAuthHandler(nil)
	req := httptest.NewRequest("POST", "/api/oauth/kiro/import-cli-proxy", strings.NewReader(`{"json":"not-json"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.HandleKiroImportCliProxy(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestNormalizeKiroExternalIDPAuth_Valid(t *testing.T) {
	got, err := normalizeKiroExternalIDPAuth(map[string]any{
		"access_token":   "at",
		"refresh_token":  "rt",
		"client_id":      "cid",
		"token_endpoint": "https://login.microsoftonline.com/tenant/oauth2/v2.0/token",
		"profile_arn":    "arn:aws:codewhisperer:eu-west-1:123:profile/p",
		"scope":          "openid profile",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ProviderSpecificData["authMethod"] != "external_idp" {
		t.Errorf("expected external_idp authMethod, got %v", got.ProviderSpecificData)
	}
}

func TestNormalizeKiroExternalIDPAuth_RejectsNonHTTPS(t *testing.T) {
	_, err := normalizeKiroExternalIDPAuth(map[string]any{
		"access_token":   "at",
		"refresh_token":  "rt",
		"client_id":      "cid",
		"token_endpoint": "http://evil.example.com/token",
		"profile_arn":    "arn",
		"scope":          "openid",
	})
	if err == nil {
		t.Error("expected endpoint validation error")
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
