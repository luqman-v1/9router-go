package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/db"
)

func TestEnforceBlockedIP(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	if _, err := repo.BlockIP("198.51.100.55", "US", "abusive", "admin"); err != nil {
		t.Fatalf("BlockIP: %v", err)
	}

	handler := EnforceBlockedIP(repo)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))

	// Blocked IP
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.RemoteAddr = "198.51.100.55:12345"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("blocked IP got %d, want 403", rec.Code)
	}

	// Allowed IP
	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req2.RemoteAddr = "198.51.100.56:12345"
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Errorf("allowed IP got %d, want 200", rec2.Code)
	}

	// Dashboard route exempt even for blocked IP
	req3 := httptest.NewRequest(http.MethodGet, "/dashboard/overview", nil)
	req3.RemoteAddr = "198.51.100.55:12345"
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Errorf("dashboard route got %d, want 200", rec3.Code)
	}
}
