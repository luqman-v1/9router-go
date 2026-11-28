package dashboard

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleBlockedIPsRoutes(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	h := NewDashboardHandler(repo)

	// List initially empty
	req := httptest.NewRequest(http.MethodGet, "/api/blocked-ips", nil)
	rec := httptest.NewRecorder()
	h.HandleListBlockedIPs(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/blocked-ips got %d, want 200", rec.Code)
	}

	// Block IP
	req2 := httptest.NewRequest(http.MethodPost, "/api/blocked-ips", bytes.NewReader([]byte(`{"ip":"203.0.113.19","country":"US","reason":"scraping"}`)))
	rec2 := httptest.NewRecorder()
	h.HandleBlockIP(rec2, req2)
	if rec2.Code != http.StatusCreated {
		t.Fatalf("POST /api/blocked-ips got %d, want 201: %s", rec2.Code, rec2.Body.String())
	}

	// List contains 1
	req3 := httptest.NewRequest(http.MethodGet, "/api/blocked-ips", nil)
	rec3 := httptest.NewRecorder()
	h.HandleListBlockedIPs(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("GET /api/blocked-ips got %d, want 200", rec3.Code)
	}

	// Unblock IP
	req4 := httptest.NewRequest(http.MethodDelete, "/api/blocked-ips?ip=203.0.113.19", nil)
	rec4 := httptest.NewRecorder()
	h.HandleUnblockIP(rec4, req4)
	if rec4.Code != http.StatusOK {
		t.Fatalf("DELETE /api/blocked-ips got %d, want 200", rec4.Code)
	}
}
