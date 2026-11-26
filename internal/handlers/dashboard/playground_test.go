package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/handlers/chat"
)

func TestHandlePlaygroundChat_RejectsExpiredAPIKey(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	if err := repo.CreateApiKey("key-exp", "sk-exp-1", "expired-key", ""); err != nil {
		t.Fatalf("CreateApiKey: %v", err)
	}
	expiredAt := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	if _, err := repo.RawDB().Exec("UPDATE apiKeys SET expiresAt = ? WHERE id = ?", expiredAt, "key-exp"); err != nil {
		t.Fatalf("set expiresAt: %v", err)
	}

	h := &DashboardHandler{Repo: repo, ChatH: &chat.ChatHandler{}}
	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"keyId":"key-exp"}`
	req := httptest.NewRequest(http.MethodPost, "/api/playground/chat", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("reached ChatH and panicked instead of rejecting expired API key: %v", r)
			}
		}()
		h.HandlePlaygroundChat(rec, req)
	}()

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(strings.ToLower(rec.Body.String()), "expired") {
		t.Fatalf("body = %q, want mention of expired key", rec.Body.String())
	}
}

func TestHandlePlaygroundChat_NilRepoWithKeyIDReturns503(t *testing.T) {
	h := &DashboardHandler{Repo: nil, ChatH: &chat.ChatHandler{}}
	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"keyId":"key-1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/playground/chat", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("HandlePlaygroundChat panicked on nil Repo: %v", r)
			}
		}()
		h.HandlePlaygroundChat(rec, req)
	}()

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body: %s)", rec.Code, rec.Body.String())
	}
}
