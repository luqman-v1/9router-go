package dashboard

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"9router/proxy/internal/auth"
)

// The setup endpoint is anonymous by necessity (a fresh install has no
// session), so the tests pin the two gates that make that safe: a stored
// password hash and a non-loopback peer must both be refused, and the local
// fresh-install path must leave a working login behind.

func postSetup(t *testing.T, h *DashboardHandler, password string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"password":` + mustJSON(t, password) + `}`
	req := httptest.NewRequest(http.MethodPost, "/api/setup", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:51000"
	rec := httptest.NewRecorder()
	h.HandlePostSetup(rec, req)
	return rec
}

func TestHandlePostSetup_SetsPasswordAndIssuesSession(t *testing.T) {
	authTestEnv(t)
	auth.ResetLoginLimiter()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	h := NewDashboardHandler(repo)
	rec := postSetup(t, h, "first-run-password")
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/setup status = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	// The session cookie must be set: the operator lands in the dashboard they
	// just unlocked, without a second form.
	if len(rec.Result().Cookies()) == 0 {
		t.Fatalf("POST /api/setup set no session cookie")
	}

	// The stored hash must verify against the password that was sent.
	raw, err := repo.GetSettingsRaw()
	if err != nil {
		t.Fatalf("GetSettingsRaw: %v", err)
	}
	hash, _ := raw["password"].(string)
	if hash == "" {
		t.Fatalf("no password hash stored after setup")
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte("first-run-password")) != nil {
		t.Fatalf("stored hash does not verify against the setup password")
	}

	// A second POST must be refused: the endpoint is not a password reset.
	rec2 := postSetup(t, h, "second-attempt")
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("second POST /api/setup status = %d, want 403", rec2.Code)
	}
}

func TestHandlePostSetup_RefusesRemotePeer(t *testing.T) {
	authTestEnv(t)
	auth.ResetLoginLimiter()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	h := NewDashboardHandler(repo)
	body := `{"password":"first-run-password"}`
	req := httptest.NewRequest(http.MethodPost, "/api/setup", strings.NewReader(body))
	req.RemoteAddr = "192.0.2.10:51000" // TEST-NET-1, not loopback
	rec := httptest.NewRecorder()
	h.HandlePostSetup(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("remote POST /api/setup status = %d, want 403", rec.Code)
	}

	raw, err := repo.GetSettingsRaw()
	if err != nil {
		t.Fatalf("GetSettingsRaw: %v", err)
	}
	if hash, _ := raw["password"].(string); hash != "" {
		t.Fatalf("remote setup stored a password hash")
	}
}

func TestHandlePostSetup_RejectsShortPassword(t *testing.T) {
	authTestEnv(t)
	auth.ResetLoginLimiter()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	h := NewDashboardHandler(repo)
	rec := postSetup(t, h, "short")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("short-password POST /api/setup status = %d, want 400", rec.Code)
	}
}

func TestHandleGetSetup_ReportsFreshAndConfigured(t *testing.T) {
	authTestEnv(t)
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	h := NewDashboardHandler(repo)

	req := httptest.NewRequest(http.MethodGet, "/api/setup", nil)
	req.RemoteAddr = "127.0.0.1:51000"
	rec := httptest.NewRecorder()
	h.HandleGetSetup(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/setup status = %d, want 200", rec.Code)
	}
	var got struct {
		Status       bool   `json:"status"`
		DatabaseType string `json:"databaseType"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !got.Status {
		t.Fatalf("fresh install reported status=false")
	}
	if got.DatabaseType != "sqlite" {
		t.Fatalf("databaseType = %q, want sqlite", got.DatabaseType)
	}

	// Once a password exists the wizard is done.
	storePassword(t, repo, "configured-password")
	rec2 := httptest.NewRecorder()
	h.HandleGetSetup(rec2, req)
	var got2 struct {
		Status bool `json:"status"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &got2); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got2.Status {
		t.Fatalf("configured install reported status=true")
	}
}
